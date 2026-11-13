package main

// Локальний HTTP-сервер для OBS (Browser Source): сторінки оверлеїв + SSE з оновленнями.
// Слухає лише 127.0.0.1 і перевіряє заголовок Host (захист від DNS-rebinding). Токенів і шляхів назовні не віддає.

import (
	"embed"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

//go:embed overlay
var overlayFS embed.FS

const defaultObsPort = 17841

type OverlayInfo struct {
	Running bool   `json:"running"`
	Port    int    `json:"port"`
	URL     string `json:"url"`
	Error   string `json:"error,omitempty"`
}

type LastOrder struct {
	User   string `json:"user"`
	Text   string `json:"text"`
	Points int    `json:"points"`
	Tier   int    `json:"tier"`
	Months int    `json:"months"`
	Ts     int64  `json:"ts"`
}

type OverlayTotals struct {
	Entries int `json:"entries"`
	Users   int `json:"users"`
	Orders  int `json:"orders"`
	Points  int `json:"points"`
	Marbles int `json:"marbles"`
}

type OverlayState struct {
	Session     Timer         `json:"session"`
	ServerNow   int64         `json:"serverNow"`
	RewardTitle string        `json:"rewardTitle"`
	Channel     string        `json:"channel"`
	Totals      OverlayTotals `json:"totals"`
	Top         []Group       `json:"top"`
	Last        *LastOrder    `json:"last"`
	Phase       string        `json:"phase"`  // collect | play
	Game        Timer         `json:"game"`   // таймер гри переможця
	Winner      string        `json:"winner"` // переможне замовлення
	CollN       int           `json:"collN"`
	Round       int           `json:"round"`
	Vote        OverlayVote   `json:"vote"`
	Auction     Timer         `json:"auction"` // таймер прийому ставок аукціону
}

type OverlayVote struct {
	Status     string `json:"status"`
	EndsAt     int64  `json:"endsAt"`
	FinishedAt int64  `json:"finishedAt"`
	Yes        int    `json:"yes"`
	No         int    `json:"no"`
	Result     string `json:"result"`
}

// ---------- SSE ----------
type sseHub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func (h *sseHub) add() chan []byte {
	c := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *sseHub) remove(c chan []byte) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *sseHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func sseFrame(name string, data any) []byte {
	b, _ := json.Marshal(data)
	return []byte("event: " + name + "\ndata: " + string(b) + "\n\n")
}

func (h *sseHub) broadcast(name string, data any) {
	if h.count() == 0 {
		return
	}
	frame := sseFrame(name, data)
	h.mu.Lock()
	for c := range h.clients {
		select {
		case c <- frame:
		default: // повільний клієнт — пропускаємо кадр
		}
	}
	h.mu.Unlock()
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// ---------- публічний зріз стану ----------
func (a *Channel) overlayState() OverlayState {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.st
	groups := aggregate(st.Settings, st.Entries)
	counted := countedIDs(st.Settings, st.Entries)
	users := map[string]bool{}
	n := 0
	for _, e := range st.Entries {
		if counted[e.ID] {
			n++
			users[userKey(e)] = true
		}
	}
	counts, total := marbleCounts(groups)
	var top []Group // у топі й лічильнику — лише замовлення з балами > 0 (ті, що стануть кулями)
	for _, g := range groups {
		if g.Points > 0 {
			top = append(top, g)
		}
	}
	if len(top) > 50 {
		top = top[:50]
	}
	out := OverlayState{Session: st.Session, ServerNow: time.Now().UnixMilli(), RewardTitle: st.Settings.RewardTitle,
		Channel: a.bc.DisplayName, Top: append([]Group(nil), top...), Phase: st.Phase, Game: st.Game, Winner: st.Winner, CollN: st.CollN, Round: st.Round, Auction: st.Auction.Timer,
		Vote:   OverlayVote{Status: st.Vote.Status, EndsAt: st.Vote.EndsAt, FinishedAt: st.Vote.FinishedAt, Yes: st.Vote.Yes, No: st.Vote.No, Result: st.Vote.Result},
		Totals: OverlayTotals{Entries: n, Users: len(users), Orders: len(counts), Points: total, Marbles: sumInts(counts)}}
	for i := range out.Top {
		if len(out.Top[i].Users) > 10 {
			out.Top[i].Users = out.Top[i].Users[:10]
		}
	}
	if a.lastOrd != nil {
		lo := *a.lastOrd
		out.Last = &lo
	}
	return out
}
