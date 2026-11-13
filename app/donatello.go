package main

// Donatello (donatello.to) — донати як замовлення СабДею і ставки аукціону, через колбеки.
// Donatello сам надсилає кожен донат: POST JSON на публічну адресу з заголовком X-Key (ключ з панелі Donatello → API → Колбеки).
// PhillBot працює на ПК, тому публічну адресу дає вбудований ngrok (ngrok.go): https://<домен ngrok>/donatello.
//   СабДей (поки йде збір):            текст донату = назва замовлення, бали = ⌊грн / DonUAHPerPoint⌋ (за замовчуванням 30).
//   Аукціон (поки приймаються ставки): текст = назва лота, ставка = ⌊грн / DonUAHPer1000⌋ × 1000 (за замовчуванням 10 грн = 1000).
// Donatello не опитується зовсім.

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Donate — тіло колбека Donatello.
type Donate struct {
	PubID          string `json:"pubId"`
	ClientName     string `json:"clientName"`
	Message        string `json:"message"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	ActualAmount   string `json:"actualAmount"`   // сума після конвертації (зазвичай у гривнях)
	ActualCurrency string `json:"actualCurrency"` // валюта actualAmount
	Source         string `json:"source"`
	Goal           string `json:"goal"`
	CreatedAt      string `json:"createdAt"`
}

// Стан приймача донатів — для інтерфейсу (у Snapshot).
type DonStatus struct {
	State string `json:"state"`         // off | starting | on | error
	URL   string `json:"url,omitempty"` // посилання для Donatello → Колбеки
	Error string `json:"error,omitempty"`
}

// парсинг суми донату: "100", "100.50", "100,5"
func donAmount(s string) float64 {
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// сума в гривнях: спершу actualAmount у гривнях (після конвертації), інакше amount, якщо донат у гривнях
func (d Donate) uah() (float64, bool) {
	if strings.EqualFold(strings.TrimSpace(d.ActualCurrency), "UAH") && strings.TrimSpace(d.ActualAmount) != "" {
		return donAmount(d.ActualAmount), true
	}
	if c := strings.TrimSpace(d.Currency); c == "" || strings.EqualFold(c, "UAH") {
		return donAmount(d.Amount), true
	}
	return 0, false
}

// бали СабДею з гривень: ⌊грн / ставка⌋
func subdayDonPoints(uah float64, perPoint int) int {
	if perPoint < 1 {
		perPoint = 30
	}
	return int(math.Floor(uah/float64(perPoint) + 1e-9))
}

// ставка аукціону з гривень: ⌊грн / ставка⌋ × 1000
func auctionDonPoints(uah float64, per1000 int) int {
	if per1000 < 1 {
		per1000 = 10
	}
	return int(math.Floor(uah/float64(per1000)+1e-9)) * 1000
}

// donatelloHandler — приймач колбеків: лише POST /donatello з правильним X-Key.
func (a *App) donatelloHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/donatello" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "use POST", http.StatusMethodNotAllowed)
			return
		}
		a.mu.Lock()
		key := strings.TrimSpace(a.st.Settings.DonKey)
		a.mu.Unlock()
		got := strings.TrimSpace(r.Header.Get("X-Key"))
		if key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(key)) != 1 {
			if key == "" {
				a.logf("Donatello: прийшов донат, але ключ колбека не вказано в налаштуваннях — не враховано")
			} else {
				a.logf("Donatello: запит з неправильним ключем X-Key — відхилено")
			}
			http.Error(w, "bad key", http.StatusForbidden)
			return
		}
		var d Donate
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&d); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		a.receiveDonate(d)
	})
}

// receiveDonate — повтор того самого донату (Donatello може надіслати ще раз) рахується один раз.
func (a *App) receiveDonate(d Donate) {
	if id := strings.TrimSpace(d.PubID); id != "" {
		a.mu.Lock()
		for _, s := range a.st.DonSeen {
			if s == id {
				a.mu.Unlock()
				return
			}
		}
		a.st.DonSeen = append(a.st.DonSeen, id)
		if len(a.st.DonSeen) > 2000 {
			a.st.DonSeen = a.st.DonSeen[len(a.st.DonSeen)-1000:]
		}
		a.saveLocked()
		a.mu.Unlock()
	}
	a.onDonate(d)
}

// onDonate — куди йде донат: аукціон (якщо приймає ставки), інакше СабДей (якщо йде збір), інакше — лише в журнал.
func (a *App) onDonate(d Donate) {
	uah, isUAH := d.uah()
	text := strings.TrimSpace(d.Message)
	name := strings.TrimSpace(d.ClientName)
	if name == "" {
		name = "Анонім"
	}
	a.mu.Lock()
	auctionOn := a.st.Auction.Timer.Status == "running"
	subdayOn := a.st.Session.Status == "running"
	per1000, perPoint := a.st.Auction.Cfg.DonUAHPer1000, a.st.Settings.DonUAHPerPoint
	a.mu.Unlock()
	label := fmt.Sprintf("Донат %s %s «%s»", strings.TrimSpace(d.Amount), strings.ToUpper(strings.TrimSpace(d.Currency)), text)
	switch {
	case !isUAH:
		a.addLog(LogLine{Text: label + " — не в гривнях, не враховано", User: name})
	case !auctionOn && !subdayOn:
		a.addLog(LogLine{Text: label + " — збір і аукціон не йдуть, не враховано", User: name})
	case text == "":
		a.addLog(LogLine{Text: label + " — без тексту, не враховано", User: name})
	case auctionOn:
		pts := auctionDonPoints(uah, per1000)
		if pts <= 0 {
			a.addLog(LogLine{Text: label + " — замало для ставки, не враховано", User: name})
			return
		}
		b := Bid{ID: newID(), Ts: time.Now().UnixMilli(), User: name, Login: "donatello:" + strings.ToLower(name), UserID: "don:" + d.PubID,
			Text: text, Amount: pts, Donation: true, UAH: uah}
		var placed Bid
		a.mutate(func(st *State) {
			placed = st.Auction.place(b)
			st.Auction.Feed = append([]Bid{placed}, st.Auction.Feed...)
			if len(st.Auction.Feed) > 300 {
				st.Auction.Feed = st.Auction.Feed[:300]
			}
		})
		a.emitEvent("auctionbid", placed.ID)
		a.addLog(LogLine{Text: fmt.Sprintf("%s → ставка %d", label, pts), User: name})
	default: // СабДей
		pts := subdayDonPoints(uah, perPoint)
		e := Entry{ID: newID(), Ts: time.Now().UnixMilli(), User: name, Login: "donatello", UserID: "don:" + d.PubID,
			Text: text, Source: "Donatello", Donation: true, DonPoints: pts, UAH: uah}
		a.mutate(func(st *State) { st.Entries = append(st.Entries, e) })
		a.emitEvent("redeem", map[string]any{"id": e.ID})
		a.addLog(LogLine{Text: fmt.Sprintf("%s → %d бал.", label, pts), User: name})
	}
}
