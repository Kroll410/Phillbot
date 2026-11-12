package main

// «Переглядовий» — аукціон.
// Глядачі погашають нагороди за бали з текстом (назва лота); сума ставки = ціна нагороди.
// Ставки приймаються, поки йде таймер (як збір у СабДеї).
// Однакові назви одразу додаються до лота; схожі — чекають рішення стрімера («Додати до X?» / «Ні»); нові — новий лот.
// Колесо: частка лота пропорційна сумі, один оберт — переможець (вибирає бекенд, інтерфейс лише анімує).

import (
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LotUser struct {
	Name   string      `json:"name"`
	Login  string      `json:"login"`
	Color  string      `json:"color,omitempty"`
	Badges []ChatBadge `json:"badges,omitempty"`
	Amount int         `json:"amount"`
}

type Lot struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Key    string    `json:"key"`
	Amount int       `json:"amount"`
	Bids   int       `json:"bids"`
	Users  []LotUser `json:"users"`
	Out    int       `json:"out,omitempty"` // вибув у N-му оберті (режим «на вибування»)
}

// Bid — ставка у стрічці праворуч.
type Bid struct {
	ID          string      `json:"id"`
	Ts          int64       `json:"ts"`
	User        string      `json:"user"`
	Login       string      `json:"login"`
	UserID      string      `json:"userId"`
	Color       string      `json:"color,omitempty"`
	Badges      []ChatBadge `json:"badges,omitempty"`
	Text        string      `json:"text"`
	Amount      int         `json:"amount"`
	Status      string      `json:"status"` // added | new | pending | rejected (стрімер сказав «ні» → новий лот)
	LotID       string      `json:"lotId,omitempty"`
	SuggestID   string      `json:"suggestId,omitempty"`
	SuggestName string      `json:"suggestName,omitempty"`
}

// Налаштування аукціону (окремо від СабДею): до 5 нагород-ставок, тривалість прийому ставок, режим колеса.
type AuctionSettings struct {
	RewardIDs   []string `json:"rewardIds"`
	RewardsSet  bool     `json:"rewardsSet"`  // стрімер сам вибрав нагороди — автовибір більше не втручається
	DurationMin int      `json:"durationMin"` // 10 хв за замовчуванням
	SpinMode    string   `json:"spinMode"`    // elim — на вибування (за замовчуванням) | instant — одразу результат
}

const maxAuctionRewards = 5

// нагороди за замовчуванням: Квиток (1000), Середній квиток (5000), Мегаквиток (10000)
var defaultAuctionRewards = []string{"квиток", "середній квиток", "мегаквиток"}

func isDefaultAuctionReward(title string) bool {
	t := strings.Join(strings.Fields(strings.ToLower(title)), " ")
	for _, d := range defaultAuctionRewards {
		if t == d {
			return true
		}
	}
	return false
}

// Spin — останній оберт колеса. Інтерфейс анімує саме цей набір секторів (стан лотів після оберту вже інший).
type Spin struct {
	At      int64     `json:"at"`
	Mode    string    `json:"mode"`
	IDs     []string  `json:"ids"`     // сектори по колу, по порядку
	Weights []float64 `json:"weights"` // розмір секторів (elim — 1/сума: дешевші вибувають частіше)
	Pick    string    `json:"pick"`    // на кого вказала стрілка
	Pos     float64   `json:"pos"`     // точка на колі 0..1 (від random.org)
	Source  string    `json:"source"`  // random.org | локально
	Final   bool      `json:"final"`   // після цього оберту відомий переможець
}

type AuctionState struct {
	Cfg     AuctionSettings `json:"cfg"`
	Timer   Timer           `json:"timer"`
	Lots    []Lot           `json:"lots"`
	Feed    []Bid           `json:"feed"`
	Winner  string          `json:"winner"` // id лота-переможця
	Spin    *Spin           `json:"spin,omitempty"`
	SeenIDs []string        `json:"seenIds"`
	Merges  []MergeRec      `json:"merges"` // історія об'єднань — для «Скасувати об'єднання»
}

// MergeRec — одне об'єднання лотів: що повернути, якщо стрімер натисне «Скасувати об'єднання».
type MergeRec struct {
	Src       Lot      `json:"src"`       // лот, що зник (як був до об'єднання)
	Index     int      `json:"index"`     // його місце в списку
	To        string   `json:"to"`        // лот, у який його злили
	FeedLot   []string `json:"feedLot"`   // ставки, що перейшли до To
	FeedSug   []string `json:"feedSug"`   // пропозиції «Додати до X?», що перейшли до To
	WasWinner bool     `json:"wasWinner"` // Src був переможцем
}

func (au *AuctionState) durMs() int64 { return int64(au.Cfg.DurationMin) * 60000 }

func (au *AuctionState) normalize() {
	if au.Lots == nil {
		au.Lots = []Lot{}
	}
	if au.Feed == nil {
		au.Feed = []Bid{}
	}
	if au.Cfg.RewardIDs == nil {
		au.Cfg.RewardIDs = []string{}
	}
	if len(au.Cfg.RewardIDs) > maxAuctionRewards {
		au.Cfg.RewardIDs = au.Cfg.RewardIDs[:maxAuctionRewards]
	}
	if au.Cfg.DurationMin < 1 || au.Cfg.DurationMin > 180 {
		au.Cfg.DurationMin = 10
	}
	if au.Cfg.SpinMode != "instant" {
		au.Cfg.SpinMode = "elim"
	}
	switch au.Timer.Status {
	case "running", "paused", "finished":
	default:
		au.Timer = Timer{Status: "idle"}
	}
	if au.Timer.Status == "running" && au.Timer.EndsAt <= time.Now().UnixMilli() {
		au.Timer.Status, au.Timer.RemainingMs, au.Timer.FinishedAt = "finished", 0, au.Timer.EndsAt
	}
	if au.Timer.Status == "idle" {
		au.Timer.RemainingMs = au.durMs()
	}
}

// ---------- схожість назв ----------

func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// 0..1: наскільки схожі дві нормалізовані назви (без пробілів і розділових знаків)
func similarity(a, b string) float64 {
	clean := func(s string) []rune {
		var r []rune
		for _, c := range s {
			if c != ' ' && c != '-' && c != '_' && c != '.' && c != ':' && c != '\'' && c != '’' {
				r = append(r, c)
			}
		}
		return r
	}
	ra, rb := clean(a), clean(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	if string(ra) == string(rb) {
		return 1
	}
	// одна назва містить іншу (напр. «elden ring» і «elden ring dlc»)
	if len(ra) >= 4 && len(rb) >= 4 && (strings.Contains(string(ra), string(rb)) || strings.Contains(string(rb), string(ra))) {
		return 0.85
	}
	d := levenshtein(ra, rb)
	l := len(ra)
	if len(rb) > l {
		l = len(rb)
	}
	return 1 - float64(d)/float64(l)
}

const similarThreshold = 0.72

func (au *AuctionState) lotIndex(id string) int {
	for i := range au.Lots {
		if au.Lots[i].ID == id {
			return i
		}
	}
	return -1
}

func (au *AuctionState) addToLot(i int, b Bid) {
	l := &au.Lots[i]
	l.Amount += b.Amount
	l.Bids++
	for k := range l.Users {
		if strings.EqualFold(l.Users[k].Login, b.Login) {
			l.Users[k].Amount += b.Amount
			if b.Color != "" {
				l.Users[k].Color = b.Color
			}
			if len(b.Badges) > 0 {
				l.Users[k].Badges = b.Badges
			}
			return
		}
	}
	l.Users = append(l.Users, LotUser{Name: b.User, Login: b.Login, Color: b.Color, Badges: b.Badges, Amount: b.Amount})
}

func (au *AuctionState) newLot(b Bid) string {
	l := Lot{ID: newID(), Name: strings.TrimSpace(b.Text), Key: normKey(b.Text)}
	au.Lots = append(au.Lots, l)
	au.addToLot(len(au.Lots)-1, b)
	return l.ID
}

// Розподіл ставки: однакова назва → додати; схожа → питання стрімеру; інакше → новий лот.
func (au *AuctionState) place(b Bid) Bid {
	key := normKey(b.Text)
	for i := range au.Lots {
		if au.Lots[i].Key == key {
			au.addToLot(i, b)
			b.Status, b.LotID = "added", au.Lots[i].ID
			return b
		}
	}
	best, bestSim := -1, 0.0
	for i := range au.Lots {
		if s := similarity(key, au.Lots[i].Key); s > bestSim {
			best, bestSim = i, s
		}
	}
	if best >= 0 && bestSim >= similarThreshold {
		b.Status, b.SuggestID, b.SuggestName = "pending", au.Lots[best].ID, au.Lots[best].Name
		return b
	}
	b.Status, b.LotID = "new", au.newLot(b)
	return b
}

// ---------- ставки з Twitch ----------

// Нагорода — ставка аукціону? Поки стрімер нічого не вибрав, нагороди «Квиток / Середній квиток / Мегаквиток» підхоплюються самі.
func (a *App) isAuctionReward(id, title string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := &a.st.Auction.Cfg
	for _, r := range c.RewardIDs {
		if r == id {
			return true
		}
	}
	if !c.RewardsSet && len(c.RewardIDs) < maxAuctionRewards && isDefaultAuctionReward(title) && id != a.st.Settings.RewardID {
		c.RewardIDs = append(c.RewardIDs, id)
		a.saveLocked()
		return true
	}
	return false
}

// автовибір нагород за замовчуванням зі списку нагород каналу (викликати під a.mu)
func (a *App) autoAuctionRewardsLocked(list []Reward) {
	c := &a.st.Auction.Cfg
	if c.RewardsSet || len(c.RewardIDs) > 0 {
		return
	}
	for _, d := range defaultAuctionRewards {
		for _, r := range list {
			if len(c.RewardIDs) < maxAuctionRewards && isDefaultAuctionReward(r.Title) && strings.Join(strings.Fields(strings.ToLower(r.Title)), " ") == d {
				c.RewardIDs = append(c.RewardIDs, r.ID)
			}
		}
	}
	if len(c.RewardIDs) > 0 {
		a.saveLocked()
	}
}

func (a *App) onAuctionBid(ev Redemption) {
	text := strings.TrimSpace(ev.UserInput)
	a.mu.Lock()
	au := &a.st.Auction
	running := au.Timer.Status == "running"
	dup := false
	for _, id := range au.SeenIDs {
		if id == ev.ID && ev.ID != "" {
			dup = true
			break
		}
	}
	a.mu.Unlock()
	if !running {
		a.logUser(ev.UserID, ev.UserName, ev.UserLogin, ev.Reward.ID, "Ставка «%s» — аукціон не йде, не враховано", text)
		return
	}
	if dup || text == "" {
		return
	}
	// вигляд ніку — з останнього повідомлення в чаті (як у СабДеї)
	var color string
	var badges []ChatBadge
	a.badgeMu.Lock()
	if c, ok := a.badgeCache[ev.UserID+"|"+ev.Reward.ID]; ok {
		color, badges = c.color, c.badges
	} else if c, ok := a.badgeCache[ev.UserID]; ok {
		color, badges = c.color, c.badges
	}
	a.badgeMu.Unlock()
	login := ev.UserLogin
	if login == "" {
		login = strings.ToLower(ev.UserName)
	}
	b := Bid{ID: newID(), Ts: time.Now().UnixMilli(), User: ev.UserName, Login: login, UserID: ev.UserID,
		Color: color, Badges: badges, Text: text, Amount: ev.Reward.Cost}
	var placed Bid
	a.mutate(func(st *State) {
		au := &st.Auction
		if ev.ID != "" {
			au.SeenIDs = append(au.SeenIDs, ev.ID)
			if len(au.SeenIDs) > 3000 {
				au.SeenIDs = au.SeenIDs[1000:]
			}
		}
		placed = au.place(b)
		au.Feed = append([]Bid{placed}, au.Feed...)
		if len(au.Feed) > 300 {
			au.Feed = au.Feed[:300]
		}
	})
	a.emitEvent("auctionbid", placed.ID)
	a.logUser(ev.UserID, ev.UserName, login, ev.Reward.ID, "Ставка %d на «%s»", b.Amount, text)
}

// ---------- методи для інтерфейсу ----------

// Налаштування аукціону: нагороди (до 5), тривалість прийому ставок, режим колеса.
func (a *App) AuctionSaveSettings(c AuctionSettings) Snapshot {
	return a.mutate(func(st *State) {
		out := []string{}
		seen := map[string]bool{}
		for _, id := range c.RewardIDs {
			if id = strings.TrimSpace(id); id != "" && !seen[id] && len(out) < maxAuctionRewards {
				seen[id] = true
				out = append(out, id)
			}
		}
		c.RewardIDs, c.RewardsSet = out, true
		st.Auction.Cfg = c
		st.Auction.normalize()
	})
}

func (a *App) AuctionStart() Snapshot {
	return a.mutate(func(st *State) {
		au := &st.Auction
		t, now := &au.Timer, time.Now().UnixMilli()
		switch t.Status {
		case "running":
		case "paused":
			t.EndsAt, t.Status = now+t.RemainingMs, "running"
		default:
			*t = Timer{Status: "running", StartedAt: now, EndsAt: now + au.durMs()}
		}
	})
}

func (a *App) AuctionPause() Snapshot {
	return a.mutate(func(st *State) {
		t := &st.Auction.Timer
		if t.Status == "running" {
			t.RemainingMs, t.Status = max64(0, t.EndsAt-time.Now().UnixMilli()), "paused"
		}
	})
}

func (a *App) AuctionFinish() Snapshot {
	return a.mutate(func(st *State) {
		t := &st.Auction.Timer
		if t.Status == "running" || t.Status == "paused" {
			t.Status, t.RemainingMs, t.FinishedAt = "finished", 0, time.Now().UnixMilli()
			a.saveAuctionLocked()
		}
	})
}

func (a *App) AuctionAddTime(ms int64) Snapshot {
	return a.mutate(func(st *State) {
		t, now := &st.Auction.Timer, time.Now().UnixMilli()
		switch t.Status {
		case "running":
			t.EndsAt = max64(now+1000, t.EndsAt+ms)
		case "paused":
			t.RemainingMs = max64(1000, t.RemainingMs+ms)
		}
	})
}

// Відповідь стрімера на «Додати до X?»: accept=true — додати до запропонованого лота, false — новий лот.
func (a *App) AuctionResolve(bidID string, accept bool) (Snapshot, error) {
	var err error
	snap := a.mutate(func(st *State) {
		au := &st.Auction
		for i := range au.Feed {
			b := &au.Feed[i]
			if b.ID != bidID {
				continue
			}
			if b.Status != "pending" {
				err = errors.New("цю ставку вже розподілено")
				return
			}
			if j := au.lotIndex(b.SuggestID); accept && j >= 0 {
				au.addToLot(j, *b)
				b.Status, b.LotID = "added", b.SuggestID
			} else {
				b.Status, b.LotID = "rejected", au.newLot(*b)
			}
			return
		}
		err = errors.New("ставку не знайдено")
	})
	return snap, err
}

// Об'єднати лот from у лот to (перетягування, як у СабДеї).
func (a *App) AuctionMergeLots(from, to string) Snapshot {
	return a.mutate(func(st *State) {
		au := &st.Auction
		i, j := au.lotIndex(from), au.lotIndex(to)
		if i < 0 || j < 0 || i == j {
			return
		}
		src := au.Lots[i]
		rec := MergeRec{Src: src, Index: i, To: to, WasWinner: au.Winner == from}
		src.Users = append([]LotUser(nil), src.Users...)
		rec.Src.Users = src.Users
		for _, u := range src.Users {
			au.addToLot(j, Bid{User: u.Name, Login: u.Login, Color: u.Color, Badges: u.Badges, Amount: u.Amount})
		}
		au.Lots[j].Bids += src.Bids - len(src.Users) // addToLot рахує по одній на користувача
		for k := range au.Feed {
			if au.Feed[k].LotID == from {
				au.Feed[k].LotID = to
				rec.FeedLot = append(rec.FeedLot, au.Feed[k].ID)
			}
			if au.Feed[k].SuggestID == from { // пропозиція «Додати до X?» тепер про об'єднаний лот
				au.Feed[k].SuggestID, au.Feed[k].SuggestName = to, au.Lots[j].Name
				rec.FeedSug = append(rec.FeedSug, au.Feed[k].ID)
			}
		}
		if au.Winner == from {
			au.Winner = to
		}
		au.Lots = append(au.Lots[:i], au.Lots[i+1:]...)
		au.Merges = append(au.Merges, rec)
		if len(au.Merges) > 50 {
			au.Merges = au.Merges[len(au.Merges)-50:]
		}
	})
}

// AuctionUndoMerge — скасувати останнє об'єднання: лот повертається на своє місце з усіма ставками,
// з цільового лота віднімається його внесок (ставки, що прийшли після об'єднання, лишаються).
func (a *App) AuctionUndoMerge() (Snapshot, error) {
	var err error
	snap := a.mutate(func(st *State) {
		au := &st.Auction
		for len(au.Merges) > 0 {
			rec := au.Merges[len(au.Merges)-1]
			au.Merges = au.Merges[:len(au.Merges)-1]
			j := au.lotIndex(rec.To)
			if j < 0 { // цільовий лот видалили — це об'єднання вже не скасувати
				continue
			}
			t := &au.Lots[j]
			t.Amount -= rec.Src.Amount
			t.Bids -= rec.Src.Bids
			if t.Amount < 0 {
				t.Amount = 0
			}
			if t.Bids < 1 {
				t.Bids = 1
			}
			for _, u := range rec.Src.Users {
				for k := range t.Users {
					if strings.EqualFold(t.Users[k].Login, u.Login) {
						t.Users[k].Amount -= u.Amount
						if t.Users[k].Amount <= 0 {
							t.Users = append(t.Users[:k], t.Users[k+1:]...)
						}
						break
					}
				}
			}
			idx := rec.Index
			if idx > len(au.Lots) {
				idx = len(au.Lots)
			}
			au.Lots = append(au.Lots[:idx], append([]Lot{rec.Src}, au.Lots[idx:]...)...)
			ids := map[string]bool{}
			for _, id := range rec.FeedLot {
				ids[id] = true
			}
			sug := map[string]bool{}
			for _, id := range rec.FeedSug {
				sug[id] = true
			}
			for k := range au.Feed {
				if ids[au.Feed[k].ID] && au.Feed[k].LotID == rec.To {
					au.Feed[k].LotID = rec.Src.ID
				}
				if sug[au.Feed[k].ID] && au.Feed[k].SuggestID == rec.To {
					au.Feed[k].SuggestID, au.Feed[k].SuggestName = rec.Src.ID, rec.Src.Name
				}
			}
			if rec.WasWinner && au.Winner == rec.To {
				au.Winner = rec.Src.ID
			}
			return
		}
		err = errors.New("немає об'єднань, які можна скасувати")
	})
	return snap, err
}

func (a *App) AuctionRemoveLot(id string) Snapshot {
	return a.mutate(func(st *State) {
		au := &st.Auction
		if i := au.lotIndex(id); i >= 0 {
			au.Lots = append(au.Lots[:i], au.Lots[i+1:]...)
		}
	})
}

// randomUnit — випадкове число 0..1 з random.org (справжня випадковість, як у звичних аукціонах),
// якщо сервіс недоступний — криптографічний генератор Windows. Повертає також джерело.
var randomUnit = func() (float64, string) {
	const n = 1000000000
	c := &http.Client{Timeout: 6 * time.Second}
	req, _ := http.NewRequest("GET", "https://www.random.org/integers/?num=1&min=0&max=999999999&col=1&base=10&format=plain&rnd=new", nil)
	req.Header.Set("User-Agent", "PhillBot auction wheel")
	if resp, err := c.Do(req); err == nil {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		if v, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && resp.StatusCode == 200 && v >= 0 && v < n {
			return float64(v) / n, "random.org"
		}
	}
	var buf [8]byte
	_, _ = crand.Read(buf[:])
	return float64(binary.BigEndian.Uint64(buf[:])>>11) / float64(1<<53), "локально"
}

// AuctionSpin — один оберт. Бекенд вибирає результат (random.org), інтерфейс лише анімує колесо до нього.
//
//	instant — частка сектора = сума лота; на кого вказала стрілка, той і переміг.
//	elim    — частка сектора ∝ 1/сума (дешевші вибувають частіше); вибраний лот вибуває, останній — переможець.
func (a *App) AuctionSpin() (Snapshot, error) {
	a.mu.Lock()
	au := &a.st.Auction
	if au.Winner != "" {
		a.mu.Unlock()
		return a.GetState(), errors.New("переможця вже визначено — почніть новий аукціон або поверніть лоти")
	}
	mode := au.Cfg.SpinMode
	var ids []string
	var w []float64
	total := 0.0
	for _, l := range au.Lots {
		if l.Out > 0 || l.Amount <= 0 {
			continue
		}
		x := float64(l.Amount)
		if mode == "elim" {
			x = 1 / x
		}
		ids, w, total = append(ids, l.ID), append(w, x), total+x
	}
	a.mu.Unlock()
	if len(ids) == 0 {
		return a.GetState(), errors.New("на колесі ще немає лотів")
	}
	pos, src := 0.0, "—"
	pick := ids[0]
	if len(ids) > 1 {
		pos, src = randomUnit()
		r := pos * total
		for k, x := range w {
			pick = ids[k]
			if r < x {
				break
			}
			r -= x
		}
	}
	snap := a.mutate(func(st *State) {
		au := &st.Auction
		sp := &Spin{At: time.Now().UnixMilli(), Mode: mode, IDs: ids, Weights: w, Pick: pick, Pos: pos, Source: src}
		if mode == "instant" || len(ids) <= 1 {
			au.Winner, sp.Final = pick, true
		} else {
			round := 1
			for _, l := range au.Lots {
				if l.Out >= round {
					round = l.Out + 1
				}
			}
			if i := au.lotIndex(pick); i >= 0 {
				au.Lots[i].Out = round
			}
			if len(ids) == 2 { // лишився один — він і переміг
				for _, id := range ids {
					if id != pick {
						au.Winner = id
					}
				}
				sp.Final = true
			}
		}
		au.Spin = sp
		if au.Winner != "" {
			a.saveAuctionLocked()
		}
	})
	return snap, nil
}

// Повернути всі лоти на колесо (скасувати вибування і переможця).
func (a *App) AuctionResetSpins() Snapshot {
	return a.mutate(func(st *State) {
		au := &st.Auction
		for i := range au.Lots {
			au.Lots[i].Out = 0
		}
		au.Winner, au.Spin = "", nil
	})
}

// Новий аукціон: поточний зберігається в history/, лоти й стрічка очищуються (налаштування лишаються).
func (a *App) AuctionNew() Snapshot {
	return a.mutate(func(st *State) {
		a.saveAuctionLocked()
		st.Auction = AuctionState{Cfg: st.Auction.Cfg} // історія об'єднань теж очищується
		st.Auction.normalize()
	})
}

// history/auction_<дата>.json — копія аукціону (лоти, ставки, переможець). Викликати під a.mu.
func (a *App) saveAuctionLocked() {
	au := a.st.Auction
	if len(au.Lots) == 0 {
		return
	}
	if err := os.MkdirAll(a.historyDir, 0755); err != nil {
		return
	}
	start := au.Timer.StartedAt
	if start == 0 {
		start = time.Now().UnixMilli()
	}
	lots := append([]Lot(nil), au.Lots...)
	sort.SliceStable(lots, func(i, j int) bool { return lots[i].Amount > lots[j].Amount })
	b, _ := json.MarshalIndent(map[string]any{"savedAt": time.Now().Format(time.RFC3339), "lots": lots, "feed": au.Feed, "winner": au.Winner}, "", "  ")
	_ = writeFileAtomic(filepath.Join(a.historyDir, "auction_"+time.UnixMilli(start).Format("2006-01-02_15-04-05")+".json"), b)
}

// таймер прийому ставок (з ticker під a.mu)
func (a *App) tickAuctionLocked(now int64) bool {
	t := &a.st.Auction.Timer
	if t.Status == "running" && t.EndsAt <= now {
		t.Status, t.RemainingMs, t.FinishedAt = "finished", 0, now
		a.saveAuctionLocked()
		return true
	}
	return false
}
