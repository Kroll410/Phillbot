//go:build server

package main

import (
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
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
	case "/api/history":
		writeJSONResp(w, rt.ch.viewerHistory())
	case "/api/events":
		// той самий потік, що й у віджетів OBS: кожен кадр — сигнал перечитати повний стан
		serveSSE(w, r, rt.host.ov, sseFrame("state", rt.ch.overlayState()))
	default:
		http.NotFound(w, r)
	}
}

// ---------- історія для глядачів: минулі збори СабДею й аукціони (Переглядовий) за датами ----------
// Лише назви ігор, бали й переможці — без логінів, id і сум донатів окремих глядачів.

type ViewerHistGroup struct {
	Name   string `json:"name"`
	Points int    `json:"points"`
	Count  int    `json:"count"`
}

type ViewerHistColl struct {
	At     int64             `json:"at"` // початок збору, мс
	N      int               `json:"n"`
	Winner string            `json:"winner,omitempty"`
	Orders int               `json:"orders"`
	Points int               `json:"points"`
	Top    []ViewerHistGroup `json:"top"`
}

type ViewerHistAuction struct {
	At     int64             `json:"at"`
	Winner string            `json:"winner,omitempty"`
	Sum    int               `json:"sum"`
	Bids   int               `json:"bids"`
	Lots   int               `json:"lots"`
	Top    []ViewerHistGroup `json:"top"` // лоти: name + points (сума ставок)
}

type ViewerHistory struct {
	Subday   []ViewerHistColl    `json:"subday"`
	Auctions []ViewerHistAuction `json:"auctions"`
}

const viewerHistMax = 100 // найновіших записів кожного виду

// дата з id стріму/аукціону: 2026-11-13_19-00-00
func idTime(id string) int64 {
	if len(id) >= 19 {
		if t, err := time.ParseInLocation("2006-01-02_15-04-05", id[:19], time.Local); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func (a *Channel) viewerHistory() ViewerHistory {
	out := ViewerHistory{Subday: []ViewerHistColl{}, Auctions: []ViewerHistAuction{}}
	for _, sc := range a.loadAllSubday() {
		c := sc.Coll
		if c.FinishedAt == 0 || len(c.Entries) == 0 { // поточний збір видно на вкладці СабДей
			continue
		}
		at := c.StartedAt
		if at == 0 {
			at = idTime(sc.Stream)
		}
		hc := ViewerHistColl{At: at, N: c.N, Winner: c.Winner, Top: []ViewerHistGroup{}}
		for _, g := range aggregate(c.Settings, c.Entries) {
			hc.Orders += g.Count
			hc.Points += g.Points
			if len(hc.Top) < 10 {
				hc.Top = append(hc.Top, ViewerHistGroup{g.Name, g.Points, g.Count})
			}
		}
		out.Subday = append(out.Subday, hc)
	}
	a.mu.Lock()
	curAu, curWinner := a.st.Auction.ID, a.st.Auction.Winner
	a.mu.Unlock()
	for _, r := range a.loadAllAuctions() {
		if r.ID == curAu && curWinner == "" { // поточний аукціон без переможця — на вкладці Переглядовий
			continue
		}
		ha := ViewerHistAuction{At: idTime(r.ID), Top: []ViewerHistGroup{}}
		lots := []Lot{}
		for _, l := range r.Lots {
			if l.Hidden || l.Amount <= 0 {
				continue
			}
			lots = append(lots, l)
			ha.Sum += l.Amount
			ha.Bids += l.Bids
			if l.ID == r.Winner {
				ha.Winner = l.Name
			}
		}
		if len(lots) == 0 {
			continue
		}
		ha.Lots = len(lots)
		sort.SliceStable(lots, func(i, j int) bool { return lots[i].Amount > lots[j].Amount })
		for i, l := range lots {
			if i == 10 {
				break
			}
			ha.Top = append(ha.Top, ViewerHistGroup{l.Name, l.Amount, l.Bids})
		}
		out.Auctions = append(out.Auctions, ha)
	}
	sort.SliceStable(out.Subday, func(i, j int) bool { return out.Subday[i].At > out.Subday[j].At })
	sort.SliceStable(out.Auctions, func(i, j int) bool { return out.Auctions[i].At > out.Auctions[j].At })
	if len(out.Subday) > viewerHistMax {
		out.Subday = out.Subday[:viewerHistMax]
	}
	if len(out.Auctions) > viewerHistMax {
		out.Auctions = out.Auctions[:viewerHistMax]
	}
	return out
}
