package main

import (
	"os"
	"path/filepath"
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
	if err != nil || p == "" || len(a.st.Entries) != 0 {
		t.Fatal(p, err)
	}
}

func TestExport(t *testing.T) {
	a := newTestApp(t)
	a.st.Settings.Encoding = "utf16"
	r, err := a.ExportMarbles([]string{"Ой, \"так\"", "B"}, false)
	if err != nil || r.Lines != 2 {
		t.Fatal(r, err)
	}
	b, _ := os.ReadFile(r.Path)
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatal("bom")
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
