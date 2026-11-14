//go:build !server

package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportAll(t *testing.T) {
	a := newTestApp(t)
	a.store.Close() // файли попередньої версії: перенесуться в БД при першому відкритті нової версії
	for _, f := range []string{"phillbot.db", "phillbot.db-wal", "phillbot.db-shm"} {
		_ = os.Remove(filepath.Join(a.baseDir, f))
	}
	s := defaultSettings()
	// збережений стрім + поточний з пам'яті + старий архів
	_ = os.MkdirAll(a.streamsDir, 0755)
	_ = os.MkdirAll(a.historyDir, 0755)
	_ = os.WriteFile(filepath.Join(a.streamsDir, "2026-11-01_18-00-00.json"),
		[]byte(`{"id":"2026-11-01_18-00-00","collections":[{"n":1,"winner":"Celeste","entries":[{"id":"a","user":"Mira","tier":3,"months":2,"text":"Celeste"}]}]}`), 0644)
	_ = os.WriteFile(filepath.Join(a.historyDir, "subday_2026-10-01_10-00-00.json"),
		[]byte(`{"entries":[{"id":"o","user":"Old","tier":1,"months":1,"text":"Hades"}],"settings":{}}`), 0644)
	_ = os.WriteFile(filepath.Join(a.historyDir, "auction_2026-11-02_20-00-00.json"),
		[]byte(`{"lots":[{"id":"l1","name":"Elden Ring","amount":6000,"bids":2,"users":[{"name":"Zo","amount":5000},{"name":"Kat","amount":1000}]},{"id":"l2","name":"Doom","amount":1000,"bids":1,"out":1}],"feed":[{"id":"b1","ts":1,"user":"Zo","amount":5000,"text":"elden","lotId":"l1"}],"winner":"l1"}`), 0644)
	a.openStore()
	if a.store == nil || a.storeErr != "" {
		t.Fatal(a.storeErr)
	}
	t.Cleanup(func() { a.store.Close() })
	if _, err := os.Stat(a.streamsDir + ".migrated"); err != nil {
		t.Fatal("old files must be renamed, not deleted")
	}
	a.st.Settings = s
	a.st.Stream = StreamInfo{ID: "2026-11-13_19-00-00"}
	a.st.CollN = 1
	a.st.Entries = []Entry{{ID: "x", User: "Taras", Tier: 1, Months: 8, Text: "Hollow; Knight"}, {ID: "d", User: "Don", Text: "Hades", Donation: true, DonPoints: 4, UAH: 120}}
	a.st.Auction.Lots = []Lot{{ID: "n1", Name: "Celeste", Amount: 3000, Bids: 1}}
	a.st.Auction.Timer.StartedAt = 1794600000000

	colls := a.loadAllSubday()
	if len(colls) != 3 {
		t.Fatalf("collections: %d", len(colls))
	}
	sub := string(subdayAllCSV(colls))
	for _, want := range []string{"\xEF\xBB\xBFsep=;", "Mira;;3;2;;;2;Celeste;так;Celeste", "\"Hollow; Knight\"", "донат", ";120;4;Hades", "2026-10-01_10-00-00-a"} {
		if !strings.Contains(sub, want) {
			t.Errorf("subday csv lacks %q:\n%s", want, sub)
		}
	}
	recs := a.loadAllAuctions()
	if len(recs) != 2 {
		t.Fatalf("auctions: %d", len(recs))
	}
	au := string(auctionAllCSV(recs))
	for _, want := range []string{"Elden Ring;6000;2;Zo (5000), Kat (1000);переміг", "Doom;1000;1;;вибув у 1-му оберті", "Ставки", "Zo;;5000;;elden;Elden Ring", "Celeste;3000"} {
		if !strings.Contains(au, want) {
			t.Errorf("auction csv lacks %q:\n%s", want, au)
		}
	}
	if strings.Count(au, "\xEF\xBB\xBF") != 1 {
		t.Error("BOM inside auction csv")
	}
	zb, err := exportZip(colls, recs)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"subday.csv", "auction_lots.csv", "auction_bids.csv", "subday/2026-11-13_19-00-00/збір_01.csv", "auction/2026-11-02_20-00-00.csv"} {
		if !names[want] {
			t.Errorf("zip lacks %s: %v", want, names)
		}
	}
	if _, err := a.ExportAll("bogus"); err == nil {
		t.Error("unknown kind accepted")
	}
}
