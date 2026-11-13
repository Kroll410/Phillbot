//go:build !server

package main

// Десктопна оболонка (Wails): вікно, вхід через Device Code, файли поруч з exe.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// App — десктопна оболонка (Wails): вікно, файли поруч з exe, локальний сервер віджетів, ngrok, вікно чату.
// Методи каналу (*Channel) доступні інтерфейсу через вбудовування.
type App struct {
	*Channel
	ctx context.Context

	baseDir, configFile, tokensFile, stateFile, historyDir, exportsDir, streamsDir string

	tunStop context.CancelFunc // тунель ngrok для колбеків Donatello

	chatWinClients int         // підключені вікна чату (SSE з ?window=1)
	chatWinPending bool        // вікно запускається, ще не підключилось
	chatWinProc    *os.Process // процес Edge --app

	overlay *overlayServer
}

func NewApp() *App {
	a := &App{}
	a.Channel = newChannel(desktopHost{a})
	if exe, err := os.Executable(); err == nil {
		a.baseDir = filepath.Dir(exe)
	} else {
		a.baseDir = "."
	}
	a.configFile = filepath.Join(a.baseDir, "config.json")
	a.tokensFile = filepath.Join(a.baseDir, "tokens.json")
	a.stateFile = filepath.Join(a.baseDir, "subday_state.json")
	a.historyDir = filepath.Join(a.baseDir, "history")
	a.streamsDir = filepath.Join(a.baseDir, "streams")
	a.exportsDir = filepath.Join(a.baseDir, "exports")
	a.openStore()
	a.initStreamLocked()
	return a
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	var cfg Config
	readJSON(a.configFile, &cfg)
	a.clientID = strings.TrimSpace(cfg.ClientID)
	if a.clientID == "" {
		a.auth = AuthInfo{Status: "no_client"}
	} else {
		go a.authFlow(false)
	}
	go a.ticker(ctx) // запуск продовжує останній стрім і останній аукціон (стан зберігається між запусками)
	a.overlay = newOverlayServer(a)
	a.mu.Lock()
	port := a.st.Settings.ObsPort
	a.mu.Unlock()
	a.overlay.start(port)
	a.restartTunnel()
	if a.storeErr != "" {
		a.logf("⚠ %s", a.storeErr)
	}
	if low := strings.ToLower(a.baseDir); strings.Contains(low, `\temp\`) || strings.Contains(low, `\tmp\`) {
		a.logf("⚠ Програму запущено з тимчасової папки (архіву) — налаштування й токени зникнуть. Розпакуйте PhillBot.exe у звичайну папку.")
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	if a.tunStop != nil {
		a.tunStop()
	}
	a.mu.Unlock()
	if a.overlay != nil {
		a.overlay.stop()
	}
	a.mu.Lock()
	a.saveLocked()
	if a.store != nil {
		_ = a.store.Close() // переносить журнал WAL у phillbot.db
		a.store = nil
	}
	a.mu.Unlock()
}

// openStore — відкрити phillbot.db; першого разу перенести туди дані з JSON-файлів попередніх версій.
func (a *App) openStore() {
	a.fallbackFile = ""
	st, err := openStore(filepath.Join(a.baseDir, "phillbot.db"), "local")
	if err != nil {
		a.storeErr = "База даних недоступна (" + err.Error() + ") — стан зберігається у subday_state.json"
		a.fallbackFile = a.stateFile
		a.st = loadState(a.stateFile)
		return
	}
	a.store = st
	if old, ok, err := st.migrateFiles(a.baseDir); err != nil {
		a.storeErr = "Не вдалося перенести дані в БД: " + err.Error()
	} else if ok {
		a.st = old
		if err := st.Sync(&a.st); err != nil {
			a.storeErr = "Не вдалося записати перенесені дані: " + err.Error()
		}
	}
	loaded, ok, err := st.Load()
	if err != nil {
		a.storeErr = "Не вдалося прочитати БД: " + err.Error()
	}
	if ok {
		a.st = fixState(loaded)
	} else {
		a.st = fixState(defaultState())
	}
}

// ---------- авторизація ----------
func (a *App) authFlow(forceLogin bool) {
	a.mu.Lock()
	if a.authCancel != nil {
		a.authCancel()
	}
	if a.listenCancel != nil {
		a.listenCancel()
		a.listenCancel = nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.authCancel = cancel
	a.mu.Unlock()

	a.setAuth(AuthInfo{Status: "checking"})
	a.setConn("off", "Не підключено")
	var tok *Tokens
	if !forceLogin {
		tok = a.storedToken()
	}
	if tok == nil {
		if !forceLogin {
			a.setAuth(AuthInfo{Status: "need_login"})
			return
		}
		var err error
		tok, err = a.deviceLogin(ctx)
		if err != nil {
			if ctx.Err() == nil {
				a.setAuth(AuthInfo{Status: "error", Message: err.Error()})
			}
			return
		}
	}
	a.tokMu.Lock()
	a.token = tok
	a.tokMu.Unlock()

	u, err := a.fetchUser()
	if err != nil {
		a.setAuth(AuthInfo{Status: "error", Message: fmtErr("Не вдалося отримати дані каналу", err)})
		return
	}
	a.mu.Lock()
	a.bc = u
	a.auth = AuthInfo{Status: "ok", User: &u}
	lctx, lcancel := context.WithCancel(context.Background())
	a.listenCancel = lcancel
	a.emitLocked()
	a.mu.Unlock()
	a.logf("Вхід виконано: канал %s", u.DisplayName)
	go a.listen(lctx, u)
	go a.loadBadges(u.ID)
	go a.fillColors()
	a.RefreshRewards()
}

func (a *App) SetClientID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("Client ID порожній")
	}
	a.clientID = id
	writeJSON(a.configFile, Config{ClientID: id})
	go a.authFlow(true)
	return nil
}

func (a *App) Login() {
	if a.clientID == "" {
		a.setAuth(AuthInfo{Status: "no_client"})
		return
	}
	go a.authFlow(true)
}

func (a *App) CancelLogin() {
	a.mu.Lock()
	if a.authCancel != nil {
		a.authCancel()
	}
	a.mu.Unlock()
	a.setAuth(AuthInfo{Status: "need_login"})
}

func (a *App) ChangeClientID() {
	a.Logout()
	a.setAuth(AuthInfo{Status: "no_client"})
}

func (a *App) OpenURL(u string) {
	if strings.HasPrefix(u, "https://") {
		openURL(u)
	}
}
