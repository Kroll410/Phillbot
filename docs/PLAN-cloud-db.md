# План: PhillBot у хмарі — доступ за білим списком, база даних, CSV на вимогу

Мета: одна розгорнута копія PhillBot, у яку через браузер заходять стрімери з білого списку.
Кожен бачить лише свій канал. Десктопний `.exe` лишається як «офлайн-режим» на тому самому коді.

Замінює розділ 1 «Внутрішня база даних» у [PLAN-db-obs-chat.md](PLAN-db-obs-chat.md).
Той план розраховував на один канал і локальну SQLite.

---

## 1. Як це виглядає для користувача

1. Стрімер відкриває `https://<домен>` і натискає «Увійти через Twitch». Client ID більше не вставляє.
2. Якщо його Twitch-акаунта немає в білому списку, бачить «Доступ за запрошенням» і далі не проходить.
3. Якщо є — бачить той самий інтерфейс, що й зараз (СабДей, Переглядовий, налаштування).
4. Віджет OBS: персональне посилання `https://<домен>/w/<секрет>`. Секрет можна перевипустити в налаштуваннях.
5. Donatello: колбек `https://<домен>/donatello/<секрет каналу>` із ключем. Ngrok більше не потрібен.
6. Marbles: кнопка «Експорт у Marbles» завантажує `.csv` у браузер. Запуск гри зі Steam лишається тільки в десктопній версії.
7. Адмін (власник сервісу) має сторінку «Доступ», де додає або прибирає логіни Twitch і бачить, хто коли заходив.

## 2. Архітектура

```
Браузер (той самий HTML/JS) ──HTTPS / WebSocket──► Go-сервер (один процес)
                                                    ├─ HTTP API замість Wails-біндингів
                                                    ├─ WS /events замість window.runtime.EventsOn
                                                    ├─ воркер на кожен активний канал:
                                                    │    EventSub WS, таймери, gifts-цикл, автооберти
                                                    ├─ /donatello/<секрет>, /w/<секрет> (віджет OBS)
                                                    └─ Store ──► PostgreSQL
```

- **Один процес, стан каналу в пам'яті.** Як зараз: `mutate` змінює `State`, `Store` дописує зміни в БД.
  Для білого списку на десятки каналів одного інстансу достатньо. Горизонтальне масштабування не потрібне.
- **Код спільний із десктопом.** `App` розбивається на:
  - `Channel` — стан і логіка одного каналу, тобто все, що зараз є в `App`;
  - транспорт: у десктопі Wails, у хмарі HTTP+WS.

  JS-шар `api()` у фронтенді вже єдина точка входу. У хмарі він робить
  `fetch('/api/<метод>', {body: args})` замість `window.go.main.App.<метод>`.
- **Twitch-авторизація:**
  - у хмарі — Authorization Code flow з **Confidential** клієнтом, Redirect URL `https://<домен>/auth/callback`;
  - у десктопі лишається Device Code;
  - токени кожного каналу в БД зашифровані AES-GCM, ключ береться зі змінної оточення `PHILLBOT_KEY`.
- **Сесії:** cookie `HttpOnly; Secure; SameSite=Lax`, сесія живе в БД. Для POST-запитів — CSRF-токен.
  Перевірка білого списку відбувається на кожен запит, тож видалення зі списку діє одразу.
- **EventSub:** один WebSocket на канал із токеном стрімера. Twitch дозволяє до 3 WS на користувача, тож цього досить.
  Якщо каналів стане більше 50, варто перейти на Conduits/вебхуки з app-токеном.
- **Що зникає в хмарі:**
  - ngrok;
  - буфер обміну;
  - `explorer /select`;
  - запуск Steam;
  - локальний OBS-сервер на 127.0.0.1 (віджет віддає сам сервер).

## 3. База даних

**PostgreSQL** (керована, з бекапами). У десктопі той самий інтерфейс `Store` реалізується через SQLite (`modernc.org/sqlite`, без CGO).
Схема однакова. Відмінності — лише типи (`BIGINT`/`INTEGER`, `JSONB`/`TEXT`).

Кожна таблиця з даними має `channel_id`: це ізоляція між стрімерами. Усі запити йдуть через `Store`,
який отримує `channel_id` із сесії, а не з тіла запиту.

```sql
-- доступ
CREATE TABLE users      (twitch_id TEXT PRIMARY KEY, login TEXT NOT NULL, display_name TEXT,
                         role TEXT NOT NULL DEFAULT 'streamer',          -- streamer | admin
                         allowed BOOLEAN NOT NULL DEFAULT TRUE, added_by TEXT, added_at BIGINT, last_login_at BIGINT);
CREATE TABLE sessions   (id TEXT PRIMARY KEY, twitch_id TEXT NOT NULL REFERENCES users, created_at BIGINT, expires_at BIGINT);
CREATE TABLE channels   (id TEXT PRIMARY KEY,                             -- twitch broadcaster id
                         owner_id TEXT NOT NULL REFERENCES users,
                         tokens_enc BYTEA,                                -- access/refresh, AES-GCM
                         widget_secret TEXT UNIQUE, don_secret TEXT UNIQUE, don_key_enc BYTEA,
                         settings JSONB NOT NULL,                         -- Settings СабДею
                         auction_cfg JSONB NOT NULL,                      -- AuctionSettings
                         live JSONB NOT NULL);                            -- дрібний живий стан: таймери, фаза, голосування, spin

-- СабДей
CREATE TABLE streams     (id BIGSERIAL PRIMARY KEY, channel_id TEXT NOT NULL REFERENCES channels, started_at BIGINT NOT NULL);
CREATE TABLE collections (id BIGSERIAL PRIMARY KEY, stream_id BIGINT NOT NULL REFERENCES streams ON DELETE CASCADE,
                          n INT NOT NULL, started_at BIGINT, finished_at BIGINT, settings JSONB NOT NULL, winner TEXT);
CREATE TABLE entries     (id TEXT PRIMARY KEY, collection_id BIGINT NOT NULL REFERENCES collections ON DELETE CASCADE,
                          redemption_id TEXT, ts BIGINT NOT NULL, user_id TEXT, login TEXT, display_name TEXT,
                          tier INT, months INT, gifts INT, text TEXT NOT NULL,
                          excluded BOOLEAN, manual BOOLEAN, donation BOOLEAN, don_points INT, uah NUMERIC(10,2),
                          color TEXT, badges JSONB, source TEXT,
                          UNIQUE (collection_id, redemption_id));        -- дедуплікація замість seenRedeems

-- аукціон
CREATE TABLE auctions (id BIGSERIAL PRIMARY KEY, channel_id TEXT NOT NULL REFERENCES channels,
                       started_at BIGINT, finished_at BIGINT, winner_lot TEXT, spice_at INT, merges JSONB);
CREATE TABLE lots     (id TEXT PRIMARY KEY, auction_id BIGINT NOT NULL REFERENCES auctions ON DELETE CASCADE,
                       name TEXT, key TEXT, amount INT, bids INT, users JSONB, out_round INT, hidden BOOLEAN, ord INT);
CREATE TABLE bids     (id TEXT PRIMARY KEY, auction_id BIGINT NOT NULL REFERENCES auctions ON DELETE CASCADE,
                       redemption_id TEXT, ts BIGINT, user_id TEXT, login TEXT, display_name TEXT, text TEXT,
                       amount INT, status TEXT, lot_id TEXT, donation BOOLEAN, uah NUMERIC(10,2),
                       UNIQUE (auction_id, redemption_id));

-- допоміжне
CREATE TABLE gifts     (channel_id TEXT, gifter_id TEXT, ts BIGINT, count INT, PRIMARY KEY (channel_id, gifter_id, ts));
CREATE TABLE donations (channel_id TEXT, pub_id TEXT, ts BIGINT, client_name TEXT, amount NUMERIC(10,2), currency TEXT,
                        message TEXT, used_for TEXT, PRIMARY KEY (channel_id, pub_id));   -- замінює DonSeen
CREATE TABLE audit_log (id BIGSERIAL PRIMARY KEY, channel_id TEXT, actor TEXT, ts BIGINT, action TEXT, details JSONB);
CREATE TABLE wheel_images (channel_id TEXT, name TEXT, data BYTEA, mime TEXT, PRIMARY KEY (channel_id, name));
```

- Бали в БД **не зберігаються**: їх рахують `pointsOf` / `entryPoints` із тіра, стажу, подарунків і `settings` збору.
- Запис іде дрібними операціями: нове замовлення — `INSERT`, правка — `UPDATE` одного рядка, таймери — `UPDATE channels.live`.
  Повного перезапису стану на кожен клік більше немає.
- Міграції схеми — нумеровані SQL у коді і `schema_version` у таблиці `meta`.

## 4. CSV лише на гру або на вимогу

| Що | Зараз | Буде |
|---|---|---|
| `exports/<стрім>/збір_NN.csv` | автоматично після кожного збору | **прибрати**; дані лежать у БД |
| `streams/*.json`, `history/*.json`, `subday_state.json` | повний перезапис на кожну зміну | **прибрати**; БД |
| Файл для Marbles | `marbles/subday_marbles.csv` + копія в `marbles/history` | лише при «Експорт у Marbles»: десктоп пише файл, хмара віддає його на завантаження; копії в історію немає (є в БД) |
| Експорт за весь час | — | **вже зроблено:** «Загальні налаштування → Експорт за весь час»: СабДей .csv, Аукціон .csv, Усе .zip |
| Таблиця одного збору | автоматичний файл | кнопка «.csv» біля збору в «Журналі стрімів» |

У налаштуваннях СабДею прибирається блок «Дані» («Відкрити теку з .csv … зберігається автоматично»).

## 5. Перенесення наявних даних

- **Десктоп, перший запуск із БД:** імпорт `subday_state.json`, `streams/*.json`, `history/subday_*.json`
  і `history/auction_*.json` в одній транзакції. Після цього файли перейменовуються в `*.migrated`, а не видаляються.
- **З десктопу в хмару:** не потрібно — вебверсія стартує з чистої бази (рішення 2026-11-13).

## 6. Безпека

- Білий список перевіряється на кожен запит. Новий логін, якого немає в `users`, отримує 403 і запис в `audit_log`.
- Перший адмін задається змінною `PHILLBOT_ADMIN=<twitch id>`.
- Секрети (`tokens_enc`, `don_key_enc`) зашифровані. `PHILLBOT_KEY` живе лише в секретах хостингу.
- `widget_secret` і `don_secret` — 128 біт із `crypto/rand`, їх можна перевипустити.
- Ліміти: тіло запиту до 64 КБ, картинка колеса до 5 МБ, rate limit на `/donatello/*` і `/auth/*`.
- Бекапи: щоденні знімки керованої БД із зберіганням 7 днів, плюс експорт `.zip` на вимогу самого стрімера.

## 7. Хостинг (рекомендація)

- **Fly.io** (один невеликий інстанс, підтримує WebSocket, власний домен з TLS) + **Neon** або **Fly Postgres**.
  Для білого списку очікувано $5–10/міс.
- Альтернатива — VPS (Hetzner) з Docker Compose: Go-сервер, Postgres і Caddy з автоматичним TLS.
  Це дешевше, але бекапи й оновлення доведеться робити самому.
- Збірка: `Dockerfile` (multi-stage, `CGO_ENABLED=0`). Деплой — GitHub Actions на тег, як зараз реліз exe.

## 8. Порядок робіт

1. **Store + SQLite у десктопі.** Інтерфейс `Store` і SQLite-реалізація, міграція з JSON, прибрати автоматичні CSV.
   Поведінка програми не змінюється, а тести перевіряють, що стан переживає перезапуск.
2. **Розділити `App` → `Channel` + транспорт.** Wails-обгортка лишається для exe.
3. **HTTP-сервер:** `/api/*`, WS `/events`, статичний фронтенд, віджет `/w/<секрет>`.
4. **Вхід через Twitch + білий список + сесії + сторінка «Доступ».**
5. **PostgreSQL-реалізація Store** і `channel_id` скрізь, плюс тести ізоляції: канал A не бачить даних каналу B.
6. **Воркери каналів:** EventSub, подарунки, таймери. Старт — при вході стрімера або коли таймер активний, зупинка — після бездіяльності.
7. **Donatello через `/donatello/<секрет>`,** Marbles .csv на завантаження.
8. **Docker, деплой, домен, бекапи,** а потім пробний стрім з одним каналом.

Кроки 1–2 корисні й без хмари, бо прибирають повний перезапис JSON. Кроки 3–8 — це вже хмарна версія.
