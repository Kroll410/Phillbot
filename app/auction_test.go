package main

import "testing"

func bid(id, rid, title, user, text string, cost int) Redemption {
	ev := Redemption{ID: id, UserID: user, UserName: user, UserLogin: user, UserInput: text}
	ev.Reward.ID, ev.Reward.Title, ev.Reward.Cost = rid, title, cost
	return ev
}

func TestAuctionFlow(t *testing.T) {
	a := newTestApp(t)
	// нагороди за замовчуванням підхоплюються зі списку каналу
	a.mu.Lock()
	a.autoAuctionRewardsLocked([]Reward{{ID: "m", Title: "Мегаквиток"}, {ID: "x", Title: "Інше"}, {ID: "q", Title: "Квиток"}, {ID: "s", Title: "Середній  квиток"}})
	ids := append([]string(nil), a.st.Auction.Cfg.RewardIDs...)
	a.mu.Unlock()
	if len(ids) != 3 || ids[0] != "q" || ids[1] != "s" || ids[2] != "m" {
		t.Fatalf("default rewards %v", ids)
	}
	// не йде таймер — ставка не рахується
	a.onRedemption(bid("1", "q", "Квиток", "u1", "Elden Ring", 1000), User{})
	if len(a.st.Auction.Lots) != 0 {
		t.Fatal("bid accepted while idle")
	}
	if a.AuctionStart(); a.st.Auction.Timer.Status != "running" || a.st.Auction.Timer.EndsAt-a.st.Auction.Timer.StartedAt != 600000 {
		t.Fatal("timer 10 min")
	}
	a.onRedemption(bid("2", "q", "Квиток", "u1", "Elden Ring", 1000), User{})
	a.onRedemption(bid("3", "m", "Мегаквиток", "u2", "elden  ring", 10000), User{})   // та сама назва → додати
	a.onRedemption(bid("3", "m", "Мегаквиток", "u2", "elden  ring", 10000), User{})   // дубль події
	a.onRedemption(bid("4", "s", "Середній квиток", "u3", "Elden Rng", 5000), User{}) // схоже → питання
	a.onRedemption(bid("5", "s", "Середній квиток", "u3", "Hades", 5000), User{})
	au := &a.st.Auction
	if len(au.Lots) != 2 || au.Lots[0].Amount != 11000 || au.Lots[0].Bids != 2 {
		t.Fatalf("lots %+v", au.Lots)
	}
	pend := au.Feed[1]
	if pend.Status != "pending" || pend.SuggestID != au.Lots[0].ID {
		t.Fatalf("pending %+v", pend)
	}
	if _, err := a.AuctionResolve(pend.ID, true); err != nil || au.Lots[0].Amount != 16000 {
		t.Fatalf("resolve %v %+v", err, au.Lots[0])
	}
	if _, err := a.AuctionResolve(pend.ID, true); err == nil {
		t.Fatal("double resolve")
	}
	// колесо на вибування: 3 лоти → 2 оберти до переможця
	a.onRedemption(bid("6", "q", "Квиток", "u4", "Celeste", 1000), User{})
	randomUnit = func() (float64, string) { return 0.999, "test" }
	if _, err := a.AuctionSpin(); err != nil || au.Winner != "" || au.Spin == nil || au.Spin.Final {
		t.Fatalf("spin1 %v %+v", err, au.Spin)
	}
	out1 := au.Spin.Pick
	a.AuctionSpin()
	if au.Winner == "" || !au.Spin.Final || au.Winner == out1 || au.Winner == au.Spin.Pick {
		t.Fatalf("spin2 winner=%s %+v", au.Winner, au.Spin)
	}
	if _, err := a.AuctionSpin(); err == nil {
		t.Fatal("spin after winner")
	}
	// одразу результат: pos 0 → перший лот
	a.AuctionResetSpins()
	c := au.Cfg
	c.SpinMode = "instant"
	a.AuctionSaveSettings(c)
	randomUnit = func() (float64, string) { return 0, "test" }
	a.AuctionSpin()
	if au.Winner != au.Lots[0].ID {
		t.Fatalf("instant %s", au.Winner)
	}
	// до 5 нагород; вибір стрімера вимикає автовибір
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"1", "2", "3", "4", "5", "6"}})
	if len(au.Cfg.RewardIDs) != 5 || !au.Cfg.RewardsSet || au.Cfg.SpinMode != "elim" || au.Cfg.DurationMin != 10 {
		t.Fatalf("cfg %+v", au.Cfg)
	}
	if a.isAuctionReward("zz", "Квиток") {
		t.Fatal("auto after manual choice")
	}
	a.AuctionNew()
	if len(au.Lots) != 0 || au.Timer.Status != "idle" || len(au.Cfg.RewardIDs) != 5 {
		t.Fatal("new")
	}
}

func TestSimilarity(t *testing.T) {
	for _, c := range []struct {
		a, b string
		sim  bool
	}{{"elden ring", "elden rng", true}, {"dark souls 3", "dark souls 2", true}, {"hades", "celeste", false}, {"gta", "gta 5", true}, {"doom", "dota 2", false}, {"elden ring", "elden ring dlc", true}} {
		if got := similarity(c.a, c.b) >= similarThreshold; got != c.sim {
			t.Errorf("%q~%q = %v (%.2f)", c.a, c.b, got, similarity(c.a, c.b))
		}
	}
}

func TestAuctionMerge(t *testing.T) {
	a := newTestApp(t)
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}})
	a.AuctionStart()
	a.onRedemption(bid("1", "q", "Квиток", "u1", "Elden Ring", 1000), User{})
	a.onRedemption(bid("2", "q", "Квиток", "u2", "Elden Ring", 1000), User{})
	a.onRedemption(bid("3", "q", "Квиток", "u1", "Shadow of Erdtree", 5000), User{})
	a.onRedemption(bid("4", "q", "Квиток", "u3", "Shadow of Erdtre", 1000), User{}) // схоже → питання
	au := &a.st.Auction
	er, eld := au.Lots[1].ID, au.Lots[0].ID
	a.AuctionMergeLots(er, eld)
	if len(au.Lots) != 1 || au.Lots[0].Amount != 7000 || au.Lots[0].Bids != 3 || len(au.Lots[0].Users) != 2 {
		t.Fatalf("merged %+v", au.Lots)
	}
	if au.Feed[0].Status != "pending" || au.Feed[0].SuggestID != eld || au.Feed[0].SuggestName != "Elden Ring" {
		t.Fatalf("pending suggestion %+v", au.Feed[0])
	}
	if au.Feed[1].LotID != eld {
		t.Fatal("feed lot id")
	}
}

func TestAuctionUndoMerge(t *testing.T) {
	a := newTestApp(t)
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}})
	a.AuctionStart()
	a.onRedemption(bid("1", "q", "Квиток", "u1", "Elden Ring", 1000), User{})
	a.onRedemption(bid("2", "q", "Квиток", "u2", "Hades", 1000), User{})
	a.onRedemption(bid("3", "q", "Квиток", "u1", "Celeste", 5000), User{})
	au := &a.st.Auction
	el, cel := au.Lots[0].ID, au.Lots[2].ID
	a.AuctionMergeLots(cel, el)
	a.onRedemption(bid("4", "q", "Квиток", "u3", "Elden Ring", 1000), User{}) // ставка вже після об'єднання
	if au.Lots[0].Amount != 7000 || len(au.Merges) != 1 {
		t.Fatalf("merge %+v", au.Lots)
	}
	if _, err := a.AuctionUndoMerge(); err != nil {
		t.Fatal(err)
	}
	if len(au.Lots) != 3 || au.Lots[2].ID != cel || au.Lots[2].Amount != 5000 || au.Lots[0].Amount != 2000 || au.Lots[0].Bids != 2 {
		t.Fatalf("undo %+v", au.Lots)
	}
	if len(au.Lots[0].Users) != 2 || au.Lots[0].Users[0].Amount != 1000 {
		t.Fatalf("users %+v", au.Lots[0].Users)
	}
	for _, b := range au.Feed {
		if b.Text == "Celeste" && b.LotID != cel {
			t.Fatal("feed not restored")
		}
	}
	if _, err := a.AuctionUndoMerge(); err == nil {
		t.Fatal("nothing to undo")
	}
}

func TestAuctionHiddenLot(t *testing.T) {
	a := newTestApp(t)
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}, SpinMode: "instant"})
	a.AuctionStart()
	a.onRedemption(bid("1", "q", "Квиток", "u1", "Elden Ring", 1000), User{})
	a.onRedemption(bid("2", "q", "Квиток", "u2", "Hades", 1000), User{})
	au := &a.st.Auction
	a.AuctionToggleLot(au.Lots[0].ID)
	a.AuctionFinish()
	randomUnit = func() (float64, string) { return 0, "test" }
	a.AuctionSpin()
	if au.Winner != au.Lots[1].ID || len(au.Spin.IDs) != 1 {
		t.Fatalf("hidden lot on the wheel: %+v", au.Spin)
	}
	a.AuctionToggleLot(au.Lots[0].ID)
	if au.Lots[0].Hidden {
		t.Fatal("toggle back")
	}
}

func TestAuctionEditAndWinnerLock(t *testing.T) {
	a := newTestApp(t)
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}, SpinMode: "instant"})
	a.AuctionStart()
	a.onRedemption(bid("1", "q", "Квиток", "u1", "Elden Ring", 1000), User{})
	a.onRedemption(bid("2", "q", "Квиток", "u2", "Hades", 1000), User{})
	au := &a.st.Auction
	id := au.Lots[1].ID
	if _, err := a.AuctionUpdateLot(id, "  Hades II ", -1); err != nil || au.Lots[1].Name != "Hades II" || au.Lots[1].Key != "hades ii" || au.Lots[1].Amount != 1000 {
		t.Fatalf("rename %v %+v", err, au.Lots[1])
	}
	a.AuctionUpdateLot(id, "", 2500)
	if au.Lots[1].Name != "Hades II" || au.Lots[1].Amount != 2500 {
		t.Fatalf("amount %+v", au.Lots[1])
	}
	a.onRedemption(bid("3", "q", "Квиток", "u3", "hades ii", 1000), User{}) // нова назва — ставка йде в цей лот
	if au.Lots[1].Amount != 3500 {
		t.Fatalf("bid to renamed lot %+v", au.Lots)
	}
	a.AuctionFinish()
	randomUnit = func() (float64, string) { return 0, "test" }
	a.AuctionSpin()
	if au.Winner == "" {
		t.Fatal("no winner")
	}
	if _, err := a.AuctionMergeLots(au.Lots[1].ID, au.Lots[0].ID); err == nil || len(au.Lots) != 2 {
		t.Fatal("merge allowed after winner")
	}
}
