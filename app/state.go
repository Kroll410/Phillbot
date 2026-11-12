package main

// Стан застосунку: налаштування, таймер збору, замовлення. Зберігається в subday_state.json поруч з exe.

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Settings struct {
	RewardID     string         `json:"rewardId"`
	RewardTitle  string         `json:"rewardTitle"`
	DurationMin  int            `json:"durationMin"`
	NonSubPoints int            `json:"nonSubPoints"`
	Mult         map[string]int `json:"mult"`
	DupMode      string         `json:"dupMode"` // last | first | all
	Cap          int            `json:"cap"`     // куль сумарно у файлі для Marbles (≤ 1000)
	ObsPort      int            `json:"obsPort"` // локальний сервер для OBS (127.0.0.1)
}

type Timer struct {
	Status      string `json:"status"` // idle | running | paused | finished
	EndsAt      int64  `json:"endsAt"`
	RemainingMs int64  `json:"remainingMs"`
	StartedAt   int64  `json:"startedAt"`
	FinishedAt  int64  `json:"finishedAt"`
}

type Entry struct {
	ID       string `json:"id"`
	Ts       int64  `json:"ts"`
	User     string `json:"user"`
	Login    string `json:"login"`
	UserID   string `json:"userId"`
	Tier     int    `json:"tier"`
	Months   int    `json:"months"`
	Text     string `json:"text"`
	Excluded bool   `json:"excluded"`
	Manual   bool   `json:"manual"`
	// діагностика: що сказали API і бейдж
	Source string `json:"source,omitempty"`
}

type SeenReward struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Count  int    `json:"count"`
	Ts     int64  `json:"ts"`
	Sample string `json:"sample"`
	User   string `json:"user"`
}

type State struct {
	Settings    Settings              `json:"settings"`
	Session     Timer                 `json:"session"`
	Entries     []Entry               `json:"entries"`
	SeenRewards map[string]SeenReward `json:"seenRewards"`
	SeenRedeems []string              `json:"seenRedeems"`
}

func defaultSettings() Settings {
	return Settings{DurationMin: 10, NonSubPoints: 0, Mult: map[string]int{"1": 1, "2": 2, "3": 3},
		DupMode: "last", Cap: 1000, ObsPort: defaultObsPort}
}

func defaultState() State {
	s := defaultSettings()
	return State{Settings: s, Session: Timer{Status: "idle", RemainingMs: int64(s.DurationMin) * 60000},
		Entries: []Entry{}, SeenRewards: map[string]SeenReward{}, SeenRedeems: []string{}}
}

func (s *Settings) normalize() {
	d := defaultSettings()
	if s.DurationMin < 1 {
		s.DurationMin = d.DurationMin
	}
	if s.Mult == nil {
		s.Mult = d.Mult
	}
	for _, k := range []string{"1", "2", "3"} {
		if _, ok := s.Mult[k]; !ok {
			s.Mult[k] = d.Mult[k]
		}
		if s.Mult[k] < 0 {
			s.Mult[k] = 0
		}
	}
	if s.NonSubPoints < 0 {
		s.NonSubPoints = 0
	}
	if s.DupMode != "first" && s.DupMode != "all" {
		s.DupMode = "last"
	}
	if s.Cap < 1 || s.Cap > 1000 {
		s.Cap = 1000
	}
	if s.ObsPort < 1024 || s.ObsPort > 65000 {
		s.ObsPort = defaultObsPort
	}
}

func loadState(file string) State {
	st := defaultState()
	b, err := os.ReadFile(file)
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	st.Settings.normalize()
	if st.Entries == nil {
		st.Entries = []Entry{}
	}
	if st.SeenRewards == nil {
		st.SeenRewards = map[string]SeenReward{}
	}
	if st.SeenRedeems == nil {
		st.SeenRedeems = []string{}
	}
	switch st.Session.Status {
	case "running":
		// застосунок закрили під час збору — продовжуємо, якщо час ще не вийшов
		if st.Session.EndsAt <= time.Now().UnixMilli() {
			st.Session.Status = "finished"
			st.Session.RemainingMs = 0
			st.Session.FinishedAt = st.Session.EndsAt
		}
	case "idle", "paused", "finished":
	default:
		st.Session = defaultState().Session
	}
	return st
}

func writeFileAtomic(file string, data []byte) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func newID() string {
	return strconv.FormatInt(time.Now().UnixMilli(), 36) + strconv.FormatInt(rand.Int63n(1<<30), 36)
}

func archiveName(dir string, t time.Time) string {
	return filepath.Join(dir, "subday_"+t.Format("2006-01-02_15-04-05")+".json")
}

func isSubdayReward(title string) bool {
	t := strings.ToLower(title)
	return strings.Contains(t, "сабдей") || strings.Contains(t, "subday") || strings.Contains(t, "саб дей")
}

func fmtErr(prefix string, err error) string { return fmt.Sprintf("%s: %v", prefix, err) }
