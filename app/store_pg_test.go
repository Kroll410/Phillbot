//go:build server

package main

import (
	"os"
	"strconv"
	"testing"
)

// Перевірка на справжньому PostgreSQL: PHILLBOT_TEST_PG=postgres://postgres:pw@127.0.0.1:55432/postgres?sslmode=disable
// Без змінної тест пропускається. Тест працює у власній схемі, яку прибирає наприкінці.
func openTestPG(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("PHILLBOT_TEST_PG")
	if url == "" {
		t.Skip("PHILLBOT_TEST_PG не задано")
	}
	root, err := openDB(url, "")
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "t_" + randomHex(6)
	if _, err := root.db.Exec(`CREATE SCHEMA ` + schemaName); err != nil {
		t.Fatal(err)
	}
	root.Close()
	sep := "?"
	if hasQuery(url) {
		sep = "&"
	}
	s, err := openDB(url+sep+"search_path="+schemaName, "chan1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.db.Exec(`DROP SCHEMA ` + schemaName + ` CASCADE`)
		s.Close()
	})
	return s
}

func hasQuery(u string) bool {
	for i := range u {
		if u[i] == '?' {
			return true
		}
	}
	return false
}

func TestPostgresStore(t *testing.T) {
	s := openTestPG(t)
	if !s.pg || s.rb(`a = ? AND b = ?`) != `a = $1 AND b = $2` {
		t.Fatal("діалект Postgres не увімкнено")
	}

	st := defaultState()
	st.Stream = StreamInfo{ID: "s1", StartedAt: 1700000000000}
	st.Past = []Collection{{N: 1, StartedAt: 1, FinishedAt: 2, Winner: "Hades", Settings: st.Settings,
		Entries: []Entry{{ID: "p1", Ts: 5, Login: "a", Text: "Hades"}, {ID: "p2", Ts: 4, Login: "b", Text: "Doom"}}}}
	for i := 0; i < 5; i++ { // ts спадає, id не за алфавітом — порядок має бути саме вставки
		st.Entries = append(st.Entries, Entry{ID: "e" + strconv.Itoa(9-i), Ts: int64(100 - i), Login: "u", Text: "g" + strconv.Itoa(i)})
	}
	st.SeenRedeems = []string{"r9", "r1", "r5"}
	st.DonSeen = []string{"d1"}
	st.Auction.ID = ""
	st.Auction.Timer.StartedAt = 1700000000000
	st.Auction.Lots = []Lot{{ID: "l2", Name: "B", Amount: 3}, {ID: "l1", Name: "A", Amount: 5_000_000_000}}
	st.Auction.Feed = []Bid{{ID: "b3", Ts: 30, Amount: 1}, {ID: "b1", Ts: 10, Amount: 2}, {ID: "b2", Ts: 10, Amount: 3}}
	st.Auction.SeenIDs = []string{"b3", "b1", "b2"}
	if err := s.Sync(&st); err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(&st); err != nil { // повторний Sync нічого не ламає
		t.Fatal(err)
	}
	aid := st.Auction.ID
	if aid == "" {
		t.Fatal("id аукціону не створено")
	}

	// зміни: правка, видалення, нова ставка
	st.Entries[1].Text = "edited"
	st.Entries = append(st.Entries[:2], st.Entries[3:]...)
	st.Auction.Feed = append(st.Auction.Feed, Bid{ID: "b4", Ts: 40})
	st.SeenRedeems = []string{"r9", "r1", "r5", "r7"}
	if err := s.Sync(&st); err != nil {
		t.Fatal(err)
	}

	// новий Store (як після перезапуску) бачить те саме й у тому ж порядку
	s2 := s.forChannel("chan1")
	got, ok, err := s2.Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	ids := func() (r string) {
		for _, e := range got.Entries {
			r += e.ID + ","
		}
		return
	}()
	if ids != "e9,e8,e6,e5," || got.Entries[1].Text != "edited" {
		t.Fatalf("порядок/зміст замовлень: %s %+v", ids, got.Entries)
	}
	if len(got.SeenRedeems) != 4 || got.SeenRedeems[0] != "r9" || got.SeenRedeems[3] != "r7" || len(got.DonSeen) != 1 {
		t.Fatalf("seen: %v %v", got.SeenRedeems, got.DonSeen)
	}
	if got.Auction.ID != aid || len(got.Auction.Lots) != 2 || got.Auction.Lots[0].ID != "l2" || got.Auction.Lots[1].Amount != 5_000_000_000 {
		t.Fatalf("лоти: %+v", got.Auction.Lots)
	}
	feed := ""
	for _, b := range got.Auction.Feed { // найновіші першими; за однакового ts — пізніше вставлена першою
		feed += b.ID + ","
	}
	if feed != "b4,b3,b2,b1," {
		t.Fatalf("стрічка: %s", feed)
	}

	f, err := s2.streamFile("s1")
	if err != nil || f.StartedAt != 1700000000000 || len(f.Collections) != 1 || f.Collections[0].Winner != "Hades" ||
		len(f.Collections[0].Entries) != 2 || f.Collections[0].Entries[0].ID != "p1" {
		t.Fatalf("streamFile: %v %+v", err, f)
	}
	if !s2.streamExists("s1") || s2.streamExists("nope") {
		t.Fatal("streamExists")
	}
	if m := s2.streamMetas(); len(m) != 1 || m[0].Collections != 1 || m[0].Orders != 2 {
		t.Fatalf("streamMetas: %+v", m)
	}
	if all := s2.allAuctions(); len(all) != 1 || len(all[0].Lots) != 2 || len(all[0].Feed) != 4 {
		t.Fatalf("allAuctions: %+v", all)
	}
	if cs := s2.allCollections(); len(cs) != 1 {
		t.Fatalf("allCollections: %d", len(cs))
	}

	// інший канал у тій самій БД не бачить чужого
	if _, ok, _ := s.forChannel("chan2").Load(); ok {
		t.Fatal("канал chan2 бачить чужі дані")
	}
	if err := s.setMeta("k", "v1"); err != nil || s.setMeta("k", "v2") != nil || s.meta("k") != "v2" {
		t.Fatal("meta")
	}
}

func TestPostgresAuth(t *testing.T) {
	s := openTestPG(t)
	a, err := newAuthStore(s, "k-0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.allow("@Phill_Bot", "admin", "test"); err != nil {
		t.Fatal(err)
	}
	if err := a.allow("phill_bot", "streamer", "test"); err != nil { // роль admin не знижується
		t.Fatal(err)
	}
	if u, ok := a.user("phill_bot"); !ok || u.Role != "admin" || !u.Allowed {
		t.Fatalf("user: %+v %v", u, ok)
	}
	if _, ok := a.loginOK(User{ID: "1", Login: "stranger", DisplayName: "S"}); ok {
		t.Fatal("чужого пустили")
	}
	wu, ok := a.loginOK(User{ID: "42", Login: "phill_bot", DisplayName: "Phill"})
	if !ok || wu.TwitchID != "42" || wu.LastLoginAt == 0 {
		t.Fatalf("loginOK: %+v %v", wu, ok)
	}
	tok, err := a.newSession("phill_bot")
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := a.session(tok); !ok || u.Login != "phill_bot" {
		t.Fatal("сесія")
	}
	if err := a.saveTokens("42", &Tokens{AccessToken: "acc", RefreshToken: "ref"}); err != nil {
		t.Fatal(err)
	}
	if err := a.saveTokens("42", &Tokens{AccessToken: "acc2", RefreshToken: "ref"}); err != nil {
		t.Fatal(err)
	}
	if tk := a.loadTokens("42"); tk == nil || tk.AccessToken != "acc2" {
		t.Fatalf("токени: %+v", tk)
	}
	if ids := a.channelsWithTokens(); len(ids) != 1 || ids[0] != "42" {
		t.Fatalf("channelsWithTokens: %v", ids)
	}
	if err := s.setMeta("widget_secret:42", "sec"); err != nil {
		t.Fatal(err)
	}
	if a.channelBySecret("widget_secret", "sec") != "42" || a.channelBySecret("widget_secret", "x") != "" {
		t.Fatal("channelBySecret")
	}
	if us, err := a.users(); err != nil || len(us) != 1 {
		t.Fatalf("users: %v %v", us, err)
	}
	if err := a.revoke("phill_bot", "test"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.session(tok); ok {
		t.Fatal("сесія після revoke")
	}
	a.pruneSessions()
	a.endSession(tok)
}
