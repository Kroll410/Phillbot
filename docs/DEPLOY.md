# Розгортання вебверсії PhillBot

Вебверсія — той самий інтерфейс у браузері для кількох стрімерів. Вхід через Twitch, доступ мають лише
логіни з білого списку (сторінка **/admin**, бачить лише адміністратор). Кожен стрімер працює лише зі своїм каналом.

Образ збирається з `app/Dockerfile` (Go з тегом `server`, без CGO; база — SQLite на томі `/data` або PostgreSQL).

## 1. Застосунок Twitch

[dev.twitch.tv/console/apps](https://dev.twitch.tv/console/apps) → Register Your Application:

- **OAuth Redirect URL:** `https://<ваш-домен>/auth/callback` (точно як у `PHILLBOT_PUBLIC_URL` + `/auth/callback`);
- **Client Type:** **Confidential**; після створення — **New Secret**.

Десктопний застосунок (Public, Device Code) для вебверсії не підходить, тож для неї потрібен окремий.

## 2. Змінні оточення

| Змінна | Призначення |
|---|---|
| `PHILLBOT_PUBLIC_URL` | зовнішня https-адреса сервісу, напр. `https://phillbot.fly.dev` — з неї будуються вхід і посилання на віджети |
| `TWITCH_CLIENT_ID` / `TWITCH_CLIENT_SECRET` | застосунок Twitch з кроку 1 (**секрет — лише в секретах хостингу**) |
| `PHILLBOT_KEY` | ≥ 32 випадкові символи: ним шифруються токени каналів у БД. **Не змінюйте** — інакше всім доведеться увійти знову |
| `PHILLBOT_ADMIN` | логін Twitch адміністратора; він додає інших на `/admin` |
| `PHILLBOT_ADDR` | адреса сервера; у контейнері `0.0.0.0:8080` |
| `PHILLBOT_DATA` | тека з базою SQLite `phillbot.db`; у контейнері `/data` (том). Не використовується, якщо задано `PHILLBOT_DATABASE_URL` |
| `PHILLBOT_DATABASE_URL` | необов'язково: `postgres://…` або `postgresql://…` — тоді сервер працює на PostgreSQL замість SQLite (див. «База даних») |
| `PHILLBOT_DEV=1` | розробка без входу — **лише локально** (на `127.0.0.1`); у контейнері сервер відмовиться стартувати |

Без `TWITCH_CLIENT_ID`, `TWITCH_CLIENT_SECRET`, `PHILLBOT_KEY` і `PHILLBOT_ADMIN` сервер не стартує.

Згенерувати ключ: `openssl rand -hex 32` (PowerShell: `-join ((1..32) | % { '{0:x2}' -f (Get-Random -Max 256) })`).

## 3. Локальний запуск

Інтерфейс без входу:

```bash
cd app
PHILLBOT_DEV=1 go run -tags server .
```

(PowerShell: `$env:PHILLBOT_DEV="1"; go run -tags server .`)

Контейнер (порт лише на localhost; вхід через Twitch працюватиме, якщо в застосунку Twitch додано
Redirect URL `http://localhost:8080/auth/callback`):

```bash
cd app
docker build -t phillbot --build-arg VERSION=dev .
docker run --rm -p 127.0.0.1:8080:8080 -v phillbot-data:/data \
  -e PHILLBOT_PUBLIC_URL=http://localhost:8080 -e TWITCH_CLIENT_ID=… -e TWITCH_CLIENT_SECRET=… \
  -e PHILLBOT_KEY=… -e PHILLBOT_ADMIN=<ваш_логін> phillbot
```

## 4. Fly.io

Приклад конфігурації — `fly.toml` у корені `subday-bot`.

```bash
fly launch --no-deploy --copy-config -c fly.toml   # задайте унікальну назву app
fly volumes create phillbot_data --size 1 --region waw
fly secrets set TWITCH_CLIENT_ID=… TWITCH_CLIENT_SECRET=… PHILLBOT_KEY=… PHILLBOT_ADMIN=<ваш_логін>
cd app
fly deploy -c ../fly.toml --dockerfile Dockerfile --build-arg VERSION=$(git describe --tags --always)
```

Налаштування: один інстанс, том `phillbot_data` → `/data`, `internal_port = 8080`, `force_https`, `auto_stop_machines = false`
(таймери й EventSub мають працювати постійно), health check `GET /`. Не масштабуйте понад один інстанс:
стан каналів живе в пам'яті процесу, а SQLite-том підключається лише до однієї машини.

`PHILLBOT_PUBLIC_URL` задається в `[env]` у `fly.toml` і має збігатися з реальною https-адресою
та з Redirect URL у застосунку Twitch.

## Donatello

У вебверсії ngrok не потрібен: «Загальні налаштування → Донати Donatello» показують посилання
`https://<домен>/donatello/<секрет каналу>`. Його й ключ колбека вставте в Donatello → API → Колбеки.

## Що ще не перенесено у вебверсію

- Картинка в центрі колеса з власного файлу (емодзі каналу працюють).

## База даних

Вибір за змінною `PHILLBOT_DATABASE_URL`:

- не задано (або не `postgres://` / `postgresql://`) — SQLite, файл `phillbot.db` у `PHILLBOT_DATA` (потрібен том, один інстанс);
- `postgres://…` або `postgresql://…` — PostgreSQL. Схема створюється сама при старті (`CREATE TABLE IF NOT EXISTS`),
  том для даних не потрібен. Драйвер чистий Go (pgx), без CGO.

Десктопна версія завжди використовує лише SQLite.

### Fly Postgres / Neon

**Neon** (найпростіше): створіть проєкт на [neon.tech](https://neon.tech), скопіюйте рядок підключення (Connection string)
й задайте його секретом:

```bash
fly secrets set PHILLBOT_DATABASE_URL='postgres://користувач:пароль@ep-….neon.tech/neondb?sslmode=require'
```

**Fly Postgres / Managed Postgres:**

```bash
fly postgres create --name phillbot-db --region waw
fly postgres attach phillbot-db -a <назва_app>   # сам задасть секрет DATABASE_URL
```

`attach` створює секрет `DATABASE_URL`, а сервер читає `PHILLBOT_DATABASE_URL`, тому скопіюйте рядок:
`fly secrets set PHILLBOT_DATABASE_URL='<той самий рядок>'` (і за потреби `fly secrets unset DATABASE_URL`).
У внутрішній мережі Fly (`*.flycast`, `*.internal`) додайте `?sslmode=disable`.

Після переходу том `phillbot_data` більше не потрібен. Дані зі старого `phillbot.db` автоматично не переносяться:
БД Postgres починається порожньою (канали входять заново).

### Перевірка локально

```bash
docker run --rm -d --name phillbot-pg -e POSTGRES_PASSWORD=secret -p 127.0.0.1:55432:5432 postgres:17
cd app
PHILLBOT_TEST_PG='postgres://postgres:secret@127.0.0.1:55432/postgres?sslmode=disable' go test -tags server -run Postgres -v .
docker rm -f phillbot-pg
```

Без `PHILLBOT_TEST_PG` ці тести пропускаються.
