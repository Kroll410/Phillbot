# План: внутрішня база даних, адаптація під OBS, вікно лайв-чату

Документ планування наступного етапу після першої Wails-версії (`app/`). Код у `app/` на момент написання:
`app.go` (App, таймер, замовлення, методи для UI), `state.go` (модель `State` + `subday_state.json`),
`twitch.go` (Device Code, Helix, EventSub), `export.go` (Marbles, CSV), `ws.go` (WS-клієнт), фронтенд у `app/frontend/dist/`.

Ключові обмеження: один `.exe` для Windows, **без CGO** (Wails v2 на Windows збирається без gcc), мало RAM поруч з OBS,
інтерфейс українською, секрети поза git.

---

## 1. Внутрішня база даних

### Як зараз
- `subday_state.json` — **повний перезапис** усього стану (налаштування + таймер + усі замовлення + `seenRedeems` до 3000 id)
  на **кожну** зміну: кожен redeem, кожне редагування клітинки, кожен +1 хв. На 1–2 тис. замовлень це вже десятки–сотні КБ
  на кожен клік; атомарність є (`.tmp` + rename), але немає історії змін і пошуку.
- Архіви — `history/subday_*.json` (повний дамп при «Новий збір»). Перегляду історії в UI немає.
- Кеш бейджів (`badgeCache`) — лише в пам'яті, губиться при перезапуску.

### Варіанти (усі без CGO)

| Варіант | + до exe | Диск | Запити / зручність | Ризики |
|---|---|---|---|---|
| **`modernc.org/sqlite`** (SQLite, транспільований у Go) | ≈ +5–8 МБ (з `-s -w`) | компактно, `VACUUM` повертає місце | SQL: фільтри, агрегати, історія, індекси; WAL → дешеві дрібні записи | більший exe, повільніша збірка (~1–2 хв перший раз) |
| `github.com/ncruces/go-sqlite3` (SQLite у WASM через wazero) | ≈ +4–6 МБ | те саме | те саме | трохи повільніший старт, менш поширений |
| `go.etcd.io/bbolt` (KV, B+tree) | ≈ +0.3 МБ | файл **не зменшується** без компакції, значення — JSON | лише ключ/діапазон, агрегати руками | ручні індекси, mmap-файл займає місце |
| JSONL-журнал (append-only) + gzip архівів | 0 | найменше (gzip ~10×) | все в пам'яті, пошук по історії — читати файли | компакція/відновлення своїми руками, легко зламати формат |

### Рекомендація: SQLite через `modernc.org/sqlite`
- Обсяг даних малий (оцінка: запис замовлення ≈ 150–250 Б; 300 замовлень × 200 стрімів ≈ 60 тис. рядків ≈ **10–15 МБ** у SQLite;
  той самий обсяг у JSON-архівах ≈ 25–40 МБ). Для чату з ретеншном 14 днів — ще кілька МБ.
- Дрібні записи замість перезапису всього файла: `INSERT` одного рядка на redeem, `UPDATE` одного поля на редагування.
- Режим: `PRAGMA journal_mode=WAL; synchronous=NORMAL; foreign_keys=ON; busy_timeout=3000`. Один `*sql.DB` з `SetMaxOpenConns(1)` (простота, без блокувань).
- Бекап: `VACUUM INTO 'backup/subday_YYYY-MM-DD.db'` раз на день/при «Новий збір», зберігати 7 останніх.
- Пам'ять: модель `State` у пам'яті лишається джерелом для UI (як зараз), БД — лише персистентність. Тобто логіка `mutate` не ламається.
- Альтернатива, якщо розмір exe критичний: bbolt (+0.3 МБ) з тими самими «таблицями» як бакетами — інтерфейс `Store` нижче дозволяє замінити.

### Схема (файл `subday.db` поруч з exe)
```sql
CREATE TABLE meta      (key TEXT PRIMARY KEY, value TEXT NOT NULL);            -- schema_version, migrated_from_json
CREATE TABLE settings  (key TEXT PRIMARY KEY, value TEXT NOT NULL);            -- JSON-значення: 'subday' → Settings, 'obs' → ObsSettings, 'chat' → ChatSettings

CREATE TABLE collections (                -- один «збір» (сесія СабДею / майбутнього Переглядового)
  id           INTEGER PRIMARY KEY,
  mode         TEXT NOT NULL DEFAULT 'subday',   -- subday | viewing (майбутнє)
  reward_id    TEXT, reward_title TEXT,
  status       TEXT NOT NULL,                    -- idle | running | paused | finished | archived
  ends_at      INTEGER, remaining_ms INTEGER,
  started_at   INTEGER, finished_at INTEGER, archived_at INTEGER,
  settings_json TEXT NOT NULL                    -- знімок правил балів на момент збору
);
CREATE INDEX collections_status ON collections(status);

CREATE TABLE entries (
  id            TEXT PRIMARY KEY,                -- як зараз newID()
  collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
  redemption_id TEXT UNIQUE,                     -- замінює seenRedeems (дедуплікація на рівні БД)
  ts            INTEGER NOT NULL,
  user_id TEXT, login TEXT NOT NULL, display_name TEXT NOT NULL,
  tier INTEGER NOT NULL DEFAULT 0, months INTEGER NOT NULL DEFAULT 0,
  text TEXT NOT NULL, norm_key TEXT NOT NULL,    -- normKey() для швидкого GROUP BY
  excluded INTEGER NOT NULL DEFAULT 0, manual INTEGER NOT NULL DEFAULT 0,
  source TEXT
);
CREATE INDEX entries_coll_ts ON entries(collection_id, ts);

CREATE TABLE users (                             -- кеш чатерсів: підписка, колір, останні бейджі
  user_id TEXT PRIMARY KEY, login TEXT, display_name TEXT, color TEXT,
  tier INTEGER, months INTEGER, badges_json TEXT, updated_at INTEGER
);

CREATE TABLE seen_rewards (reward_id TEXT PRIMARY KEY, title TEXT, count INTEGER, last_ts INTEGER, sample TEXT, last_user TEXT);

CREATE TABLE chat_messages (                     -- див. п. 3; ретеншн N днів
  id TEXT PRIMARY KEY, ts INTEGER NOT NULL, user_id TEXT, login TEXT, display_name TEXT, color TEXT,
  badges_json TEXT, fragments_json TEXT, text TEXT, reward_id TEXT, deleted INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX chat_ts ON chat_messages(ts);
```
Бали **не зберігаються** — рахуються з `tier/months` + `settings_json` (як зараз `pointsOf` у фронтенді), тож зміна правил перераховує все.

### Міграція з JSON
1. На старті, якщо `subday.db` порожня (`meta.schema_version` нема) і є `subday_state.json`:
   створити колекцію з `State.Session` + `Settings`, вставити `Entries`, `SeenRewards`, `SeenRedeems` → `entries.redemption_id` не мапляться (там нема зв'язку) — просто пропустити.
2. Кожен `history/subday_*.json` → колекція зі `status='archived'`.
3. Усе в одній транзакції; після успіху перейменувати `subday_state.json` → `subday_state.json.migrated`, `history/` → `history.migrated/` (не видаляти).
4. Міграції схеми — нумеровані SQL-рядки в коді (`migrations[]`), `meta.schema_version`.

### Зміни в коді
- **Новий `app/store.go`**: інтерфейс `Store` і реалізація SQLite:
  `Open(path)`, `LoadActive() (State, error)`, `InsertEntry`, `UpdateEntry`, `DeleteEntry`, `SaveTimer(Timer)`, `SaveSettings`,
  `UpsertSeenReward`, `UpsertUser`, `ArchiveActive() (id)`, `ListCollections(limit, offset)`, `GetCollection(id)`, `InsertChat`, `PruneChat(before)`, `Backup()`.
- **Новий `app/migrate.go`**: імпорт JSON → БД.
- **`app/app.go`**: `saveLocked()` → точкові виклики Store всередині кожного методу (`StartTimer` → `SaveTimer`, `onRedemption` → `InsertEntry` з `redemption_id`, `UpdateEntry` → `UpdateEntry`…); `NewSession` → `ArchiveActive` + нова колекція.
  Нові методи для UI: `ListHistory()`, `OpenHistory(id)` (тільки читання, ті ж таблиці), `ExportHistory(id)`, `DeleteHistory(id)`.
- **`app/state.go`**: `loadState/writeFileAtomic` лишаються лише для міграції; `SeenRedeems` прибрати з моделі (дедуп через UNIQUE).
- **Фронтенд**: вкладка «Історія» (список зборів → та сама таблиця «Підсумок/Усі замовлення» у режимі читання, експорт CSV/Marbles).
- `go.mod`: `modernc.org/sqlite`.
- `.gitignore`: `subday.db*`, `backup/`.

**Обсяг:** ~1.5–2 дні (store + міграція + тести на міграцію/дедуп + вкладка історії).
**Ризики:** зростання exe до ~20 МБ; помилки міграції (пом'якшено: старі файли не видаляються); одночасний запуск двох копій
(вже є `SingleInstanceLock`); антивіруси інколи ріжуть нові невідомі exe — не пов'язано з БД, але exe стане більшим.

---

## 2. Адаптація під OBS

### Рекомендація: вбудований HTTP-сервер у застосунку + Browser Source
OBS не бачить внутрішній asset-server Wails, тому потрібен окремий `net/http` сервер (stdlib, 0 залежностей), що стартує в `startup()`.

- Адреса: **тільки `127.0.0.1`**, порт за замовчуванням `17841` (налаштовується; якщо зайнятий — пробуємо +1…+10 і показуємо фактичний у UI).
- Захист:
  - перевірка заголовка `Host` ∈ {`127.0.0.1:PORT`, `localhost:PORT`} — від DNS-rebinding зі сторінок у браузері;
  - віддаємо лише **публічний зріз** даних (без токенів, user_id, Client ID, шляхів до файлів);
  - ендпоінти, що щось змінюють (етап 4, док-панель), — лише з `?key=<випадковий токен>` з `config.json`, `POST`, без CORS.
- Оновлення: **SSE** (`text/event-stream`) — `EventSource` у браузері сам перепідключається, немає потреби у WS-сервері.
  Таймер шлемо як `endsAt` + `serverNow` (клієнт тікає сам, як головне вікно) → трафік мінімальний; троттлінг подій ≤ 4/с.
- Прозорий фон: `html,body{background:transparent}` (OBS Browser Source прозорий за замовчуванням).
- Параметри в URL: `?limit=10&scale=1.5&theme=dark|light|plain&hideIdle=1&font=…&align=left|center` — щоб стрімер не правив CSS.

### Сторінки оверлеїв
| URL | Що показує | Рекомендований розмір |
|---|---|---|
| `/overlay/timer` | великий таймер збору, статус («Збір замовлень!», «Пауза», «Збір завершено»), пульсація < 30 с, `hideIdle` ховає коли не йде | 600×200 |
| `/overlay/top` | топ-N замовлень: назва, бали, смужка частки, анімація зміни позицій | 500×600 |
| `/overlay/marbles` | лічильник: «N куль із M замовлень», загальні бали, к-сть чатерсів | 500×120 |
| `/overlay/last` | алерт останнього замовлення («Ніка замовив Dark Souls — 138 балів»), зникає через X с | 800×150 |
| `/overlay/chat` | чат (див. п. 3) | 400×700 |
| `/dock` (етап 4) | компактна панель керування для OBS Custom Browser Dock: таймер, +1/−1 хв, пауза, завершити | 320×400 |

### API
- `GET /api/overlay/state` → `{session:{status,endsAt,remainingMs}, serverNow, rewardTitle, totals:{entries,users,points,marbles}, top:[{name,points,count}], last:{user,text,points,ts}}`
- `GET /api/overlay/events` → SSE: `event: state` (повний зріз), `event: redeem` (нове замовлення), `event: chat` (повідомлення), `event: chatdel`.
- `GET /overlay/*` → статичні файли з `//go:embed overlay/*` (окремо від `frontend/dist`, щоб не тягнути головний UI).
- (етап 4) `POST /api/control/{start|pause|finish|add?ms=60000}?key=…`.

Агрегацію (top, бали) зараз робить фронтенд головного вікна. Для оверлеїв її треба **перенести в Go** (`aggregate.go`: `pointsOf`, `countedSet`, `normKey`, `aggregate`, `marbleLines` — бо частина вже була в `bot/marbles.go`) і віддавати готовим; головне вікно може теж брати з Go, щоб логіка була одна.

### obs-websocket (опційно, пізніше)
OBS 28+ має вбудований obs-websocket v5 (`ws://127.0.0.1:4455`, auth SHA-256). Можливості: при «Почати збір» увімкнути
джерело таймера/перемкнути сцену, при «Завершити» — сховати. Потрібно: підтримка `ws://` без TLS у `ws.go` (зараз `wsDial` завжди TLS),
пароль у `config.json`, налаштування «яке джерело/сцена». Цінність помірна — Browser Source з `hideIdle` закриває 80% випадку.

### Зміни в коді
- **Новий `app/overlay_server.go`**: `startOverlayServer(port)`, mux, Host-check, SSE-хаб (`subscribe/unsubscribe/broadcast`, буферизований канал на клієнта, дроп повільних).
- **Новий `app/aggregate.go`** (+ `aggregate_test.go`): порт логіки з `ui-draft.html`/`bot/marbles.go`.
- **Нова тека `app/overlay/`**: `timer.html`, `top.html`, `marbles.html`, `last.html`, `chat.html`, спільні `overlay.css`, `overlay.js` (EventSource + рендер).
- **`app/app.go`**: у `emitLocked()` додатково `overlayHub.Broadcast(publicSnapshot())` (з троттлінгом); `startup` — запуск сервера; `shutdown` — `srv.Shutdown`.
- **`app/state.go`**: `ObsSettings{Enabled bool; Port int; Key string; Theme string}`.
- **Фронтенд**: картка «OBS»: перемикач, порт, список URL з кнопкою «Копіювати», підказка «OBS → Джерела → + → Браузер → вставити URL, 600×200», кнопка «Відкрити в браузері» для перевірки.

**Обсяг:** сервер + SSE + 3 оверлеї (timer/top/marbles) ≈ 1.5 дня; `last` + док + control API ≈ +1 день; obs-websocket ≈ +1 день.
**Ризики:** брандмауер Windows (на 127.0.0.1 зазвичай не питає — саме тому НЕ слухати `0.0.0.0`); зайнятий порт; кеш OBS Browser Source
(додати `Cache-Control: no-store`); CPU OBS при частих перемальовках — CSS-анімації замість JS, троттлінг.

---

## 3. Вікно лайв-чату Twitch

### Джерело даних — вже є
EventSub `channel.chat.message` (скоуп `user:read:chat`) уже підписаний у `twitch.go`, але зараз з події беруться лише бейджі.
Подія містить усе потрібне: `message_id`, `chatter_user_login/name`, **`color`**, `badges[]`, `message.text` і **`message.fragments[]`**
(`text` / `emote{id}` / `cheermote` / `mention`), `message_type`, `reply`, **`channel_points_custom_reward_id`**.

Додатково підписатися (той самий `user:read:chat`, нових скоупів не треба): `channel.chat.message_delete`, `channel.chat.clear`,
`channel.chat.clear_user_messages` — щоб видалені/забанені повідомлення зникали і в нас.

**Бонус для СабДею:** `channel_points_custom_reward_id` у повідомленні чату дозволяє **точно** зв'язати redeem з повідомленням
(і його бейджем/місяцями) замість поточної евристики «повідомлення від того ж user_id за останні 15 с» у `lookupSub`. Варто зробити разом із чатом.

### Відображення
- Емоути: `https://static-cdn.jtvnw.net/emoticons/v2/{id}/default/dark/1.0` (без авторизації).
- Бейджі: Helix `GET /chat/badges/global` і `GET /chat/badges?broadcaster_id=` (скоупи не потрібні) → кеш у пам'яті/таблиці, раз на старт.
- Колір ніка — з `color`; якщо порожній — детермінований з палітри Twitch за логіном; на темному фоні підняти яскравість занадто темних кольорів.
- Підсвітка: повідомлення з `channel_points_custom_reward_id == settings.RewardID` — рамка кольору акценту + бейдж «★ замовлення» + бали;
  інші нагороди — тонка підсвітка; згадки стрімера — жовтий фон.
- Буфер: кільце на 500 повідомлень у пам'яті; у БД (п. 1) — опційно, з ретеншном 14 днів (`PruneChat` на старті).
- Продуктивність: пакетний рендер раз на 100–200 мс, віртуальний список/обрізка DOM до 300 вузлів, автопрокрутка вимикається, коли користувач прокрутив угору (кнопка «↓ нові повідомлення»).

### Де показувати (Wails v2 = одне вікно)
1. **Вкладка/бічна панель «Чат» у головному вікні** — рекомендовано першим кроком (події через `wruntime.EventsEmit("chat", msg)`).
2. **«Відкрити в окремому вікні»** → сторінка `http://127.0.0.1:PORT/chat` з локального сервера (п. 2), відкрита через
   `msedge.exe --app=http://127.0.0.1:PORT/chat --window-size=420,800` — окреме вікно без адресного рядка (Edge є на кожній Win10/11);
   запасний варіант — браузер за замовчуванням. Це дає «друге вікно» без переходу на Wails v3.
3. **OBS-оверлей** `/overlay/chat` (прозорий, без інтерактиву, параметри `?fade=30&hideBots=1&fontSize=…`) та як OBS Custom Browser Dock.
4. Wails v3 (нативні кілька вікон) — перевірити статус стабільності на момент впровадження; перехід v2→v3 — окремий проєкт, зараз не рекомендується.

### Скоупи
| Потреба | Скоуп | Новий? |
|---|---|---|
| Читати чат, видалення/очищення | `user:read:chat` | ні, вже є |
| Бейджі, емоути | — | ні |
| **Писати в чат** від імені каналу (`POST /helix/chat/messages`) — напр. «Збір замовлень відкрито на 10 хв!» | `user:write:chat` | **так** |
| Писати від окремого бот-акаунта | `user:bot` (бот) + `channel:bot` (канал) або бот як модератор | так, і окремий вхід |
| Видаляти повідомлення / таймаут з вікна чату | `moderator:manage:chat_messages`, `moderator:manage:banned_users` | так |
| Керувати нагородою (вмикати/вимикати при старті збору) | `channel:manage:redemptions` (+ нагорода створена цим Client ID) | так |

Додавання будь-якого скоупу змусить усіх перелогінитися: `storedToken()` уже повертає `nil`, якщо скоупів бракує → стан `need_login`.
Пропозиція: тримати скоупи списком «базові + увімкнені функції» і просити нові лише коли користувач вмикає відповідну функцію.

### Зміни в коді
- **`app/twitch.go`**: розширити розбір `channel.chat.message` (повна структура `ChatMessage` з fragments/color/reward_id/reply);
  підписки на `message_delete`, `clear`, `clear_user_messages`; `fetchChatBadges()`; у `lookupSub` — пошук спершу за `reward_id`+`user_id`.
- **Новий `app/chat.go`**: кільцевий буфер, нормалізація повідомлення для UI, позначка «замовлення», трансляція в Wails-події та SSE-хаб, запис у Store (опційно).
- **`app/app.go`**: методи `GetChat(limit)`, `OpenChatWindow()` (запуск Edge `--app`), `SendChat(text)` (етап 4, `user:write:chat`).
- **Фронтенд**: вкладка «Чат» (рендер фрагментів, бейджі, емоути, підсвітка), налаштування чату (розмір шрифту, показ бейджів, ховати ботів за списком логінів).
- **`app/overlay/chat.html`**: та ж логіка рендеру, прозорий фон, затухання повідомлень.

**Обсяг:** вкладка чату + розширений парсинг + бейджі/емоути ≈ 1.5 дня; окреме вікно через Edge + оверлей чату ≈ 0.5–1 день (після п. 2);
відправка повідомлень/модерація ≈ +1 день.
**Ризики:** великі канали — сотні повідомлень/с (троттлінг рендеру обов'язковий); EventSub-ліміт — 3 WS-з'єднання на користувача
(ми використовуємо одне — ок); прапорець `--app` Edge може змінитися (є запасний варіант через звичайний браузер);
XSS — текст повідомлень лише через `textContent`/екранування, ніколи `innerHTML` з сирого тексту.

---

## Порядок впровадження

0. **Стабілізувати поточну Wails-версію** (тест на живому каналі, Marbles UTF-8/UTF-16, Ctrl+V у грі).
1. **`aggregate.go`** — перенести підрахунок балів/топу/куль у Go з тестами (потрібно і для оверлеїв, і для історії). ~0.5 дня.
2. **SQLite-сховище + міграція JSON + вкладка «Історія»**. ~1.5–2 дні.
3. **Локальний HTTP-сервер + SSE + оверлеї timer/top/marbles + картка «OBS»**. ~1.5 дня.
4. **Чат у головному вікні** (+ точне зв'язування redeem ↔ повідомлення через `channel_points_custom_reward_id`). ~1.5 дня.
5. **Чат в окремому вікні (Edge `--app`) і `/overlay/chat`, `/overlay/last`**. ~1 день.
6. **Опційно:** OBS-док з керуванням таймером, obs-websocket, відправка в чат (`user:write:chat`), керування нагородою (`channel:manage:redemptions`). ~2–3 дні.

Пункти 3–5 можна робити до 2, якщо оверлеї потрібні швидше: вони не залежать від БД (чат-історія в БД додається потім).
