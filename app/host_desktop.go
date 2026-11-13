package main

// desktopHost — Host для десктопної версії: події у вікно Wails і віджети OBS, токени у tokens.json.

import (
	"os"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type desktopHost struct{ a *App }

func (h desktopHost) Emit(name string, data any) {
	if h.a.ctx != nil {
		wruntime.EventsEmit(h.a.ctx, name, data)
	}
	if h.a.overlay != nil {
		h.a.overlay.hub.broadcast(name, data)
	}
}

func (h desktopHost) Notify() {
	if h.a.overlay != nil {
		h.a.overlay.notify()
	}
}

func (h desktopHost) Decorate(s *Snapshot) {
	s.BaseDir = h.a.baseDir
	if h.a.overlay != nil {
		s.Obs = h.a.overlay.info()
	}
	s.ChatWindow = h.a.chatWinClients > 0 || h.a.chatWinPending
}

func (h desktopHost) SaveTokens(t *Tokens) {
	if t == nil {
		_ = os.Remove(h.a.tokensFile)
		return
	}
	writeJSON(h.a.tokensFile, t)
}

func (h desktopHost) LoadTokens() *Tokens {
	var t Tokens
	if !readJSON(h.a.tokensFile, &t) {
		return nil
	}
	return &t
}

// зміна порту віджета — перезапуск локального сервера; токен/домен ngrok — перезапуск тунелю
func (h desktopHost) SettingsChanged(old, nu Settings) {
	a := h.a
	if a.overlay != nil && nu.ObsPort != old.ObsPort {
		a.overlay.stop()
		a.overlay.start(nu.ObsPort)
	}
	if nu.NgrokToken != old.NgrokToken || nu.NgrokDomain != old.NgrokDomain {
		a.restartTunnel()
	}
}

func (h desktopHost) KeepWheelImage(name string) { h.a.cleanWheelDir(name) }
