//go:build server

package main

import (
	"net/http"
	"path"
	"strings"
)

// Сторінка для глядачів: /v/<секрет> — поточний СабДей або аукціон каналу без входу.
// У посиланні лише випадковий секрет (не нік і не id каналу), тож чужі сторінки не знайти перебором ніків.
// Глядач бачить те саме, що й на стрімі: таймер, топ замовлень, лоти з шансами; без токенів і налаштувань.

type ViewerLot struct {
	Name   string  `json:"name"`
	Amount int     `json:"amount"`
	Bids   int     `json:"bids"`
	Chance float64 `json:"chance"`        // шанс перемогти, 0…1 (від сум лотів на колесі)
	Out    int     `json:"out,omitempty"` // вибув у N-му оберті
}

type ViewerAuction struct {
	Timer  Timer       `json:"timer"`
	Lots   []ViewerLot `json:"lots"`
	Winner string      `json:"winner,omitempty"` // назва лота-переможця
	Mode   string      `json:"mode"`             // elim — на вибування | instant — одразу результат
	Sum    int         `json:"sum"`
	Bids   int         `json:"bids"`
}

type ViewerState struct {
	OverlayState
	Au ViewerAuction `json:"au"`
}

func (a *Channel) viewerState() ViewerState {
	vs := ViewerState{OverlayState: a.overlayState()}
	a.mu.Lock()
	defer a.mu.Unlock()
	au := a.st.Auction
	vs.Au = ViewerAuction{Timer: au.Timer, Mode: au.Cfg.SpinMode, Lots: []ViewerLot{}}
	for _, l := range au.Lots {
		if l.Hidden || l.Amount <= 0 {
			continue
		}
		vs.Au.Sum += l.Amount
		vs.Au.Bids += l.Bids
		vs.Au.Lots = append(vs.Au.Lots, ViewerLot{Name: l.Name, Amount: l.Amount, Bids: l.Bids, Out: l.Out})
		if l.ID == au.Winner {
			vs.Au.Winner = l.Name
		}
	}
	for i := range vs.Au.Lots {
		if vs.Au.Sum > 0 {
			vs.Au.Lots[i].Chance = float64(vs.Au.Lots[i].Amount) / float64(vs.Au.Sum)
		}
	}
	return vs
}

// GET /v/<секрет> — сторінка; /v/<секрет>/api/state — стан; /v/<секрет>/api/events — сигнал «щось змінилося» (SSE)
func (s *webServer) serveViewer(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v/")
	sec, sub, _ := strings.Cut(rest, "/")
	rt := s.secretChannel("view_secret", sec)
	if rt == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex") // посилання не має потрапляти в пошук
	switch path.Clean("/" + sub) {
	case "/":
		serveEmbedded(w, r, webFS, "web/viewer.html")
	case "/api/state":
		writeJSONResp(w, rt.ch.viewerState())
	case "/api/events":
		// той самий потік, що й у віджетів OBS: кожен кадр — сигнал перечитати повний стан
		serveSSE(w, r, rt.host.ov, sseFrame("state", rt.ch.overlayState()))
	default:
		http.NotFound(w, r)
	}
}
