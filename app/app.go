package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type AuthInfo struct {
	Status  string `json:"status"` // no_client | checking | need_login | pending | ok | error
	Code    string `json:"code,omitempty"`
	URI     string `json:"uri,omitempty"`
	Message string `json:"message,omitempty"`
	User    *User  `json:"user,omitempty"`
}

type ConnInfo struct {
	Status string `json:"status"` // off | wait | on | error
	Text   string `json:"text"`
}

// Snapshot — усе, що потрібно інтерфейсу.
type Snapshot struct {
	State
	Auth       AuthInfo          `json:"auth"`
	Conn       ConnInfo          `json:"conn"`
	Rewards    []Reward          `json:"rewards"`
	RewardsErr string            `json:"rewardsErr"`
	BaseDir    string            `json:"baseDir"`
	ClientID   string            `json:"clientId"`
	Now        int64             `json:"now"`
	Log        []LogLine         `json:"log"`
	Obs        OverlayInfo       `json:"obs"`
	ChatWindow bool              `json:"chatWindow"`
	Don        DonStatus         `json:"don"`       // підключення до Donatello
	Version    string            `json:"version"`   // dev — тестова збірка; у релізі — тег (v1.1.0)
	SubBadges  map[string]string `json:"subBadges"` // значки підписки каналу: версія ("0", "3", "2012", "3036"…) → картинка // чат відкрито в окремому вікні — вкладка заблокована
}

// Host — те, що дає каналу оболонка: десктоп (Wails + локальні файли) або вебсервер.
type Host interface {
	Emit(event string, data any) // подія для інтерфейсу і віджетів OBS (чат, ставка…)
	EmitState(s Snapshot)        // повний стан — лише інтерфейсу стрімера (у ньому налаштування й ключі; віджетам — ні)
	Notify()                     // віджетам: стан каналу змінився (вони отримують свій overlayState)
	Decorate(s *Snapshot)        // доповнити знімок даними оболонки (OBS, тека, вікно чату); під c.mu
	SaveTokens(t *Tokens)        // зберегти токени Twitch каналу (nil — видалити)
	LoadTokens() *Tokens         // nil — токенів немає
	SettingsChanged(old, nu Settings)
	KeepWheelImage(name string) // картинка центру колеса змінилась — інші можна прибрати
}

// Channel — один канал Twitch: стан, логіка СабДею й аукціону, підключення до Twitch, чат.
// Не знає, де працює: усе про вікно, файли й мережу назовні — через Host.
type Channel struct {
	host Host

	store    *Store // внутрішня БД; nil — БД недоступна, стан пишеться у fallbackFile
	storeErr string
	// запасний файл стану, якщо БД не відкрилась (десктоп: subday_state.json)
	fallbackFile string

	mu       sync.Mutex
	st       State
	auth     AuthInfo
	conn     ConnInfo
	rewards  []Reward
	rewErr   string
	logLines []LogLine
	bc       User

	clientID     string
	clientSecret string // лише вебверсія (Confidential-клієнт Twitch)
	tokMu        sync.Mutex
	token        *Tokens

	giftSnap   map[string]int // активні подаровані підписки: дарувальник → кількість (Helix)
	don        DonStatus      // підключення до Donatello
	badgeMu    sync.Mutex
	badgeCache map[string]subInfo
	badgeImgs  map[string]badgeImg

	chattersMu sync.Mutex
	chatters   map[string]*Chatter
	chattersCh chan struct{}

	chatMu sync.Mutex
	chat   []ChatMsg

	lastOrd *LastOrder

	authCancel   context.CancelFunc
	listenCancel context.CancelFunc
}

// nopHost — канал без оболонки (тести, фонові задачі).
type nopHost struct{}

func (nopHost) Emit(string, any) {}

func (nopHost) EmitState(Snapshot) {}

func (nopHost) Notify() {}

func (nopHost) Decorate(*Snapshot) {}

func (nopHost) SaveTokens(*Tokens) {}

func (nopHost) LoadTokens() *Tokens { return nil }

func (nopHost) SettingsChanged(_, _ Settings) {}

func (nopHost) KeepWheelImage(string) {}

func newChannel(host Host) *Channel {
	if host == nil {
		host = nopHost{}
	}
	c := &Channel{host: host, badgeCache: map[string]subInfo{}, badgeImgs: map[string]badgeImg{},
		chatters: map[string]*Chatter{}, chattersCh: make(chan struct{}, 1), st: fixState(defaultState())}
	c.conn = ConnInfo{Status: "off", Text: "Не підключено"}
	go c.chattersLoop()
	return c
}

// ---------- внутрішнє ----------
func (a *Channel) snapshotLocked() Snapshot {
	b, _ := json.Marshal(a.st) // глибока копія
	var st State
	_ = json.Unmarshal(b, &st)
	var user *User
	if a.auth.User != nil {
		u := *a.auth.User
		user = &u
	}
	auth := a.auth
	auth.User = user
	snap := Snapshot{State: st, Auth: auth, Conn: a.conn, Rewards: append([]Reward(nil), a.rewards...), RewardsErr: a.rewErr,
		ClientID: a.clientID, Now: time.Now().UnixMilli(), Log: append([]LogLine(nil), a.logLines...),
		SubBadges: a.subBadges(), Don: a.don, Version: version}
	a.host.Decorate(&snap)
	return snap
}

// подія для головного вікна і для оверлеїв OBS
func (a *Channel) emitEvent(name string, data any) { a.host.Emit(name, data) }

func (a *Channel) saveLocked() {
	if a.store != nil {
		if err := a.store.Sync(&a.st); err != nil {
			go a.logf("Не вдалося зберегти в БД: %v", err)
		}
		return
	}
	if a.fallbackFile == "" {
		return
	}
	b, err := json.MarshalIndent(a.st, "", "  ")
	if err == nil {
		_ = writeFileAtomic(a.fallbackFile, b)
	}
}

func (a *Channel) emitLocked() {
	a.host.EmitState(a.snapshotLocked())
	a.host.Notify()
}

// змінити стан, зберегти і повідомити інтерфейс
func (a *Channel) mutate(fn func(st *State)) Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(&a.st)
	a.saveLocked()
	a.emitLocked()
	return a.snapshotLocked()
}

func (a *Channel) setAuth(ai AuthInfo) {
	a.mu.Lock()
	if ai.User == nil && ai.Status == "ok" {
		ai.User = a.auth.User
	}
	a.auth = ai
	a.emitLocked()
	a.mu.Unlock()
}

func (a *Channel) setConn(status, text string) {
	a.mu.Lock()
	a.conn = ConnInfo{Status: status, Text: text}
	a.emitLocked()
	a.mu.Unlock()
}

// Рядок журналу. Для подій від глядача — нік як у чаті Twitch (колір і бейджі з його повідомлення).
type LogLine struct {
	Ts     int64       `json:"ts"`
	Text   string      `json:"text"`
	User   string      `json:"user,omitempty"`
	Login  string      `json:"login,omitempty"`
	Color  string      `json:"color,omitempty"`
	Badges []ChatBadge `json:"badges,omitempty"`
}

func (a *Channel) addLog(l LogLine) {
	l.Ts = time.Now().UnixMilli()
	a.mu.Lock()
	a.logLines = append(a.logLines, l)
	if len(a.logLines) > 200 {
		a.logLines = a.logLines[len(a.logLines)-200:]
	}
	a.emitLocked()
	a.mu.Unlock()
}

func (a *Channel) logf(format string, args ...any) {
	a.addLog(LogLine{Text: fmt.Sprintf(format, args...)})
}

// подія від глядача: колір і бейджі беремо з останнього його повідомлення в чаті (якщо є)
func (a *Channel) logUser(userID, name, login, rewardID, format string, args ...any) {
	l := LogLine{Text: fmt.Sprintf(format, args...), User: name, Login: login}
	a.badgeMu.Lock()
	c, ok := a.badgeCache[userID+"|"+rewardID]
	if !ok {
		c, ok = a.badgeCache[userID]
	}
	a.badgeMu.Unlock()
	if ok {
		l.Color, l.Badges = c.color, c.badges
	}
	a.addLog(l)
}

func (a *Channel) ticker(ctx context.Context) {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.mu.Lock()
			if msg := a.tickGameLocked(time.Now().UnixMilli()); msg != "" {
				a.saveLocked()
				a.emitLocked()
				a.mu.Unlock()
				a.logf("%s", msg)
				continue
			}
			if a.tickAuctionLocked(time.Now().UnixMilli()) {
				a.saveLocked()
				a.emitLocked()
				a.mu.Unlock()
				a.logf("Аукціон: прийом ставок завершено (час вийшов)")
				continue
			}
			s := &a.st.Session
			if s.Status == "running" && s.EndsAt <= time.Now().UnixMilli() {
				s.Status, s.RemainingMs, s.FinishedAt = "finished", 0, time.Now().UnixMilli()
				a.persistCollectionLocked()
				a.saveLocked()
				a.emitLocked()
				a.mu.Unlock()
				a.logf("Збір завершено (час вийшов)")
				continue
			}
			a.mu.Unlock()
		}
	}
}

// ---------- методи для інтерфейсу ----------
func (a *Channel) GetState() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked()
}

func (a *Channel) Logout() {
	a.mu.Lock()
	if a.listenCancel != nil {
		a.listenCancel()
		a.listenCancel = nil
	}
	a.mu.Unlock()
	a.tokMu.Lock()
	a.token = nil
	a.tokMu.Unlock()
	a.host.SaveTokens(nil)
	a.setConn("off", "Не підключено")
	a.setAuth(AuthInfo{Status: "need_login"})
}

func (a *Channel) RefreshRewards() {
	a.mu.Lock()
	bcID := a.bc.ID
	a.mu.Unlock()
	if bcID == "" {
		return
	}
	list, err := a.fetchRewards(bcID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.rewErr = "Не вдалося отримати список нагород (" + err.Error() + "). Можна вибрати нагороду з отриманих погашень нижче."
	} else {
		a.rewErr = ""
		a.rewards = list
		a.autoAuctionRewardsLocked(list)
		// автовибір: якщо нагороду ще не вибрано — беремо ту, де в назві «СабДей»
		if a.st.Settings.RewardID == "" {
			for _, r := range list {
				if isSubdayReward(r.Title) {
					a.st.Settings.RewardID, a.st.Settings.RewardTitle = r.ID, r.Title
					a.saveLocked()
					break
				}
			}
		}
	}
	a.emitLocked()
}

func (a *Channel) SelectReward(id, title string) Snapshot {
	return a.mutate(func(st *State) { st.Settings.RewardID, st.Settings.RewardTitle = strings.TrimSpace(id), title })
}

func (a *Channel) SaveSettings(s Settings) Snapshot {
	var old Settings
	snap := a.mutate(func(st *State) {
		old = st.Settings
		s.RewardID, s.RewardTitle = st.Settings.RewardID, st.Settings.RewardTitle
		s.normalize()
		st.Settings = s
		if st.Session.Status == "idle" {
			st.Session.RemainingMs = int64(s.DurationMin) * 60000
		}
	})
	a.host.SettingsChanged(old, snap.Settings) // десктоп: новий порт віджета, новий тунель ngrok
	if snap.Settings.ObsPort != old.ObsPort {
		snap = a.GetState()
		a.mu.Lock()
		a.emitLocked()
		a.mu.Unlock()
	}
	return snap
}

// ---------- таймер ----------
func (a *Channel) StartTimer() Snapshot {
	return a.mutate(func(st *State) {
		s, now := &st.Session, time.Now().UnixMilli()
		switch s.Status {
		case "running":
			return
		case "paused":
			s.EndsAt = now + s.RemainingMs
		default: // idle / finished — кожен «Старт» починає НОВИЙ збір цього стріму
			a.archiveLocked()
			a.ensureStreamLocked(time.UnixMilli(now)) // довга перерва — новий стрім
			st.CollN = len(st.Past) + 1
			st.Winner, st.Exported, st.Phase, st.Round = "", false, phaseCollect, 0
			st.Vote = VoteState{Status: "idle"}
			st.Game = Timer{Status: "idle"}
			st.Entries = []Entry{}
			st.SeenRedeems = []string{}
			a.lastOrd = nil
			dur := int64(st.Settings.DurationMin) * 60000
			if s.Status == "idle" && s.RemainingMs > 0 {
				dur = s.RemainingMs // ±1 хв до старту
			}
			*s = Timer{StartedAt: now, EndsAt: now + max64(dur, 1000)}
		}
		s.Status = "running"
	})
}

func (a *Channel) PauseTimer() Snapshot {
	return a.mutate(func(st *State) {
		s := &st.Session
		if s.Status != "running" {
			return
		}
		s.RemainingMs = max64(0, s.EndsAt-time.Now().UnixMilli())
		s.Status = "paused"
	})
}

func (a *Channel) FinishTimer() Snapshot {
	return a.mutate(func(st *State) {
		s := &st.Session
		if s.Status != "running" && s.Status != "paused" {
			return
		}
		s.Status, s.RemainingMs, s.FinishedAt = "finished", 0, time.Now().UnixMilli()
		a.persistCollectionLocked()
	})
}

func (a *Channel) AddTime(ms int64) Snapshot {
	return a.mutate(func(st *State) {
		s, now := &st.Session, time.Now().UnixMilli()
		switch s.Status {
		case "running":
			s.EndsAt = max64(now+1000, s.EndsAt+ms)
		case "paused":
			s.RemainingMs = max64(1000, s.RemainingMs+ms)
		} // до старту й після завершення ±1 хв не діє — збір не йде
	})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ---------- замовлення ----------
func (a *Channel) onRedemption(ev Redemption, bc User) {
	a.mu.Lock()
	sr := a.st.SeenRewards[ev.Reward.ID]
	sample := []rune(ev.UserInput)
	if len(sample) > 60 {
		sample = sample[:60]
	}
	a.st.SeenRewards[ev.Reward.ID] = SeenReward{ID: ev.Reward.ID, Title: ev.Reward.Title, Count: sr.Count + 1,
		Ts: time.Now().UnixMilli(), Sample: string(sample), User: ev.UserName}
	if a.st.Settings.RewardID == "" && isSubdayReward(ev.Reward.Title) {
		a.st.Settings.RewardID, a.st.Settings.RewardTitle = ev.Reward.ID, ev.Reward.Title
	}
	bound := a.st.Settings.RewardID
	running := a.st.Session.Status == "running"
	dup := false
	if ev.ID != "" {
		for _, id := range a.st.SeenRedeems {
			if id == ev.ID {
				dup = true
				break
			}
		}
	}
	accept := ev.Reward.ID == bound && running && !dup && strings.TrimSpace(ev.UserInput) != ""
	if accept && ev.ID != "" {
		a.st.SeenRedeems = append(a.st.SeenRedeems, ev.ID)
		if len(a.st.SeenRedeems) > 3000 {
			a.st.SeenRedeems = a.st.SeenRedeems[1000:]
		}
	}
	a.saveLocked()
	a.emitLocked()
	a.mu.Unlock()

	if ev.Reward.ID != bound && a.isAuctionReward(ev.Reward.ID, ev.Reward.Title) {
		a.onAuctionBid(ev)
		return
	}
	if ev.Reward.ID != bound {
		a.logUser(ev.UserID, ev.UserName, ev.UserLogin, ev.Reward.ID, "Нагорода «%s» — не враховано (інша нагорода)", ev.Reward.Title)
		return
	}
	if !running {
		a.logUser(ev.UserID, ev.UserName, ev.UserLogin, ev.Reward.ID, "«%s» — збір не йде, не враховано", ev.UserInput)
		return
	}
	if !accept {
		return
	}
	tier, months, source, color, badges := a.lookupSub(bc.ID, ev.UserID, ev.Reward.ID)
	login := ev.UserLogin
	if login == "" {
		login = strings.ToLower(ev.UserName)
	}
	e := Entry{ID: newID(), Ts: time.Now().UnixMilli(), User: ev.UserName, Login: login, UserID: ev.UserID,
		Tier: tier, Months: months, Text: strings.TrimSpace(ev.UserInput), Source: source, Color: color, Badges: badges}
	a.mu.Lock()
	pts := pointsOf(a.st.Settings, e)
	a.lastOrd = &LastOrder{User: e.User, Text: e.Text, Points: pts, Tier: tier, Months: months, Ts: e.Ts}
	lo := *a.lastOrd
	a.mu.Unlock()
	a.emitEvent("redeem", map[string]any{"id": e.ID, "order": lo}) // підсвітка рядка + алерт в OBS
	if e.Color == "" {
		defer func() { go a.fillColors() }()
	}
	a.mutate(func(st *State) {
		e.Gifts = a.giftsForLocked(e.UserID) // подаровані сабки за 30 днів — +1 бал за кожну
		st.Entries = append(st.Entries, e)
	})
	sub := "не підписник"
	if tier > 0 {
		sub = fmt.Sprintf("T%d, %d міс.", tier, months)
	}
	a.addLog(LogLine{Text: fmt.Sprintf("Замовлення «%s» — %s", e.Text, sub), User: e.User, Login: e.Login, Color: e.Color, Badges: e.Badges})
}

func (a *Channel) UpdateEntry(e Entry) Snapshot {
	return a.mutate(func(st *State) {
		for i := range st.Entries {
			if st.Entries[i].ID != e.ID {
				continue
			}
			x := &st.Entries[i]
			if e.Tier >= 0 && e.Tier <= 3 {
				x.Tier = e.Tier
			}
			if x.Tier > 0 {
				x.Months = e.Months
				if x.Months < 1 {
					x.Months = 1
				}
			} else {
				x.Months = 0
			}
			if t := strings.TrimSpace(e.Text); t != "" {
				x.Text = t
			}
			x.Excluded = e.Excluded
		}
	})
}

// Об'єднання схожих замовлень (drag-n-drop): текст вибраних замовлень замінюється на текст цільового.
func (a *Channel) RenameEntries(ids []string, text string) Snapshot {
	text = strings.TrimSpace(text)
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return a.mutate(func(st *State) {
		if text == "" {
			return
		}
		for i := range st.Entries {
			if set[st.Entries[i].ID] {
				st.Entries[i].Text = text
			}
		}
	})
}

func (a *Channel) DeleteEntry(id string) Snapshot {
	return a.mutate(func(st *State) {
		out := st.Entries[:0]
		for _, e := range st.Entries {
			if e.ID != id {
				out = append(out, e)
			}
		}
		st.Entries = out
	})
}

func (a *Channel) AddManual(user string, tier, months int, text string) (Snapshot, error) {
	user, text = strings.TrimSpace(user), strings.TrimSpace(text)
	if user == "" || text == "" {
		return a.GetState(), errors.New("вкажіть нік і замовлення")
	}
	if tier < 0 || tier > 3 {
		tier = 0
	}
	if tier == 0 {
		months = 0
	} else if months < 1 {
		months = 1
	}
	e := Entry{ID: newID(), Ts: time.Now().UnixMilli(), User: user, Login: strings.ToLower(user), Tier: tier, Months: months, Text: text, Manual: true}
	return a.mutate(func(st *State) { st.Entries = append(st.Entries, e) }), nil
}

// Новий збір: поточний переходить у завершені збори стріму, замовлення очищаються.
func (a *Channel) NewSession() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.archiveLocked()
	a.st.Entries = []Entry{}
	a.st.SeenRedeems = []string{}
	a.st.Session = Timer{Status: "idle", RemainingMs: int64(a.st.Settings.DurationMin) * 60000}
	a.saveLocked()
	a.emitLocked()
	return "", nil
}
