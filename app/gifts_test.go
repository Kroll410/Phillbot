//go:build !server

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGiftSubsPoints(t *testing.T) {
	a := newTestApp(t)
	a.SelectReward("r1", "СабДей")
	a.StartTimer()
	order := func(id, user, text string) {
		ev := Redemption{ID: id, UserID: user, UserName: user, UserLogin: user, UserInput: text}
		ev.Reward.ID = "r1"
		a.onRedemption(ev, User{})
	}
	// список активних підписок: mira подарувала 3, ira — 1
	a.applyGiftSnapshot(map[string]int{"mira": 3, "ira": 1})
	order("1", "mira", "Hades") // не-підписник (без Helix) — 0 балів + 1 за 3 подарунки
	if e := a.st.Entries[0]; e.Gifts != 3 || pointsOf(a.st.Settings, e) != 1 {
		t.Fatalf("snapshot gifts: %+v", e)
	}
	// живий журнал більший за список — береться він; старі (>30 днів) не рахуються; анонімні — теж
	a.mu.Lock()
	a.st.Gifts = append(a.st.Gifts, GiftEvent{Ts: time.Now().Add(-40 * 24 * time.Hour).UnixMilli(), GifterID: "mira", Count: 50})
	a.mu.Unlock()
	a.onGift(giftEvent{UserID: "mira", UserLogin: "mira", UserName: "Mira", Total: 5})
	a.onGift(giftEvent{UserID: "anon", Total: 10, IsAnonymous: true})
	if g := a.st.Entries[0].Gifts; g != 5 {
		t.Fatalf("live gifts in last 30 days: %d, want 5", g)
	}
	if len(a.st.Gifts) != 1 {
		t.Fatalf("old gifts not pruned: %+v", a.st.Gifts)
	}
	// підписник T2 × 3 міс. / 3 = 2; 1 подарунок — менше двох, бал не додає
	a.mu.Lock()
	a.st.Entries = append(a.st.Entries, Entry{ID: "x", Ts: time.Now().UnixMilli(), User: "ira", Login: "ira", UserID: "ira", Tier: 2, Months: 3, Text: "Doom"})
	a.refreshEntryGiftsLocked()
	a.mu.Unlock()
	if p := pointsOf(a.st.Settings, a.st.Entries[1]); p != 2 {
		t.Fatalf("sub + 1 gift = %d, want 2", p)
	}
	for gifts, want := range map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 5: 2, 6: 3, 10: 5} { // 2 сабки = 1 бал, вниз
		if g := giftPoints(gifts); g != want {
			t.Fatalf("giftPoints(%d) = %d, want %d", gifts, g, want)
		}
	}
	// «ділити»: бали разом із подарунками діляться між замовленнями глядача
	s := a.st.Settings
	s.DupMode = "split"
	es := []Entry{
		{ID: "a", Ts: 1, UserID: "u", Login: "u", Tier: 1, Months: 2, Gifts: 4, Text: "A"},
		{ID: "b", Ts: 2, UserID: "u", Login: "u", Tier: 1, Months: 2, Gifts: 4, Text: "B"},
	}
	if p := entryPoints(s, es); p["a"]+p["b"] != 3 || p["a"] != 1 || p["b"] != 2 { // (1×2 + 1 за 4 сабки) = 3 на двох
		t.Fatalf("split with gifts %v", p)
	}
	// донати й ручні замовлення подарунків не мають
	a.mu.Lock()
	a.st.Entries = append(a.st.Entries, Entry{ID: "d", Donation: true, DonPoints: 2, UserID: "don:1"})
	a.refreshEntryGiftsLocked()
	a.mu.Unlock()
	if a.st.Entries[2].Gifts != 0 {
		t.Fatal("donation got gifts")
	}
}

func TestGiftSnapshotPaging(t *testing.T) {
	// Helix /subscriptions з двома сторінками: рахуються лише подарунки з відомим дарувальником, без підписки самого стрімера
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Query().Get("broadcaster_id") != "bc" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("after") == "" {
			w.Write([]byte(`{"data":[{"user_id":"v1","gifter_id":"mira","is_gift":true},{"user_id":"v2","gifter_id":"mira","is_gift":true},{"user_id":"v3","is_gift":false}],"pagination":{"cursor":"p2"}}`))
			return
		}
		w.Write([]byte(`{"data":[{"user_id":"bc","is_gift":false},{"user_id":"v4","gifter_id":"ira","is_gift":true},{"user_id":"v5","gifter_id":"","is_gift":true}],"pagination":{}}`))
	}))
	defer srv.Close()
	old := helixBase
	helixBase = srv.URL + "/"
	defer func() { helixBase = old }()
	a := newTestApp(t)
	a.token = &Tokens{AccessToken: "tok"}
	snap, err := a.fetchGiftSnapshot("bc")
	if err != nil || snap["mira"] != 2 || snap["ira"] != 1 || len(snap) != 2 {
		t.Fatalf("snapshot %v %v", snap, err)
	}
}
