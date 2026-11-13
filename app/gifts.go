package main

// Подаровані сабки: +1 бал до замовлення СабДею за кожну сабку, яку глядач подарував за останні 30 днів.
// Два джерела, береться більше з двох:
//   1) список активних підписок каналу (Helix /subscriptions): у подарованих вказано, хто подарував.
//      Подарована сабка діє місяць, тож активні подаровані ≈ подаровані за ~30 днів — історія доступна одразу.
//   2) подія EventSub channel.subscription.gift — нові подарунки наживо, журнал за 30 днів у стані програми.
// Анонімні подарунки не рахуються (дарувальник невідомий).

import (
	"context"
	"net/url"
	"time"
)

const giftWindow = 30 * 24 * time.Hour

// GiftEvent — подарунок, отриманий наживо.
type GiftEvent struct {
	Ts       int64  `json:"ts"`
	GifterID string `json:"gifterId"`
	Login    string `json:"login"`
	Name     string `json:"name"`
	Count    int    `json:"count"`
}

// giftsForLocked — скільки сабок глядач подарував за 30 днів (викликати під a.mu).
func (a *Channel) giftsForLocked(userID string) int {
	if userID == "" {
		return 0
	}
	live, since := 0, time.Now().Add(-giftWindow).UnixMilli()
	for _, g := range a.st.Gifts {
		if g.GifterID == userID && g.Ts >= since {
			live += g.Count
		}
	}
	if snap := a.giftSnap[userID]; snap > live {
		return snap
	}
	return live
}

// оновити кількість подарунків у замовленнях поточного збору (викликати під a.mu)
func (a *Channel) refreshEntryGiftsLocked() bool {
	changed := false
	for i := range a.st.Entries {
		e := &a.st.Entries[i]
		if e.Donation || e.UserID == "" {
			continue
		}
		if g := a.giftsForLocked(e.UserID); g != e.Gifts {
			e.Gifts, changed = g, true
		}
	}
	return changed
}

// прибрати з журналу подарунки, старші за 30 днів (викликати під a.mu)
func (a *Channel) pruneGiftsLocked() {
	since := time.Now().Add(-giftWindow).UnixMilli()
	out := a.st.Gifts[:0]
	for _, g := range a.st.Gifts {
		if g.Ts >= since {
			out = append(out, g)
		}
	}
	a.st.Gifts = out
}

// подія EventSub channel.subscription.gift
type giftEvent struct {
	UserID      string `json:"user_id"`
	UserLogin   string `json:"user_login"`
	UserName    string `json:"user_name"`
	Total       int    `json:"total"`
	Tier        string `json:"tier"`
	IsAnonymous bool   `json:"is_anonymous"`
}

func (a *Channel) onGift(ev giftEvent) {
	if ev.IsAnonymous || ev.UserID == "" || ev.Total <= 0 {
		return
	}
	a.mutate(func(st *State) {
		st.Gifts = append(st.Gifts, GiftEvent{Ts: time.Now().UnixMilli(), GifterID: ev.UserID, Login: ev.UserLogin, Name: ev.UserName, Count: ev.Total})
		a.pruneGiftsLocked()
		a.refreshEntryGiftsLocked()
	})
	a.addLog(LogLine{Text: "Подарував(ла) сабок: " + itoa(ev.Total) + " — +" + itoa(ev.Total) + " до балів замовлення", User: ev.UserName, Login: ev.UserLogin})
}

// fetchGiftSnapshot — активні подаровані підписки каналу: дарувальник → кількість.
func (a *Channel) fetchGiftSnapshot(bcID string) (map[string]int, error) {
	out := map[string]int{}
	cursor := ""
	for page := 0; page < 100; page++ {
		var d struct {
			Data []struct {
				UserID   string `json:"user_id"`
				GifterID string `json:"gifter_id"`
				IsGift   bool   `json:"is_gift"`
			} `json:"data"`
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
		}
		q := "subscriptions?broadcaster_id=" + url.QueryEscape(bcID) + "&first=100"
		if cursor != "" {
			q += "&after=" + url.QueryEscape(cursor)
		}
		if err := a.helix("GET", q, nil, &d); err != nil {
			return nil, err
		}
		for _, s := range d.Data {
			if s.IsGift && s.GifterID != "" && s.UserID != bcID {
				out[s.GifterID]++
			}
		}
		if d.Pagination.Cursor == "" || len(d.Data) == 0 {
			break
		}
		cursor = d.Pagination.Cursor
	}
	return out, nil
}

func (a *Channel) applyGiftSnapshot(snap map[string]int) {
	a.mu.Lock()
	a.giftSnap = snap
	a.pruneGiftsLocked()
	changed := a.refreshEntryGiftsLocked()
	if changed {
		a.saveLocked()
		a.emitLocked()
	}
	a.mu.Unlock()
}

// giftLoop — список подарованих підписок одразу після підключення і далі раз на 10 хвилин.
func (a *Channel) giftLoop(ctx context.Context, bcID string) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		if snap, err := a.fetchGiftSnapshot(bcID); err == nil {
			a.applyGiftSnapshot(snap)
		} else {
			a.logf("Не вдалося отримати подаровані сабки: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}
