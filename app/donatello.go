package main

// Donatello (donatello.to) — донати як замовлення СабДею і ставки аукціону.
// Програма раз на кілька секунд питає GET https://donatello.to/api/v1/donates (заголовок X-Token).
//   СабДей (поки йде збір):        текст донату = назва замовлення, бали = ⌊грн / DonUAHPerPoint⌋ (за замовчуванням 30).
//   Аукціон (поки приймаються ставки): текст = назва лота, ставка = ⌊грн / DonUAHPer1000⌋ × 1000 (за замовчуванням 10 грн = 1000).
// При першому підключенні вже наявні донати позначаються як бачені — історія не зараховується.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Donate struct {
	PubID      string `json:"pubId"`
	ClientName string `json:"clientName"`
	Message    string `json:"message"`
	Amount     string `json:"amount"`
	Currency   string `json:"currency"`
	Goal       string `json:"goal"`
	CreatedAt  string `json:"createdAt"`
}

// Стан підключення до Donatello — для інтерфейсу (у Snapshot).
type DonStatus struct {
	State    string `json:"state"` // off | on | error
	Nickname string `json:"nickname,omitempty"`
	Error    string `json:"error,omitempty"`
}

var donatelloBase = "https://donatello.to/api/v1/"

func donatelloGet(token, path string, out any) error {
	c := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", donatelloBase+path, nil)
	req.Header.Set("X-Token", token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return errors.New("немає зв'язку з Donatello")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return errors.New("Donatello не прийняв токен — перевірте його на сторінці API в Donatello")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("Donatello відповів %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// парсинг суми донату: "100", "100.50", "100,5"
func donAmount(s string) float64 {
	f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
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

// ---------- опитування ----------

// restartDonatello — (пере)запустити опитування з поточним токеном (при старті й після збереження налаштувань).
func (a *App) restartDonatello() {
	a.mu.Lock()
	if a.donCancel != nil {
		a.donCancel()
		a.donCancel = nil
	}
	token := strings.TrimSpace(a.st.Settings.DonToken)
	if token == "" {
		a.don = DonStatus{State: "off"}
		a.emitLocked()
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.donCancel = cancel
	a.mu.Unlock()
	go a.donatelloLoop(ctx, token)
}

func (a *App) donatelloLoop(ctx context.Context, token string) {
	var me struct {
		Nickname string `json:"nickname"`
	}
	setStatus := func(s DonStatus) {
		a.mu.Lock()
		changed := a.don != s
		a.don = s
		if changed {
			a.emitLocked()
		}
		a.mu.Unlock()
	}
	if err := donatelloGet(token, "me", &me); err != nil {
		setStatus(DonStatus{State: "error", Error: err.Error()})
	} else {
		setStatus(DonStatus{State: "on", Nickname: me.Nickname})
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		var page struct {
			Content []Donate `json:"content"`
		}
		if err := donatelloGet(token, "donates?page=0&size=50", &page); err != nil {
			setStatus(DonStatus{State: "error", Nickname: me.Nickname, Error: err.Error()})
		} else {
			setStatus(DonStatus{State: "on", Nickname: me.Nickname})
			a.handleDonates(page.Content)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// handleDonates — нові донати (Donatello віддає найновіші першими) обробляються від старших до новіших.
func (a *App) handleDonates(list []Donate) {
	a.mu.Lock()
	seen := map[string]bool{}
	for _, id := range a.st.DonSeen {
		seen[id] = true
	}
	baseline := !a.st.DonInit // перше підключення: історію лише запам'ятовуємо
	var fresh []Donate
	for i := len(list) - 1; i >= 0; i-- {
		d := list[i]
		if d.PubID == "" || seen[d.PubID] {
			continue
		}
		a.st.DonSeen = append(a.st.DonSeen, d.PubID)
		if !baseline {
			fresh = append(fresh, d)
		}
	}
	if len(a.st.DonSeen) > 2000 {
		a.st.DonSeen = a.st.DonSeen[len(a.st.DonSeen)-1000:]
	}
	changed := baseline || len(fresh) > 0
	a.st.DonInit = true
	if changed {
		a.saveLocked()
	}
	a.mu.Unlock()
	for _, d := range fresh {
		a.onDonate(d)
	}
}

// onDonate — куди йде донат: аукціон (якщо приймає ставки), інакше СабДей (якщо йде збір), інакше — лише в журнал.
func (a *App) onDonate(d Donate) {
	uah := donAmount(d.Amount)
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
	cur := strings.ToUpper(strings.TrimSpace(d.Currency))
	label := fmt.Sprintf("Донат %s %s «%s»", strings.TrimSpace(d.Amount), cur, text)
	switch {
	case cur != "" && cur != "UAH":
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

// TestDonatello — перевірити токен (кнопка в налаштуваннях): повертає нікнейм у Donatello.
func (a *App) TestDonatello(token string) (string, error) {
	var me struct {
		Nickname string `json:"nickname"`
	}
	if strings.TrimSpace(token) == "" {
		return "", errors.New("вставте токен API з Donatello")
	}
	if err := donatelloGet(strings.TrimSpace(token), "me", &me); err != nil {
		return "", err
	}
	return me.Nickname, nil
}
