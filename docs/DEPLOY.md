# Розгортання вебверсії PhillBot

Вебверсія — той самий інтерфейс у браузері для кількох стрімерів. Вхід через Twitch, доступ мають лише
логіни з білого списку (сторінка **/admin**, бачить лише адміністратор). Кожен стрімер працює лише зі своїм каналом.

Образ збирається з `app/Dockerfile` (Go з тегом `server`, без CGO, SQLite на томі `/data`).

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
| `PHILLBOT_DATA` | тека з базою `phillbot.db`; у контейнері `/data` (том) |
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

## Що ще не перенесено у вебверсію

- Донати Donatello (колбек на `/donatello/<секрет>` замість ngrok) — наступний етап.
- Картинка в центрі колеса з власного файлу (емодзі каналу працюють).
- Імпорт даних з десктопного `PhillBot.exe`.
- PostgreSQL замість SQLite (за `docs/PLAN-cloud-db.md`).
