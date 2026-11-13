# Розгортання вебверсії PhillBot

> ## ⚠️ УВАГА: ДО ПОЯВИ ВХОДУ ЧЕРЕЗ TWITCH І БІЛОГО СПИСКУ (НАСТУПНИЙ ЕТАП) СЕРВЕР НЕ МОЖНА ВІДКРИВАТИ В ІНТЕРНЕТ — У НЬОМУ НЕМАЄ АВТЕНТИФІКАЦІЇ.
> Не деплойте на Fly.io чи будь-який публічний хостинг, а порт контейнера публікуйте лише на `127.0.0.1`.

Образ збирається з `app/Dockerfile` (Go з тегом `server`, без CGO, SQLite на томі `/data`).

## Змінні оточення

| Змінна | Призначення | У контейнері |
|---|---|---|
| `PHILLBOT_ADDR` | адреса сервера | `0.0.0.0:8080` |
| `PHILLBOT_DATA` | тека з базою `phillbot.db` | `/data` (том) |
| `PHILLBOT_PUBLIC_URL` | зовнішня адреса сервісу, з якої будуються посилання на віджети OBS і (далі) callback Twitch, напр. `https://phillbot.fly.dev` | задайте обов'язково |
| `PHILLBOT_DEV=1` | режим без входу | **у контейнері не вмикати** — працює лише на `127.0.0.1`, а контейнер слухає `0.0.0.0` (сервер відмовиться стартувати) |

## Локальний запуск

Збірка та запуск контейнера (порт лише на localhost хоста):

```bash
cd app
docker build -t phillbot --build-arg VERSION=dev .
docker run --rm -p 127.0.0.1:8080:8080 -v phillbot-data:/data -e PHILLBOT_PUBLIC_URL=http://127.0.0.1:8080 phillbot
```

Інтерфейс без входу (`PHILLBOT_DEV=1`) доступний лише поза контейнером:

```bash
cd app
PHILLBOT_DEV=1 go run -tags server .
```

(PowerShell: `$env:PHILLBOT_DEV="1"; go run -tags server .`)

## Fly.io

Приклад конфігурації — `fly.toml` у корені `subday-bot`. **Лише після появи входу й білого списку.**

```bash
fly launch --no-deploy --copy-config -c fly.toml   # задайте унікальну назву app
fly volumes create phillbot_data --size 1 --region waw
cd app
fly deploy -c ../fly.toml --dockerfile Dockerfile --build-arg VERSION=$(git describe --tags --always)
```

Налаштування: один інстанс, том `phillbot_data` → `/data`, `internal_port = 8080`, `force_https`, `auto_stop_machines = false` (таймери й EventSub мають працювати постійно), health check `GET /`. Не масштабуйте понад один інстанс: SQLite-том підключається лише до однієї машини.

`PHILLBOT_PUBLIC_URL` задається в `[env]` у `fly.toml` (або `fly secrets set PHILLBOT_PUBLIC_URL=https://<app>.fly.dev`) і має збігатися з реальною https-адресою.

Згідно з `docs/PLAN-cloud-db.md` надалі SQLite на томі буде замінено керованою Postgres.
