package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// база «вебверсії» з двома каналами → імпорт одного в базу ПК: дані замінюються, інший канал не потрапляє
func TestImportChannel(t *testing.T) {
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "web.db")
	mk := func(ch, game string, months int) {
		s, err := openStore(srcFile, ch)
		if err != nil {
			t.Fatal(err)
		}
		st := defaultState()
		st.Stream = StreamInfo{ID: "2026-11-13_19-00-00", StartedAt: 1}
		st.Entries = []Entry{{ID: ch + "-e1", Ts: 1, User: "Mira", UserID: "m", Tier: 1, Months: months, Text: game}}
		st.Past = []Collection{{N: 1, StartedAt: 1, FinishedAt: 2, Winner: game, Entries: []Entry{{ID: ch + "-p1", Ts: 1, User: "Ira", Tier: 1, Months: 1, Text: game}}}}
		st.CollN = 2
		st.Settings.GameMin = 25
		st.SeenRedeems = []string{ch + "-r1"}
		if err := s.Sync(&st); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	mk("111", "Hades", 3)
	mk("222", "Doom", 9)
	db, _ := sql.Open("sqlite", "file:"+sqliteURIPath(srcFile))
	db.Exec(`CREATE TABLE web_users (login TEXT PRIMARY KEY, twitch_id TEXT, display_name TEXT)`)
	db.Exec(`INSERT INTO web_users VALUES ('phill', '111', 'Phill')`)
	db.Close()

	chs, err := listImportChannels(srcFile)
	if err != nil || len(chs) != 2 {
		t.Fatal(chs, err)
	}
	var phill ImportChannel
	for _, c := range chs {
		if c.ID == "111" {
			phill = c
		}
	}
	if phill.Name != "Phill" || phill.Entries != 2 || phill.Collections != 1 {
		t.Fatalf("%+v", phill)
	}

	// база ПК зі своїми даними й локальними налаштуваннями
	dstFile := filepath.Join(dir, "phillbot.db")
	dst, err := openStore(dstFile, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	old := defaultState()
	old.Stream = StreamInfo{ID: "2026-10-01_10-00-00", StartedAt: 1}
	old.Entries = []Entry{{ID: "old", Ts: 1, User: "Old", Tier: 1, Months: 1, Text: "Celeste"}}
	old.Settings.NgrokToken, old.Settings.ObsPort = "tok", 17999
	if err := dst.Sync(&old); err != nil {
		t.Fatal(err)
	}

	bak := importBackupName(dir, time.Now())
	if err := dst.backupTo(bak); err != nil {
		t.Fatal(err)
	}
	if err := dst.importChannel(srcFile, "111"); err != nil {
		t.Fatal(err)
	}
	st, ok, err := dst.Load()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if len(st.Entries) != 1 || st.Entries[0].Text != "Hades" || len(st.Past) != 1 || st.Past[0].Winner != "Hades" {
		t.Fatalf("imported state: %+v", st.Entries)
	}
	if st.Settings.GameMin != 25 || st.CollN != 2 || len(st.SeenRedeems) != 1 || st.SeenRedeems[0] != "111-r1" {
		t.Fatalf("settings/meta not imported: %+v", st.Settings)
	}
	keepLocalSettings(&st.Settings, old.Settings)
	if st.Settings.NgrokToken != "tok" || st.Settings.ObsPort != 17999 {
		t.Fatal("local settings lost")
	}
	// резервна копія містить старі дані
	b, err := openStore(bak, "local")
	if err != nil {
		t.Fatal(err)
	}
	if bs, _, _ := b.Load(); len(bs.Entries) != 1 || bs.Entries[0].Text != "Celeste" {
		t.Fatal("backup lacks old data")
	}
	b.Close()
	// другий імпорт того самого — без дублів
	if err := dst.importChannel(srcFile, "111"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := dst.Load(); len(st.Entries) != 1 {
		t.Fatal("duplicate rows after re-import")
	}
	if err := dst.importChannel(srcFile, "999"); err == nil || !strings.Contains(err.Error(), "немає каналу") {
		t.Fatal("unknown channel accepted", err)
	}
	if _, err := listImportChannels(filepath.Join(dir, "nope.db")); err == nil {
		t.Fatal("missing file accepted")
	}
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("hello"), 0644)
	if _, err := listImportChannels(filepath.Join(dir, "x.txt")); err == nil {
		t.Fatal("non-db accepted")
	}
}

// Разова витяжка каналу з копії бази сервера: PHILLBOT_EXTRACT=<src.db>|<login або id>|<dst.db> go test -run TestExtractManual
func TestExtractManual(t *testing.T) {
	spec := os.Getenv("PHILLBOT_EXTRACT")
	if spec == "" {
		t.Skip()
	}
	p := strings.Split(spec, "|")
	src, who, out := p[0], strings.ToLower(p[1]), p[2]
	db, err := sql.Open("sqlite", "file:"+sqliteURIPath(src))
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`) // перенести журнал WAL у файл
	db.Close()
	chs, err := listImportChannels(src)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, c := range chs {
		t.Logf("канал %s (%s): стрімів %d, зборів %d, замовлень %d, аукціонів %d", c.ID, c.Name, c.Streams, c.Collections, c.Entries, c.Auctions)
		if strings.ToLower(c.Name) == who || c.ID == who {
			id = c.ID
		}
	}
	if id == "" {
		t.Fatalf("канал %q не знайдено", who)
	}
	os.Remove(out)
	dst, err := openStore(out, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := dst.importChannel(src, id); err != nil {
		t.Fatal(err)
	}
	st, _, err := dst.Load()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("витягнуто %s → %s: стрім %s, зборів у стрімі %d, поточних замовлень %d, лотів %d", id, out, st.Stream.ID, len(st.Past), len(st.Entries), len(st.Auction.Lots))
}
