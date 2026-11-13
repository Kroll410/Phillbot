package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	// сума в гривнях: actualAmount у UAH має перевагу; донат у валюті без конвертації — не в гривнях
	if v, ok := (Donate{Amount: "5", Currency: "USD", ActualAmount: "205.5", ActualCurrency: "UAH"}).uah(); !ok || v != 205.5 {
		t.Fatal("actual amount", v, ok)
	}
	if v, ok := (Donate{Amount: "100", Currency: "UAH"}).uah(); !ok || v != 100 {
		t.Fatal("uah", v)
	}
	if _, ok := (Donate{Amount: "5", Currency: "EUR"}).uah(); ok {
		t.Fatal("eur accepted")
	}
	for in, want := range map[string]string{" https://abc.ngrok-free.app/ ": "abc.ngrok-free.app", "abc.ngrok.app": "abc.ngrok.app", "": ""} {
		if got := ngrokDomain(in); got != want {
			t.Errorf("domain %q → %q", in, got)
		}
	}
}

func postDon(h http.Handler, key, body string) int {
	r := httptest.NewRequest("POST", "/donatello", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("X-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func donJSON(id, name, msg, amount string) string {
	return `{"pubId":"` + id + `","clientName":"` + name + `","message":"` + msg + `","amount":"` + amount + `","currency":"UAH","actualAmount":"` + amount + `","actualCurrency":"UAH","source":"donatello","createdAt":"1794518423"}`
}

func TestDonatelloCallback(t *testing.T) {
	a := newTestApp(t)
	h := a.donatelloHandler()
	// ключ не вказано — нічого не приймається
	if c := postDon(h, "k", donJSON("x0", "A", "Hades", "300")); c != http.StatusForbidden {
		t.Fatalf("no key configured: %d", c)
	}
	a.st.Settings.DonKey = "15165cf7"
	if c := postDon(h, "wrong", donJSON("x0", "A", "Hades", "300")); c != http.StatusForbidden {
		t.Fatalf("wrong key: %d", c)
	}
	// лише POST /donatello
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/donatello", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal("GET allowed")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/state", nil))
	if w.Code != http.StatusNotFound {
		t.Fatal("other paths must be closed")
	}
	if c := postDon(h, "15165cf7", "{bad"); c != http.StatusBadRequest {
		t.Fatalf("bad json: %d", c)
	}

	// СабДей: збір іде — донат стає замовленням ⌊грн/30⌋; повторний колбек того самого донату не рахується
	a.StartTimer()
	a.AddManual("Phill", 1, 2, "Hades") // T1 × 2 міс. = 2 бали
	for _, b := range []string{donJSON("d1", "Богдан", "Hades", "59"), donJSON("d2", "Богдан", "hades", "120"), donJSON("d2", "Богдан", "hades", "120")} {
		if c := postDon(h, "15165cf7", b); c != http.StatusOK {
			t.Fatalf("callback: %d", c)
		}
	}
	var donE []Entry
	for _, e := range a.st.Entries {
		if e.Donation {
			donE = append(donE, e)
		}
	}
	if len(donE) != 2 || donE[0].DonPoints != 1 || donE[1].DonPoints != 4 || donE[1].UAH != 120 {
		t.Fatalf("subday donations %+v", donE)
	}
	if g := aggregate(a.st.Settings, a.st.Entries); len(g) != 1 || g[0].Points != 2+1+4 {
		t.Fatalf("group %+v", g)
	}
	// без тексту чи не в гривнях — не враховуються
	postDon(h, "15165cf7", `{"pubId":"e1","clientName":"Y","message":"  ","amount":"90","currency":"UAH"}`)
	postDon(h, "15165cf7", `{"pubId":"e2","clientName":"X","message":"Doom","amount":"5","currency":"USD"}`)
	if n := len(a.st.Entries); n != 3 {
		t.Fatalf("entries %d", n)
	}
	// аукціон: приймає ставки — донат стає ставкою ⌊грн/10⌋×1000
	a.FinishTimer()
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}})
	a.AuctionStart()
	a.onRedemption(bid("r1", "q", "Квиток", "u1", "Elden Ring", 1000), User{})
	postDon(h, "15165cf7", donJSON("a1", "Ira", "elden ring", "100"))
	postDon(h, "15165cf7", donJSON("a2", "Max", "Celeste", "5")) // замало для ставки
	au := &a.st.Auction
	if len(au.Lots) != 1 || au.Lots[0].Amount != 11000 || au.Feed[0].Status != "added" || !au.Feed[0].Donation || au.Feed[0].UAH != 100 {
		t.Fatalf("auction %+v feed %+v", au.Lots, au.Feed)
	}
	// нічого не йде — донат лише в журнал
	a.AuctionFinish()
	before := len(a.st.Entries)
	postDon(h, "15165cf7", donJSON("late", "Z", "Doom", "300"))
	if len(a.st.Entries) != before || len(au.Lots) != 1 {
		t.Fatal("donation applied while nothing runs")
	}
}

func TestTunnelOffWithoutToken(t *testing.T) {
	a := newTestApp(t)
	a.restartTunnel()
	if a.don.State != "off" || a.tunStop != nil {
		t.Fatalf("tunnel without token: %+v", a.don)
	}
	if !strings.Contains(ngrokErrText(errString("failed: ERR_NGROK_105 authtoken invalid")), "токен") {
		t.Fatal("authtoken error text")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
