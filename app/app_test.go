//go:build !server

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBadges(t *testing.T) {
	cases := []struct {
		b        []Badge
		tier, mo int
	}{
		{[]Badge{{"subscriber", "2006", "6"}}, 2, 6},
		{[]Badge{{"vip", "1", ""}, {"subscriber", "3012", "5"}}, 3, 5},
		{[]Badge{{"subscriber", "0", "1"}}, 1, 1},
		{[]Badge{{"founder", "0", "14"}}, 1, 14},
		{[]Badge{{"moderator", "1", ""}}, 0, 0},
	}
	for _, c := range cases {
		if tr, mo := subFromBadges(c.b); tr != c.tier || mo != c.mo {
			t.Errorf("%v -> %d,%d", c.b, tr, mo)
		}
	}
}

func newTestApp(t *testing.T) *App {
	a := NewApp()
	dir := t.TempDir()
	a.baseDir = dir
	a.stateFile = filepath.Join(dir, "subday_state.json")
	a.historyDir = filepath.Join(dir, "history")
	a.exportsDir = filepath.Join(dir, "exports")
	a.streamsDir = filepath.Join(dir, "streams")
	a.openStore()
	if a.store == nil {
		t.Fatal(a.storeErr)
	}
	t.Cleanup(func() { a.store.Close() })
	return a
}

// стан, яким його побачить програма після перезапуску (окреме з'єднання з тією самою БД)
func reloadState(t *testing.T, a *App) State {
	t.Helper()
	s, err := openStore(filepath.Join(a.baseDir, "phillbot.db"), "local")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, ok, err := s.Load()
	if err != nil || !ok {
		t.Fatalf("reload: ok=%v err=%v", ok, err)
	}
	return fixState(st)
}

func TestTimerAndRedeems(t *testing.T) {
	a := newTestApp(t)
	a.SelectReward("r1", "СабДей Замовлення")
	ev := Redemption{ID: "x1", UserID: "", UserName: "Phill", UserInput: "Dark Souls"}
	ev.Reward.ID = "r1"
	a.onRedemption(ev, User{}) // таймер не йде — не записується
	if len(a.st.Entries) != 0 {
		t.Fatal("recorded while idle")
	}
	s := a.StartTimer()
	if s.Session.Status != "running" || s.Session.EndsAt-time.Now().UnixMilli() < 9*60000 {
		t.Fatalf("bad start %+v", s.Session)
	}
	before := a.st.Session.EndsAt
	a.AddTime(60000)
	if a.st.Session.EndsAt-before != 60000 {
		t.Fatal("+1 min")
	}
	a.PauseTimer()
	if a.st.Session.Status != "paused" || a.st.Session.RemainingMs < 10*60000 {
		t.Fatalf("pause %+v", a.st.Session)
	}
	a.StartTimer()
	a.FinishTimer()
	if a.st.Session.Status != "finished" {
		t.Fatal("finish")
	}
	// після завершення ±1 хв не діє — збір не йде
	a.AddTime(60000)
	if a.st.Session.Status != "finished" {
		t.Fatal("+1 must not resume a finished collection")
	}
	// повторний redeem з тим самим id ігнорується; інша нагорода — теж
	a.st.Session.Status = "running"
	other := ev
	other.Reward.ID = "r2"
	other.ID = "x2"
	a.onRedemption(other, User{})
	if len(a.st.Entries) != 0 {
		t.Fatal("other reward recorded")
	}
	if _, ok := a.st.SeenRewards["r2"]; !ok {
		t.Fatal("seen reward not stored")
	}
	st := reloadState(t, a)
	if st.Settings.RewardID != "r1" {
		t.Fatal("reload")
	}
}

func TestManualAndArchive(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.AddManual("", 1, 1, "x"); err == nil {
		t.Fatal("empty user accepted")
	}
	s, _ := a.AddManual("Kroll", 3, 46, "Elden Ring")
	if len(s.Entries) != 1 || s.Entries[0].Months != 46 {
		t.Fatal(s.Entries)
	}
	e := s.Entries[0]
	e.Tier, e.Months, e.Text = 0, 5, "  Hades "
	s = a.UpdateEntry(e)
	if s.Entries[0].Months != 0 || s.Entries[0].Text != "Hades" {
		t.Fatal(s.Entries[0])
	}
	if _, err := a.NewSession(); err != nil || len(a.st.Entries) != 0 || len(a.st.Past) != 1 {
		t.Fatal(err)
	}
	if st := reloadState(t, a); len(st.Past) != 1 || len(st.Past[0].Entries) != 1 || st.Past[0].Entries[0].Text != "Hades" {
		t.Fatalf("archived collection not in DB: %+v", st.Past)
	}
}

func TestMarbleName(t *testing.T) {
	cases := map[string]string{
		"Dark Souls":  "Dark Souls",
		"Ой, \"так\"": "Ой так",
		"Дуже довга назва замовлення тут": "Дуже довга назв...",
		"  a\tb  ": "a b",
	}
	for in, want := range cases {
		if got := marbleName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestAggregateAndMarbles(t *testing.T) {
	s := defaultSettings()
	es := []Entry{
		{ID: "1", Ts: 1, UserID: "u1", User: "Phill", Tier: 3, Months: 46, Text: "Dark Souls"},
		{ID: "2", Ts: 2, UserID: "u2", User: "kroll", Tier: 1, Months: 20, Text: "dark  souls!"},
		{ID: "3", Ts: 3, UserID: "u3", User: "x", Tier: 1, Months: 5, Text: "Elden Ring"},
		{ID: "4", Ts: 4, UserID: "u3", User: "x", Tier: 1, Months: 6, Text: "Hades"}, // замінює Elden Ring
		{ID: "5", Ts: 5, UserID: "u4", User: "y", Tier: 0, Text: "Minecraft"},
	}
	g := aggregate(s, es)
	if len(g) != 3 || g[0].Name != "Dark Souls" || g[0].Points != 53 || g[1].Name != "Hades" || g[1].Points != 2 || g[2].Points != 0 {
		t.Fatalf("%+v", g)
	}
	lines := marbleLines(s, es)
	if len(lines) != 55 {
		t.Fatal(len(lines))
	}
	big := append([]Entry{}, es...)
	big = append(big, Entry{ID: "6", Ts: 6, UserID: "u6", User: "z", Tier: 3, Months: 400, Text: "Hades"})
	if n := len(marbleLines(s, big)); n != 55+400 { // без ліміту: куль = сума балів
		t.Fatal(n)
	}
	s.DupMode = "first"
	if g := aggregate(s, es); g[1].Name != "Elden Ring" {
		t.Fatalf("first: %+v", g)
	}
}

func TestExport(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.ExportMarbles(false); err == nil {
		t.Fatal("empty export accepted")
	}
	a.st.Entries = []Entry{{ID: "1", UserID: "u", User: "U", Tier: 1, Months: 4, Text: "Ой, \"так\""}}
	r, err := a.ExportMarbles(false)
	if err != nil || r.Lines != 2 {
		t.Fatal(r, err)
	}
	if _, err := a.ExportMarbles(false); err != nil { // повторний експорт — у теці last усе одно один файл
		t.Fatal(err)
	}
	if fs, _ := os.ReadDir(r.Dir); len(fs) != 1 || filepath.Base(r.Dir) != "last" {
		t.Fatal("last folder must hold exactly one file", fs)
	}
	if _, err := os.Stat(filepath.Join(a.baseDir, "marbles", "history")); err == nil {
		t.Fatal("no dated copies: history lives in the DB")
	}
	b, _ := os.ReadFile(r.Path)
	if b[0] != 0xEF || b[1] != 0xBB || b[2] != 0xBF || string(b[3:]) != "Ой так\r\nОй так\r\n" {
		t.Fatalf("%q", b)
	}
	p, err := a.SaveExport("subday_test.csv", "a;b")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	if b[0] != 0xEF {
		t.Fatal("csv bom")
	}
}

func TestRenameEntries(t *testing.T) {
	a := newTestApp(t)
	a.AddManual("A", 1, 3, "dark souls 3")
	a.AddManual("B", 1, 2, "DS3")
	s, _ := a.AddManual("C", 1, 1, "Hades")
	ids := []string{s.Entries[1].ID}
	s = a.RenameEntries(ids, "  dark souls 3 ")
	g := aggregate(s.Settings, s.Entries)
	if len(g) != 2 || g[0].Name != "dark souls 3" || g[0].Points != 2 || g[0].Count != 2 {
		t.Fatalf("%+v", g)
	}
	if s = a.RenameEntries(ids, " "); s.Entries[1].Text != "dark souls 3" {
		t.Fatal("empty rename applied")
	}
}

func TestEachStartIsNewCollection(t *testing.T) {
	a := newTestApp(t)
	a.StartTimer()
	a.AddManual("Phill", 3, 46, "Dark Souls")
	a.AddManual("kroll", 1, 20, "dark souls!")
	a.PauseTimer()
	if s := a.StartTimer(); len(s.Entries) != 2 || s.CollN != 1 { // «Продовжити» після паузи — той самий збір
		t.Fatal("resume changed collection", s.CollN)
	}
	a.FinishTimer()
	streamID := a.st.Stream.ID
	if _, err := os.Stat(a.exportsDir); err == nil {
		t.Fatal("CSV must be written only on demand")
	}
	if txt := collectionCSV(a.st.Settings, a.st.Entries); !strings.HasPrefix(txt, "sep=;\r\n") || !strings.Contains(txt, "Dark Souls;53;2;") {
		t.Fatalf("%q", txt)
	}
	s := a.AddTime(60000) // +1 хв після завершення не діє
	if s.Session.Status != "finished" || len(s.Entries) != 2 {
		t.Fatal("+1 after finish")
	}
	s = a.StartTimer() // наступний збір того ж стріму
	if len(s.Entries) != 0 || s.CollN != 2 || len(s.Past) != 1 || s.Stream.ID != streamID {
		t.Fatalf("second collection: coll=%d past=%d stream=%s", s.CollN, len(s.Past), s.Stream.ID)
	}
	a.AddManual("Zoryana", 2, 3, "Hades")
	a.FinishTimer()
	f, err := a.GetStream(streamID)
	if err != nil || len(f.Collections) != 2 || f.Collections[0].N != 1 || f.Collections[1].N != 2 {
		t.Fatal("stream file", err, len(f.Collections))
	}
	if list := a.ListStreams(); len(list) != 1 || !list[0].Current || list[0].Collections != 2 || list[0].Orders != 3 {
		t.Fatalf("%+v", list)
	}
	// довга перерва — новий стрім, збори знову з 1
	a.mu.Lock()
	a.st.Session.FinishedAt -= int64(7 * time.Hour / time.Millisecond)
	for i := range a.st.Entries {
		a.st.Entries[i].Ts -= int64(7 * time.Hour / time.Millisecond)
	}
	a.st.Stream.StartedAt -= int64(8 * time.Hour / time.Millisecond)
	a.st.Session.StartedAt -= int64(8 * time.Hour / time.Millisecond)
	for i := range a.st.Past {
		a.st.Past[i].FinishedAt -= int64(8 * time.Hour / time.Millisecond)
	}
	a.mu.Unlock()
	s = a.StartTimer()
	if s.Stream.ID == streamID || s.CollN != 1 || len(s.Past) != 0 {
		t.Fatalf("new stream expected: %s coll=%d past=%d", s.Stream.ID, s.CollN, len(s.Past))
	}
	if list := a.ListStreams(); len(list) != 2 {
		t.Fatalf("streams: %+v", list)
	}
	s = a.NewStream() // вручну
	if s.Session.Status != "idle" || len(s.Past) != 0 {
		t.Fatal("manual new stream")
	}
	if _, err := a.GetStream("../x"); err == nil {
		t.Fatal("bad id accepted")
	}
}

func TestHidingLatestOrderCountsNothing(t *testing.T) {
	s := defaultSettings()
	es := []Entry{
		{ID: "1", Ts: 1, UserID: "u", User: "x", Tier: 1, Months: 5, Text: "Elden Ring"},
		{ID: "2", Ts: 2, UserID: "u", User: "x", Tier: 1, Months: 5, Text: "Hades", Excluded: true},
	}
	if g := aggregate(s, es); len(g) != 0 {
		t.Fatalf("older order revived: %+v", g)
	}
	if eff := effectiveIDs(s, es); len(eff) != 1 || !eff["2"] {
		t.Fatal(eff)
	}
}

func TestGamePhase(t *testing.T) {
	a := newTestApp(t)
	a.StartTimer()
	a.AddManual("Phill", 3, 46, "Dark Souls")
	if _, err := a.SetWinner("Dark Souls"); err == nil {
		t.Fatal("winner allowed before export")
	}
	a.FinishTimer()
	if _, err := a.ExportMarbles(false); err != nil {
		t.Fatal(err)
	}
	if !a.st.Exported {
		t.Fatal("export flag")
	}
	s, err := a.SetWinner("Dark Souls")
	if err != nil || s.Phase != "play" || s.Game.Status != "running" || s.Game.EndsAt-time.Now().UnixMilli() < 39*60000 {
		t.Fatalf("play: %v %+v", err, s.Game)
	}
	s = a.GamePause()
	if s.Game.Status != "paused" {
		t.Fatal("pause")
	}
	s = a.GameAddTime(60000)
	if s.Game.RemainingMs < 40*60000 {
		t.Fatal("+1")
	}
	a.GameResume()
	// час гри вийшов — етап 2 НЕ закінчується, чекаємо стрімера
	a.mu.Lock()
	a.st.Game.EndsAt = time.Now().UnixMilli() - 1
	msg := a.tickGameLocked(time.Now().UnixMilli())
	a.mu.Unlock()
	if msg == "" || a.st.Phase != "play" || a.st.Game.Status != "finished" {
		t.Fatal("game must wait for streamer", a.st.Phase, a.st.Game.Status)
	}
	if f, _ := a.GetStream(a.st.Stream.ID); len(f.Collections) != 1 || f.Collections[0].Winner != "Dark Souls" {
		t.Fatal("winner not saved in stream")
	}
	// голосування: «1» продовжити, «2» наступний збір; від кожного — останній голос
	if _, err := a.StartVote(); err != nil {
		t.Fatal(err)
	}
	a.onVoteMessage("u1", "1", true)
	a.onVoteMessage("u2", " 1 ", true)
	a.onVoteMessage("u3", "2", true)
	a.onVoteMessage("u2", "2", true)     // передумав
	a.onVoteMessage("u4", "12", true)    // не голос
	a.onVoteMessage("u5", "lol 1", true) // не голос
	a.onVoteMessage("u6", "1", false)    // без підписки — не рахується
	if v := a.st.Vote; v.Yes != 1 || v.No != 2 {
		t.Fatalf("votes %+v", v)
	}
	a.mu.Lock()
	a.st.Vote.EndsAt = time.Now().UnixMilli() - 1
	msg = a.tickGameLocked(time.Now().UnixMilli())
	a.mu.Unlock()
	if a.st.Vote.Result != "next" || a.st.Phase != "collect" {
		t.Fatal("vote result next", msg, a.st.Vote.Result, a.st.Phase)
	}
	// продовження за голосуванням — новий раунд гри
	a.mu.Lock()
	a.st.Phase, a.st.Game.Status = "play", "finished"
	a.mu.Unlock()
	a.StartVote()
	a.onVoteMessage("u1", "1", true)
	s = a.FinishVote()
	if s.Vote.Result != "continue" || s.Phase != "play" || s.Game.Status != "running" || s.Round != 2 {
		t.Fatalf("continue: %+v phase=%s round=%d", s.Vote, s.Phase, s.Round)
	}
	// нічия — вирішує стрімер
	a.FinishGame()
	a.StartVote()
	s = a.FinishVote()
	if s.Vote.Result != "tie" || s.Phase != "play" {
		t.Fatal("tie")
	}
	s = a.NextCollection()
	if s.Phase != "collect" {
		t.Fatal("next collection")
	}
	if _, err := a.SetWinner("Hades"); err == nil {
		t.Fatal("second winner for the same collection allowed")
	}
	s = a.StartTimer() // новий збір — переможець і прапорець експорту скидаються
	if s.Winner != "" || s.Exported || s.Phase != "collect" || s.CollN != 2 {
		t.Fatalf("%+v", s.Phase)
	}
}

// Налаштування СабДею «рахувати лише перший голос»: переголосувати в «1 / 2» не можна.
func TestVoteFirstOnly(t *testing.T) {
	a := newTestApp(t)
	a.mu.Lock()
	a.st.Settings.VoteFirst = true
	a.st.Phase, a.st.Game.Status = "play", "finished"
	a.mu.Unlock()
	if _, err := a.StartVote(); err != nil {
		t.Fatal(err)
	}
	a.onVoteMessage("u1", "1", true)
	a.onVoteMessage("u1", "2", true) // переголосування ігнорується
	a.onVoteMessage("u2", "2", true)
	if v := a.st.Vote; v.Yes != 1 || v.No != 1 {
		t.Fatalf("votes %+v", v)
	}
}

// Свій звук кінця гри: лише https-посилання; інше відкидається (грає вбудований сигнал).
func TestGameSoundURL(t *testing.T) {
	for in, want := range map[string]string{
		" https://www.myinstants.com/media/sounds/x.mp3 ": "https://www.myinstants.com/media/sounds/x.mp3",
		"http://example.com/a.mp3":                        "",
		"javascript:alert(1)":                             "",
		"":                                                "",
	} {
		s := Settings{GameSoundURL: in}
		s.normalize()
		if s.GameSoundURL != want {
			t.Fatalf("%q → %q, want %q", in, s.GameSoundURL, want)
		}
	}
}
