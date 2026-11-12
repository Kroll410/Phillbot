package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDonPointsMath(t *testing.T) {
	for _, c := range []struct {
		uah  float64
		per  int
		want int
	}{{120, 30, 4}, {59, 30, 1}, {29.99, 30, 0}, {30, 30, 1}, {100, 25, 4}, {90, 0, 3}} {
		if got := subdayDonPoints(c.uah, c.per); got != c.want {
			t.Errorf("subday %.2f/%d = %d, want %d", c.uah, c.per, got, c.want)
		}
	}
	for _, c := range []struct {
		uah  float64
		per  int
		want int
	}{{10, 10, 1000}, {100, 10, 10000}, {15, 10, 1000}, {9, 10, 0}, {50, 25, 2000}} {
		if got := auctionDonPoints(c.uah, c.per); got != c.want {
			t.Errorf("auction %.2f/%d = %d, want %d", c.uah, c.per, got, c.want)
		}
	}
	if donAmount("100") != 100 || donAmount("12,5") != 12.5 || donAmount("x") != 0 {
		t.Fatal("amount parse")
	}
}

func don(id, name, msg, amount string) Donate {
	return Donate{PubID: id, ClientName: name, Message: msg, Amount: amount, Currency: "UAH"}
}

func TestDonatesToSubdayAndAuction(t *testing.T) {
	a := newTestApp(t)
	// перше підключення: історія лише запам'ятовується
	a.StartTimer()
	a.handleDonates([]Donate{don("old2", "A", "Hades", "300"), don("old1", "B", "Doom", "300")})
	if len(a.st.Entries) != 0 || !a.st.DonInit || len(a.st.DonSeen) != 2 {
		t.Fatalf("baseline: entries=%d init=%v seen=%v", len(a.st.Entries), a.st.DonInit, a.st.DonSeen)
	}
	// СабДей: збір іде — донат стає замовленням з балами ⌊грн/30⌋, той самий донат удруге не рахується
	a.AddManual("Phill", 1, 2, "Hades") // підписник T1 × 2 міс. = 2 бали
	list := []Donate{don("d2", "Богдан", "hades", "120"), don("d1", "Богдан", "Hades", "59"), don("old2", "A", "Hades", "300")}
	a.handleDonates(list)
	a.handleDonates(list)
	var donE []Entry
	for _, e := range a.st.Entries {
		if e.Donation {
			donE = append(donE, e)
		}
	}
	if len(donE) != 2 || donE[0].DonPoints != 1 || donE[1].DonPoints != 4 || donE[0].UAH != 59 {
		t.Fatalf("subday donations %+v", donE)
	}
	g := aggregate(a.st.Settings, a.st.Entries)
	if len(g) != 1 || g[0].Points != 2+1+4 { // донати від одного донатера не «з'їдають» одне одного
		t.Fatalf("group %+v", g)
	}
	// не гривні, без тексту — не враховуються
	a.handleDonates([]Donate{{PubID: "usd", ClientName: "X", Message: "Doom", Amount: "5", Currency: "USD"}, don("empty", "Y", "  ", "90")})
	if n := len(a.st.Entries); n != 3 {
		t.Fatalf("entries %d", n)
	}
	// аукціон: приймає ставки — донат стає ставкою ⌊грн/10⌋×1000
	a.FinishTimer()
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}})
	a.AuctionStart()
	a.onRedemption(bid("r1", "q", "Квиток", "u1", "Elden Ring", 1000), User{})
	a.handleDonates([]Donate{don("a1", "Ira", "elden ring", "100"), don("a2", "Max", "Celeste", "5")})
	au := &a.st.Auction
	if len(au.Lots) != 1 || au.Lots[0].Amount != 11000 || au.Feed[0].Status != "added" || !au.Feed[0].Donation || au.Feed[0].UAH != 100 {
		t.Fatalf("auction %+v feed %+v", au.Lots, au.Feed)
	}
	// нічого не йде — донат лише в журнал
	a.AuctionFinish()
	before := len(a.st.Entries)
	a.handleDonates([]Donate{don("late", "Z", "Doom", "300")})
	if len(a.st.Entries) != before || len(au.Lots) != 1 {
		t.Fatal("donation applied while nothing runs")
	}
}

// опитування живого API підміняється тестовим сервером: статус «on», нікнейм, донати зчитуються
func TestDonatelloLoop(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "good" {
			w.WriteHeader(401)
			return
		}
		calls++
		switch r.URL.Path {
		case "/me":
			json.NewEncoder(w).Encode(map[string]any{"nickname": "PhillHess"})
		case "/donates":
			json.NewEncoder(w).Encode(map[string]any{"content": []Donate{don("x1", "A", "Hades", "60")}})
		}
	}))
	defer srv.Close()
	old := donatelloBase
	donatelloBase = srv.URL + "/"
	defer func() { donatelloBase = old }()

	a := newTestApp(t)
	a.st.Settings.DonToken = "good"
	a.restartDonatello()
	for i := 0; i < 50 && !(a.don.State == "on" && a.st.DonInit); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	a.mu.Lock()
	st, nick, init := a.don.State, a.don.Nickname, a.st.DonInit
	a.donCancel()
	a.mu.Unlock()
	if st != "on" || nick != "PhillHess" || !init || calls < 2 {
		t.Fatalf("loop: %s %s init=%v calls=%d", st, nick, init, calls)
	}
	if _, err := a.TestDonatello("bad"); err == nil {
		t.Fatal("bad token accepted")
	}
	if n, err := a.TestDonatello("good"); err != nil || n != "PhillHess" {
		t.Fatal(n, err)
	}
}
