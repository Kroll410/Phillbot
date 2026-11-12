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
	Auth       AuthInfo `json:"auth"`
	Conn       ConnInfo `json:"conn"`
	Rewards    []Reward `json:"rewards"`
	RewardsErr string   `json:"rewardsErr"`
	BaseDir    string   `json:"baseDir"`
	ClientID   string   `json:"clientId"`
	Now        int64    `json:"now"`
	Log        []string `json:"log"`
}

type App struct {
	ctx context.Context

	baseDir, configFile, tokensFile, stateFile, historyDir, exportsDir string

	mu       sync.Mutex
	st       State
	auth     AuthInfo
	conn     ConnInfo
	rewards  []Reward
	rewErr   string
	logLines []string
	bc       User

	clientID string
	tokMu    sync.Mutex
	token    *Tokens

	badgeMu    sync.Mutex
	badgeCache map[string]subInfo

	authCancel   context.CancelFunc
	listenCancel context.CancelFunc
}

func NewApp() *App {
	a := &App{badgeCache: map[string]subInfo{}}
	if exe, err := os.Executable(); err == nil {
		a.baseDir = filepath.Dir(exe)
	} else {
		a.baseDir = "."
	}
	a.configFile = filepath.Join(a.baseDir, "config.json")
	a.tokensFile = filepath.Join(a.baseDir, "tokens.json")
	a.stateFile = filepath.Join(a.baseDir, "subday_state.json")
	a.historyDir = filepath.Join(a.baseDir, "history")
	a.exportsDir = filepath.Join(a.baseDir, "exports")
	a.st = loadState(a.stateFile)
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
	if low := strings.ToLower(a.baseDir); strings.Contains(low, `\temp\`) || strings.Contains(low, `\tmp\`) {
		a.logf("⚠ Програму запущено з тимчасової папки (архіву) — налаштування й токени зникнуть. Розпакуйте SubDay.exe у звичайну папку.")
	}
}

func (a *App) shutdown(ctx context.Context) {
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
	return Snapshot{State: st, Auth: auth, Conn: a.conn, Rewards: append([]Reward(nil), a.rewards...), RewardsErr: a.rewErr,
		BaseDir: a.baseDir, ClientID: a.clientID, Now: time.Now().UnixMilli(), Log: append([]string(nil), a.logLines...)}
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

func (a *App) logf(format string, args ...any) {
	a.mu.Lock()
	a.logLines = append(a.logLines, time.Now().Format("15:04:05")+"  "+fmt.Sprintf(format, args...))
	if len(a.logLines) > 200 {
		a.logLines = a.logLines[len(a.logLines)-200:]
	}
	a.emitLocked()
	a.mu.Unlock()
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
			s := &a.st.Session
			if s.Status == "running" && s.EndsAt <= time.Now().UnixMilli() {
				s.Status, s.RemainingMs, s.FinishedAt = "finished", 0, time.Now().UnixMilli()
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
	return a.mutate(func(st *State) {
		s.RewardID, s.RewardTitle = st.Settings.RewardID, st.Settings.RewardTitle
		s.normalize()
		st.Settings = s
		if st.Session.Status == "idle" {
			st.Session.RemainingMs = int64(s.DurationMin) * 60000
		}
	})
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
		case "idle":
			s.EndsAt = now + max64(s.RemainingMs, 1000)
			s.StartedAt = now
		default: // finished — новий відрізок збору, замовлення зберігаються
			s.EndsAt = now + int64(st.Settings.DurationMin)*60000
			if s.StartedAt == 0 {
				s.StartedAt = now
			}
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
	})
}

func (a *App) AddTime(ms int64) Snapshot {
	return a.mutate(func(st *State) {
		s, now := &st.Session, time.Now().UnixMilli()
		switch s.Status {
		case "running":
			s.EndsAt = max64(now+1000, s.EndsAt+ms)
		case "paused", "idle":
			s.RemainingMs = max64(1000, s.RemainingMs+ms)
		case "finished":
			if ms > 0 {
				s.EndsAt, s.Status = now+ms, "running"
			}
		}
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

	if ev.Reward.ID != bound {
		a.logf("Redeem іншої нагороди «%s» від %s — не враховано", ev.Reward.Title, ev.UserName)
		return
	}
	if !running {
		a.logf("Redeem від %s («%s») — збір не йде, не враховано", ev.UserName, ev.UserInput)
		return
	}
	if !accept {
		return
	}
	tier, months, source := a.lookupSub(bc.ID, ev.UserID)
	login := ev.UserLogin
	if login == "" {
		login = strings.ToLower(ev.UserName)
	}
	e := Entry{ID: newID(), Ts: time.Now().UnixMilli(), User: ev.UserName, Login: login, UserID: ev.UserID,
		Tier: tier, Months: months, Text: strings.TrimSpace(ev.UserInput), Source: source}
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "redeem", e.ID) // підсвітка нового рядка
	}
	a.mutate(func(st *State) { st.Entries = append(st.Entries, e) })
	sub := "не підписник"
	if tier > 0 {
		sub = fmt.Sprintf("T%d, %d міс.", tier, months)
	}
	a.logf("★ %s: «%s» — %s", ev.UserName, e.Text, sub)
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
	archived := ""
	if len(a.st.Entries) > 0 {
		if err := os.MkdirAll(a.historyDir, 0755); err != nil {
			return "", err
		}
		p := archiveName(a.historyDir, time.Now())
		b, _ := json.MarshalIndent(map[string]any{"archivedAt": time.Now().Format(time.RFC3339), "settings": a.st.Settings,
			"session": a.st.Session, "entries": a.st.Entries}, "", "  ")
		if err := writeFileAtomic(p, b); err != nil {
			return "", err
		}
		archived = p
	}
	a.st.Entries = []Entry{}
	a.st.SeenRedeems = []string{}
	a.st.Session = Timer{Status: "idle", RemainingMs: int64(a.st.Settings.DurationMin) * 60000}
	a.saveLocked()
	a.emitLocked()
	return archived, nil
}
