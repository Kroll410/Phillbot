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
	a.st = defaultState()
	return a
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
	// після завершення +1 хв відновлює збір
	a.AddTime(60000)
	if a.st.Session.Status != "running" {
		t.Fatal("resume via +1")
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
	if _, err := os.Stat(a.stateFile); err != nil {
		t.Fatal("state not saved")
	}
	st := loadState(a.stateFile)
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
	p, err := a.NewSession()
	if err != nil || p == "" || len(a.st.Entries) != 0 || len(a.st.Past) != 1 {
		t.Fatal(p, err)
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
	if len(g) != 3 || g[0].Name != "Dark Souls" || g[0].Points != 158 || g[1].Name != "Hades" || g[1].Points != 6 || g[2].Points != 0 {
		t.Fatalf("%+v", g)
	}
	lines := marbleLines(s, es)
	if len(lines) != 164 {
		t.Fatal(len(lines))
	}
	big := append([]Entry{}, es...)
	big = append(big, Entry{ID: "6", Ts: 6, UserID: "u6", User: "z", Tier: 3, Months: 400, Text: "Hades"})
	if n := len(marbleLines(s, big)); n != 164+1200 { // без ліміту: куль = сума балів
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
	a.st.Entries = []Entry{{ID: "1", UserID: "u", User: "U", Tier: 1, Months: 2, Text: "Ой, \"так\""}}
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
	if hs, _ := os.ReadDir(filepath.Join(a.baseDir, "marbles", "history")); len(hs) < 1 {
		t.Fatal("history copies missing")
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
	if len(g) != 2 || g[0].Name != "dark souls 3" || g[0].Points != 5 || g[0].Count != 2 {
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
	files, _ := filepath.Glob(filepath.Join(a.exportsDir, streamID, "*.csv"))
	if len(files) != 1 || filepath.Base(files[0]) != "збір_01.csv" {
		t.Fatal("no csv after finish", files)
	}
	b, _ := os.ReadFile(files[0])
	txt := string(b)
	if !strings.HasPrefix(txt, "\xEF\xBB\xBFsep=;\r\n") || !strings.Contains(txt, "Dark Souls;158;2;") {
		t.Fatalf("%q", txt)
	}
	s := a.AddTime(60000) // +1 хв після завершення — той самий збір
	if s.Session.Status != "running" || len(s.Entries) != 2 {
		t.Fatal("+1 after finish")
	}
	a.FinishTimer()
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
