package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Замовлення пишуться, правляться й видаляються в БД; новий стрім не стирає старий.
func TestStoreEntriesAndStreams(t *testing.T) {
	a := newTestApp(t)
	a.StartTimer()
	a.AddManual("Phill", 3, 2, "Hades")
	s, _ := a.AddManual("Zo", 1, 4, "Celeste")
	a.DeleteEntry(s.Entries[1].ID)
	e := s.Entries[0]
	e.Text = "Hades II"
	a.UpdateEntry(e)
	st := reloadState(t, a)
	if len(st.Entries) != 1 || st.Entries[0].Text != "Hades II" {
		t.Fatalf("entries after edit/delete: %+v", st.Entries)
	}
	a.FinishTimer()
	old := a.st.Stream.ID
	a.NewStream()
	if a.st.Stream.ID == old || len(a.st.Past) != 0 {
		t.Fatal("new stream not started")
	}
	a.StartTimer()
	a.AddManual("Mira", 2, 1, "Doom")
	list := a.ListStreams()
	var prev *StreamMeta
	for i := range list {
		if list[i].ID == old {
			prev = &list[i]
		}
	}
	if len(list) != 2 || prev == nil || prev.Current || prev.Collections != 1 || prev.Orders != 1 {
		t.Fatalf("streams: %+v", list)
	}
	f, err := a.GetStream(old)
	if err != nil || len(f.Collections) != 1 || f.Collections[0].Entries[0].Text != "Hades II" {
		t.Fatalf("old stream: %v %+v", err, f)
	}
	if st := reloadState(t, a); st.Stream.ID != a.st.Stream.ID || len(st.Entries) != 1 || st.Entries[0].Text != "Doom" {
		t.Fatalf("current stream after reload: %+v", st.Entries)
	}
}

// Новий аукціон не стирає попередній; у БД лишаються всі ставки, а в пам'яті — останні 300.
func TestStoreAuctions(t *testing.T) {
	a := newTestApp(t)
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}})
	a.AuctionStart()
	for i := 0; i < 320; i++ {
		a.onRedemption(bid("b"+strconv.Itoa(i), "q", "Квиток", "u"+strconv.Itoa(i%7), []string{"Elden Ring", "Hades", "Celeste"}[i%3], 1000), User{})
	}
	first := a.st.Auction.ID
	if first == "" || len(a.st.Auction.Feed) != 300 {
		t.Fatalf("id=%q feed=%d", first, len(a.st.Auction.Feed))
	}
	st := reloadState(t, a)
	if st.Auction.ID != first || len(st.Auction.Lots) != 3 || len(st.Auction.Feed) != 300 || st.Auction.Feed[0].ID != a.st.Auction.Feed[0].ID || st.Auction.Feed[299].ID != a.st.Auction.Feed[299].ID {
		t.Fatalf("reload: id=%s lots=%d feed=%d", st.Auction.ID, len(st.Auction.Lots), len(st.Auction.Feed))
	}
	if len(st.Auction.SeenIDs) != len(a.st.Auction.SeenIDs) {
		t.Fatal("seen ids lost")
	}
	a.AuctionFinish()
	a.AuctionNew()
	a.AuctionStart()
	a.onRedemption(bid("n1", "q", "Квиток", "u1", "Other", 5000), User{})
	if a.st.Auction.ID == "" || a.st.Auction.ID == first {
		t.Fatalf("new auction id %q", a.st.Auction.ID)
	}
	recs := a.loadAllAuctions()
	if len(recs) != 2 {
		t.Fatalf("auctions: %d", len(recs))
	}
	for _, r := range recs {
		if r.ID == first && len(r.Feed) != 320 {
			t.Fatalf("all bids must stay in DB: %d", len(r.Feed))
		}
	}
}

// Перший запуск нової версії: subday_state.json переноситься в БД і перейменовується.
func TestStoreMigratesStateFile(t *testing.T) {
	a := newTestApp(t)
	a.StartTimer()
	a.AddManual("Phill", 1, 3, "Hades")
	a.AuctionSaveSettings(AuctionSettings{RewardIDs: []string{"q"}})
	a.AuctionStart()
	a.onRedemption(bid("1", "q", "Квиток", "u1", "Celeste", 1000), User{})
	old := a.st
	old.Auction.ID = "" // у старих версіях id аукціону не було
	a.store.Close()
	for _, f := range []string{"phillbot.db", "phillbot.db-wal", "phillbot.db-shm"} {
		_ = os.Remove(filepath.Join(a.baseDir, f))
	}
	writeJSON(a.stateFile, old)

	a.openStore()
	t.Cleanup(func() { a.store.Close() })
	if a.storeErr != "" {
		t.Fatal(a.storeErr)
	}
	if len(a.st.Entries) != 1 || a.st.Entries[0].Text != "Hades" || len(a.st.Auction.Lots) != 1 || a.st.Session.Status != "running" {
		t.Fatalf("migrated state: %+v", a.st)
	}
	if _, err := os.Stat(a.stateFile + ".migrated"); err != nil {
		t.Fatal("subday_state.json must be renamed")
	}
	if st := reloadState(t, a); len(st.Entries) != 1 || st.Auction.ID == "" {
		t.Fatalf("not in DB: %+v", st)
	}
}

// Тека з exe може містити пробіли, кирилицю, «#» — БД має відкриватися.
func TestStoreOddPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Мій стрім #1 (100%)")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(filepath.Join(dir, "phillbot.db"), "local")
	if err != nil {
		t.Fatal(err)
	}
	st := defaultState()
	st.Settings.RewardID = "r1"
	if err := s.Sync(&st); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(filepath.Join(dir, "phillbot.db")); err != nil {
		t.Fatal("db file must be inside the odd folder", err)
	}
}

// Старі файли стрімів могли мати два збори з однаковим номером — після перенесення це два окремі збори.
func TestStoreMigratesDuplicateCollectionN(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "streams"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "streams", "2026-11-01_18-00-00.json"), []byte(`{"id":"2026-11-01_18-00-00","collections":[
		{"n":1,"entries":[{"id":"a","user":"A","text":"X"}]},{"n":2,"entries":[{"id":"b","user":"B","text":"Y"}]},
		{"n":2,"entries":[{"id":"c","user":"C","text":"Z"}]}]}`), 0644)
	s, err := openStore(filepath.Join(dir, "phillbot.db"), "local")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, err := s.migrateFiles(dir); err != nil {
		t.Fatal(err)
	}
	f, err := s.streamFile("2026-11-01_18-00-00")
	if err != nil || len(f.Collections) != 3 || f.Collections[2].N != 3 || len(f.Collections[2].Entries) != 1 || f.Collections[2].Entries[0].ID != "c" {
		t.Fatalf("%v %+v", err, f.Collections)
	}
}
