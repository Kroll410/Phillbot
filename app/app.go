package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
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
	SubBadges  map[string]string `json:"subBadges"` // значки підписки каналу: версія ("0", "3", "2012", "3036"…) → картинка // чат відкрито в окремому вікні — вкладка заблокована
}

type App struct {
	ctx context.Context

	baseDir, configFile, tokensFile, stateFile, historyDir, exportsDir, streamsDir string

	mu       sync.Mutex
	st       State
	auth     AuthInfo
	conn     ConnInfo
	rewards  []Reward
	rewErr   string
	logLines []LogLine
	bc       User

	clientID string
	tokMu    sync.Mutex
	token    *Tokens

	badgeMu    sync.Mutex
	badgeCache map[string]subInfo
	badgeImgs  map[string]badgeImg

	chatWinClients int         // підключені вікна чату (SSE з ?window=1)
	chatWinPending bool        // вікно запускається, ще не підключилось
	chatWinProc    *os.Process // процес Edge --app

	chattersMu sync.Mutex
	chatters   map[string]*Chatter
	chattersCh chan struct{}

	chatMu sync.Mutex
	chat   []ChatMsg

	overlay *overlayServer
	lastOrd *LastOrder

	authCancel   context.CancelFunc
	listenCancel context.CancelFunc
}

func NewApp() *App {
	a := &App{badgeCache: map[string]subInfo{}, badgeImgs: map[string]badgeImg{},
		chatters: map[string]*Chatter{}, chattersCh: make(chan struct{}, 1)}
	go a.chattersLoop()
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
	a.st = loadState(a.stateFile)
	a.initStreamLocked()
	a.conn = ConnInfo{Status: "off", Text: "Не підключено"}
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
	go a.ticker(ctx)
	a.overlay = newOverlayServer(a)
	a.mu.Lock()
	port := a.st.Settings.ObsPort
	a.mu.Unlock()
	a.overlay.start(port)
	if low := strings.ToLower(a.baseDir); strings.Contains(low, `\temp\`) || strings.Contains(low, `\tmp\`) {
		a.logf("⚠ Програму запущено з тимчасової папки (архіву) — налаштування й токени зникнуть. Розпакуйте PhillBot.exe у звичайну папку.")
	}
}

func (a *App) shutdown(ctx context.Context) {
	if a.overlay != nil {
		a.overlay.stop()
	}
	a.mu.Lock()
	a.saveLocked()
	a.mu.Unlock()
}

// ---------- внутрішнє ----------

func (a *App) snapshotLocked() Snapshot {
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
	var obs OverlayInfo
	if a.overlay != nil {
		obs = a.overlay.info()
	}
	return Snapshot{State: st, Auth: auth, Conn: a.conn, Rewards: append([]Reward(nil), a.rewards...), RewardsErr: a.rewErr,
		BaseDir: a.baseDir, ClientID: a.clientID, Now: time.Now().UnixMilli(), Log: append([]LogLine(nil), a.logLines...), Obs: obs,
		ChatWindow: a.chatWinClients > 0 || a.chatWinPending, SubBadges: a.subBadges()}
}

// подія для головного вікна і для оверлеїв OBS
func (a *App) emitEvent(name string, data any) {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, name, data)
	}
	if a.overlay != nil {
		a.overlay.hub.broadcast(name, data)
	}
}

func (a *App) saveLocked() {
	b, err := json.MarshalIndent(a.st, "", "  ")
	if err == nil {
		_ = writeFileAtomic(a.stateFile, b)
	}
}

func (a *App) emitLocked() {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "state", a.snapshotLocked())
	}
	if a.overlay != nil {
		a.overlay.notify()
	}
}

// змінити стан, зберегти і повідомити інтерфейс
func (a *App) mutate(fn func(st *State)) Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn(&a.st)
	a.saveLocked()
	a.emitLocked()
	return a.snapshotLocked()
}

func (a *App) setAuth(ai AuthInfo) {
	a.mu.Lock()
	if ai.User == nil && ai.Status == "ok" {
		ai.User = a.auth.User
	}
	a.auth = ai
	a.emitLocked()
	a.mu.Unlock()
}

func (a *App) setConn(status, text string) {
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

func (a *App) addLog(l LogLine) {
	l.Ts = time.Now().UnixMilli()
	a.mu.Lock()
	a.logLines = append(a.logLines, l)
	if len(a.logLines) > 200 {
		a.logLines = a.logLines[len(a.logLines)-200:]
	}
	a.emitLocked()
	a.mu.Unlock()
}

func (a *App) logf(format string, args ...any) { a.addLog(LogLine{Text: fmt.Sprintf(format, args...)}) }

// подія від глядача: колір і бейджі беремо з останнього його повідомлення в чаті (якщо є)
func (a *App) logUser(userID, name, login, rewardID, format string, args ...any) {
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

func (a *App) ticker(ctx context.Context) {
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

// ---------- методи для інтерфейсу ----------

func (a *App) GetState() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.snapshotLocked()
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

func (a *App) Logout() {
	a.mu.Lock()
	if a.listenCancel != nil {
		a.listenCancel()
		a.listenCancel = nil
	}
	a.mu.Unlock()
	a.tokMu.Lock()
	a.token = nil
	a.tokMu.Unlock()
	_ = os.Remove(a.tokensFile)
	a.setConn("off", "Не підключено")
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

func (a *App) RefreshRewards() {
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

func (a *App) SelectReward(id, title string) Snapshot {
	return a.mutate(func(st *State) { st.Settings.RewardID, st.Settings.RewardTitle = strings.TrimSpace(id), title })
}

func (a *App) SaveSettings(s Settings) Snapshot {
	var oldPort int
	snap := a.mutate(func(st *State) {
		oldPort = st.Settings.ObsPort
		s.RewardID, s.RewardTitle = st.Settings.RewardID, st.Settings.RewardTitle
		s.normalize()
		st.Settings = s
		if st.Session.Status == "idle" {
			st.Session.RemainingMs = int64(s.DurationMin) * 60000
		}
	})
	if a.overlay != nil && s.ObsPort != oldPort {
		a.overlay.stop()
		a.overlay.start(s.ObsPort)
		snap = a.GetState()
		a.mu.Lock()
		a.emitLocked()
		a.mu.Unlock()
	}
	return snap
}

// ---------- таймер ----------

func (a *App) StartTimer() Snapshot {
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

func (a *App) PauseTimer() Snapshot {
	return a.mutate(func(st *State) {
		s := &st.Session
		if s.Status != "running" {
			return
		}
		s.RemainingMs = max64(0, s.EndsAt-time.Now().UnixMilli())
		s.Status = "paused"
	})
}

func (a *App) FinishTimer() Snapshot {
	return a.mutate(func(st *State) {
		s := &st.Session
		if s.Status != "running" && s.Status != "paused" {
			return
		}
		s.Status, s.RemainingMs, s.FinishedAt = "finished", 0, time.Now().UnixMilli()
		a.persistCollectionLocked()
	})
}

func (a *App) AddTime(ms int64) Snapshot {
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

func (a *App) onRedemption(ev Redemption, bc User) {
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
	a.mutate(func(st *State) { st.Entries = append(st.Entries, e) })
	sub := "не підписник"
	if tier > 0 {
		sub = fmt.Sprintf("T%d, %d міс.", tier, months)
	}
	a.addLog(LogLine{Text: fmt.Sprintf("Замовлення «%s» — %s", e.Text, sub), User: e.User, Login: e.Login, Color: e.Color, Badges: e.Badges})
}

func (a *App) UpdateEntry(e Entry) Snapshot {
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
func (a *App) RenameEntries(ids []string, text string) Snapshot {
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

func (a *App) DeleteEntry(id string) Snapshot {
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

func (a *App) AddManual(user string, tier, months int, text string) (Snapshot, error) {
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

// Новий збір: поточний архівується в history/, замовлення очищаються.
func (a *App) NewSession() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.archiveLocked()
	a.st.Entries = []Entry{}
	a.st.SeenRedeems = []string{}
	a.st.Session = Timer{Status: "idle", RemainingMs: int64(a.st.Settings.DurationMin) * 60000}
	a.saveLocked()
	a.emitLocked()
	return p, nil
}
