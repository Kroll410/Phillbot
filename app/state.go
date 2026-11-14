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
	DupMode      string         `json:"dupMode"` // кілька замовлень від глядача: last — останнє, first — перше, split — бали діляться між усіма
	ObsPort      int            `json:"obsPort"` // локальний сервер для OBS (127.0.0.1)
	Chat         ChatSettings   `json:"chat"`
	GameMin      int            `json:"gameMin"`                // хвилин на гру переможця (етап 2)
	VoteMin      int            `json:"voteMin"`                // хвилин на голосування за продовження
	GameSoundOff bool           `json:"gameSoundOff,omitempty"` // без звуку, коли час гри переможця вийшов
	GameSoundURL string         `json:"gameSoundUrl,omitempty"` // свій звук (https-посилання на аудіо); порожньо — вбудований сигнал
	VoteFirst    bool           `json:"voteFirst"`              // голосування 1/2: true — рахується лише перший голос глядача, false — можна переголосувати (останній)
	// Донати Donatello через колбеки: тунель ngrok (authtoken, необов'язковий постійний домен) і ключ X-Key з Donatello.
	// Усе це — лише на цьому ПК (subday_state.json не передається).
	NgrokToken     string `json:"ngrokToken"`
	NgrokDomain    string `json:"ngrokDomain"`
	DonKey         string `json:"donKey"`
	DonUAHPerPoint int    `json:"donUahPerPoint"` // за замовчуванням 30: 120 грн = 4 бали
}

// Налаштування вкладки «Чат» (вигляд чату в програмі та в окремому вікні).
type ChatSettings struct {
	FontSize   int  `json:"fontSize"`
	HideTime   bool `json:"hideTime"`
	HideBadges bool `json:"hideBadges"`
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
	// як нік виглядає в чаті Twitch (з повідомлення, що прийшло разом із замовленням)
	Color  string      `json:"color,omitempty"`
	Badges []ChatBadge `json:"badges,omitempty"`
	// донат Donatello: бали вже пораховано з гривень (тір/стаж не застосовуються)
	Donation  bool `json:"donation,omitempty"`
	DonPoints int  `json:"donPoints,omitempty"`
	// скільки сабок глядач подарував за 30 днів на момент замовлення: +1 бал за кожну
	Gifts int     `json:"gifts,omitempty"`
	UAH   float64 `json:"uah,omitempty"`
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
	// стрім (СабДей) і його збори: Past — завершені збори, поточний — Session/Entries з номером CollN
	Stream StreamInfo   `json:"stream"`
	Past   []Collection `json:"past"`
	CollN  int          `json:"collN"`
	// етапи: collect (збір) → play (гра переможця, окремий таймер)
	Phase    string    `json:"phase"`
	Game     Timer     `json:"game"`
	Winner   string    `json:"winner"`
	Exported bool      `json:"exported"` // поточний збір уже експортовано в Marbles — можна вибрати переможця
	Round    int       `json:"round"`    // раунд гри переможця (продовження за голосуванням)
	Vote     VoteState `json:"vote"`
	// «Переглядовий» — аукціон
	Auction AuctionState `json:"auction"`
	// Donatello: уже отримані донати (повторний колбек того самого донату не рахується)
	DonSeen []string `json:"donSeen"`
	// подаровані сабки, отримані наживо (журнал за 30 днів)
	Gifts []GiftEvent `json:"gifts"`
}

func defaultSettings() Settings {
	return Settings{DurationMin: 10, NonSubPoints: 0, Mult: map[string]int{"1": 1, "2": 2, "3": 3},
		DupMode: "last", ObsPort: defaultObsPort, GameMin: 40, VoteMin: 3}
}

func defaultState() State {
	s := defaultSettings()
	st := State{Settings: s, Session: Timer{Status: "idle", RemainingMs: int64(s.DurationMin) * 60000},
		Entries: []Entry{}, SeenRewards: map[string]SeenReward{}, SeenRedeems: []string{}}
	st.Auction.normalize()
	return st
}

func (s *Settings) normalize() {
	d := defaultSettings()
	s.DurationMin = 10 // збір замовлень завжди 10 хв (±1 хв кнопками); налаштування прибрано
	// правила балів фіксовані: T1×1, T2×2, T3×3, не-підписник 0, від чатерса — останнє замовлення
	s.Mult, s.NonSubPoints = map[string]int{"1": 1, "2": 2, "3": 3}, 0
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
	if s.DupMode != "first" && s.DupMode != "split" {
		s.DupMode = "last"
	}
	if s.Chat.FontSize < 11 || s.Chat.FontSize > 28 {
		s.Chat.FontSize = 14
	}
	if s.GameMin < 1 || s.GameMin > 600 {
		s.GameMin = 40
	}
	if s.VoteMin < 1 || s.VoteMin > 60 {
		s.VoteMin = 3
	}
	s.GameSoundURL = strings.TrimSpace(s.GameSoundURL)
	if !strings.HasPrefix(s.GameSoundURL, "https://") || len(s.GameSoundURL) > 500 {
		s.GameSoundURL = "" // лише https-посилання розумної довжини
	}
	if s.ObsPort < 1024 || s.ObsPort > 65000 {
		s.ObsPort = defaultObsPort
	}
	if s.DonUAHPerPoint < 1 || s.DonUAHPerPoint > 100000 {
		s.DonUAHPerPoint = 30
	}
	s.NgrokToken, s.DonKey = strings.TrimSpace(s.NgrokToken), strings.TrimSpace(s.DonKey)
	s.NgrokDomain = ngrokDomain(s.NgrokDomain)
}

// loadState — стан зі старого subday_state.json (лише для перенесення в БД).
func loadState(file string) State {
	st := defaultState()
	b, err := os.ReadFile(file)
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return fixState(st)
}

// fixState — привести збережений стан до ладу після запуску: правила балів, таймери, що вийшли, поки програма була закрита.
func fixState(st State) State {
	st.Settings.normalize()
	if st.Session.Status == "idle" || st.Session.Status == "" { // ще не почато — завжди повні 10 хв
		st.Session.RemainingMs = int64(st.Settings.DurationMin) * 60000
	}
	if st.Entries == nil {
		st.Entries = []Entry{}
	}
	if st.SeenRewards == nil {
		st.SeenRewards = map[string]SeenReward{}
	}
	if st.Phase != phasePlay {
		st.Phase = phaseCollect
	}
	if st.SeenRedeems == nil {
		st.SeenRedeems = []string{}
	}
	st.Auction.normalize()
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

// нормалізація домену з налаштувань: «abc.ngrok-free.app», «https://abc.ngrok-free.app/» → «abc.ngrok-free.app»
func ngrokDomain(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	return strings.TrimRight(s, "/")
}
