package main

// Додаткові перевірки логіки СабДею й аукціону (QA-прохід).

import (
	"math/rand"
	"testing"
	"time"
)

// СабДей: замовлення лише під час збору, лише з потрібної нагороди, з текстом, без дублів; видалення; налаштування.
func TestSubdayRedemptionRules(t *testing.T) {
	a := newTestApp(t)
	a.SelectReward("r1", "СабДей")
	red := func(id, user, text string) Redemption {
		ev := Redemption{ID: id, UserID: user, UserName: user, UserLogin: user, UserInput: text}
		ev.Reward.ID, ev.Reward.Title = "r1", "СабДей"
		return ev
	}
	a.StartTimer()
	a.onRedemption(red("1", "u1", "Hades"), User{})
	a.onRedemption(red("1", "u1", "Hades"), User{}) // та сама подія Twitch вдруге
	a.onRedemption(red("2", "u2", "   "), User{})   // порожній текст
	if len(a.st.Entries) != 1 {
		t.Fatalf("entries %d, want 1", len(a.st.Entries))
	}
	a.PauseTimer()
	a.onRedemption(red("3", "u3", "Celeste"), User{}) // пауза — не записується
	if len(a.st.Entries) != 1 {
		t.Fatal("recorded during pause")
	}
	a.StartTimer()
	a.onRedemption(red("4", "u3", "Celeste"), User{})
	a.DeleteEntry(a.st.Entries[0].ID)
	if len(a.st.Entries) != 1 || a.st.Entries[0].Text != "Celeste" {
		t.Fatalf("delete %+v", a.st.Entries)
	}
	// налаштування: тривалість збору завжди 10 хв, правила балів фіксовані, нагорода не губиться
	s := a.st.Settings
	s.DurationMin, s.NonSubPoints, s.Mult = 1, 5, map[string]int{"1": 9}
	a.SaveSettings(s)
	if st := a.st.Settings; st.DurationMin != 10 || st.NonSubPoints != 0 || st.Mult["1"] != 1 || st.Mult["3"] != 3 || st.RewardID != "r1" {
		t.Fatalf("settings %+v", st)
	}
}

// Гра переможця: «+1 хв» голосуванню, «Продовжити гру без голосування».
func TestVoteAddTimeAndContinue(t *testing.T) {
	a := newTestApp(t)
	a.StartTimer()
	a.AddManual("Phill", 1, 2, "Hades")
	a.FinishTimer()
	if _, err := a.ExportMarbles(false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetWinner("Hades"); err != nil {
		t.Fatal(err)
	}
	a.FinishGame()
	if _, err := a.StartVote(); err != nil {
		t.Fatal(err)
	}
	before := a.st.Vote.EndsAt
	a.VoteAddTime(60000)
	if a.st.Vote.EndsAt-before != 60000 {
		t.Fatal("vote +1 min")
	}
	a.FinishVote() // нічия 0:0
	s := a.ContinueGame()
	if s.Phase != "play" || s.Game.Status != "running" || s.Round != 2 {
		t.Fatalf("continue: phase=%s game=%s round=%d", s.Phase, s.Game.Status, s.Round)
	}
}

// Аукціон: таймер прийому ставок (пауза, ±1 хв, кінець часу), ставки після кінця не приймаються, видалення лота.
func TestAuctionTimerAndRemove(t *testing.T) {
	a := newTestApp(t)
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}, DurationMin: 3})
	a.AuctionStart()
	au := &a.st.Auction
	if d := au.Timer.EndsAt - au.Timer.StartedAt; d != 3*60000 {
		t.Fatalf("duration %d", d)
	}
	a.AuctionAddTime(60000)
	a.AuctionAddTime(-30000)
	a.AuctionPause()
	if au.Timer.Status != "paused" || au.Timer.RemainingMs < 3*60000+29000 {
		t.Fatalf("pause %+v", au.Timer)
	}
	a.onRedemption(bid("p", "q", "Квиток", "u1", "Hades", 1000), User{}) // пауза — ставка не йде
	if len(au.Lots) != 0 {
		t.Fatal("bid during pause")
	}
	a.AuctionStart()
	a.onRedemption(bid("1", "q", "Квиток", "u1", "Hades", 1000), User{})
	a.onRedemption(bid("2", "q", "Квиток", "u2", "Celeste", 1000), User{})
	// час вийшов (як у ticker)
	a.mu.Lock()
	au.Timer.EndsAt = time.Now().UnixMilli() - 1
	ended := a.tickAuctionLocked(time.Now().UnixMilli())
	a.mu.Unlock()
	if !ended || au.Timer.Status != "finished" {
		t.Fatal("timer must finish")
	}
	a.onRedemption(bid("3", "q", "Квиток", "u3", "Doom", 1000), User{})
	if len(au.Lots) != 2 {
		t.Fatal("bid after finish accepted")
	}
	a.AuctionRemoveLot(au.Lots[0].ID)
	if len(au.Lots) != 1 || au.Lots[0].Name != "Celeste" {
		t.Fatalf("remove %+v", au.Lots)
	}
}

// Стан переживає перезапуск: аукціон, стрім і збори відновлюються; таймер, що вийшов, поки програма була закрита, — завершений.
func TestStateSurvivesRestart(t *testing.T) {
	a := newTestApp(t)
	a.StartTimer()
	a.AddManual("Phill", 1, 1, "Hades")
	a.FinishTimer()
	a.StartTimer() // збір 2
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}, SpinGapSec: 7})
	a.AuctionStart()
	a.onRedemption(bid("1", "q", "Квиток", "u1", "Hades", 1000), User{})
	a.mu.Lock()
	a.st.Auction.Timer.EndsAt = time.Now().UnixMilli() - 1000 // «вимкнули» програму, поки йшов таймер
	a.saveLocked()
	a.mu.Unlock()
	st := loadState(a.stateFile)
	if st.Stream.ID != a.st.Stream.ID || len(st.Past) != 1 || st.CollN != 2 {
		t.Fatalf("stream not restored: %s past=%d n=%d", st.Stream.ID, len(st.Past), st.CollN)
	}
	if len(st.Auction.Lots) != 1 || st.Auction.Cfg.SpinGapSec != 7 || st.Auction.Timer.Status != "finished" {
		t.Fatalf("auction not restored: %+v", st.Auction)
	}
}

// Наскрізна статистика: справжні оберти AuctionSpin «на вибування» до переможця, тисячі аукціонів.
// Частота перемог має збігатися з сума/загальна сума (у межах 4 стандартних похибок).
func TestElimSpinStatistics(t *testing.T) {
	a := newTestApp(t)
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}})
	a.AuctionStart()
	amounts := []int{1000, 3000, 6000, 10000}
	names := []string{"Elden Ring", "Hades", "Celeste", "Outer Wilds"} // різні назви — 4 окремі лоти
	for i, x := range amounts {
		a.onRedemption(bid(names[i], "q", "Квиток", "u"+names[i], names[i], x), User{})
	}
	if len(a.st.Auction.Lots) != 4 {
		t.Fatalf("lots %d", len(a.st.Auction.Lots))
	}
	a.AuctionFinish()
	au := &a.st.Auction
	rng := rand.New(rand.NewSource(7))
	old := randomUnit
	randomUnit = func() (float64, string) { return rng.Float64(), "test" }
	defer func() { randomUnit = old }()
	const N = 2500
	wins := map[string]int{}
	for k := 0; k < N; k++ {
		for au.Winner == "" {
			if _, err := a.AuctionSpin(); err != nil {
				t.Fatal(err)
			}
		}
		for _, l := range au.Lots {
			if l.ID == au.Winner {
				wins[l.Name]++
			}
		}
		a.AuctionResetSpins()
	}
	total := 0
	for _, x := range amounts {
		total += x
	}
	for i, x := range amounts {
		p := float64(x) / float64(total)
		got := float64(wins[names[i]]) / N
		se := 4 * sqrt(p*(1-p)/N)
		if got < p-se || got > p+se {
			t.Errorf("lot %d (%d): won %.3f, want %.3f ± %.3f", i, x, got, p, se)
		}
		t.Logf("лот %5d: очікувано %.3f, вийшло %.3f", x, p, got)
	}
}

func sqrt(x float64) float64 {
	z := x
	for i := 0; i < 30 && z > 0; i++ {
		z = (z + x/z) / 2
	}
	return z
}
