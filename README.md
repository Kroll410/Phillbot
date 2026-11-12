# СабДей

Інструмент для Twitch-стрімів: збирає замовлення через нагороду за бали каналу «СабДей Замовлення»,
рахує бали підписників (тір × місяці підписки) і експортує результат у Marbles on Stream.

## Стан
- `app/` — **програма SubDay.exe** (Wails v2: Go + WebView2, один exe ~12 МБ): вхід у Twitch,
  вибір нагороди зі списку, таймер збору (10 хв, ±1 хв, пауза, завершити — redeem-и пишуться лише поки йде таймер),
  таблиці «Підсумок» і «Усі замовлення» з редагуванням, правила балів, експорт у Marbles / Excel (CSV) / JSON,
  журнал подій. Стан — `subday_state.json` поруч з exe, архів зборів — `history/`.
- `bot/` — консольний чорновик (перевірений на живому каналі), з нього перенесено логіку.
  `bot/ui-draft.html` — макет, з якого зроблено інтерфейс.
- `docs/PLAN-db-obs-chat.md` — план: внутрішня БД, оверлеї для OBS, вікно чату Twitch.

## Правила балів (за замовчуванням)
- бали = множник тіра (1/2/3) × місяці підписки; не-підписник — 0
- від одного чатерса рахується останнє замовлення; однакові замовлення зливаються
- Marbles: кожен бал = одна куля, ліміт гри 1000 (пропорційне зменшення)

## Збірка програми (Windows)
Потрібні Go і Wails CLI (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`); Node.js не потрібен —
фронтенд — чистий HTML/JS у `app/frontend/dist`.
```
cd app
go test .
wails build -clean -skipbindings -trimpath -ldflags "-s -w"
```
Результат — `app/build/bin/SubDay.exe`. Сам exe в git не комітиться (`*.exe` у `.gitignore`).

## GitHub Actions і релізи
`.github/workflows/build.yml` на кожен пуш у `main` і PR проганяє тести й збирає exe (артефакт запуску, 14 днів).
Реліз — пушнути тег, Action сам створить GitHub Release з `SubDay.exe`:
```
git tag v0.1.0
git push origin v0.1.0
```

## Збірка чорновика
```
cd bot
GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o SubDayBot-test.exe .
```
