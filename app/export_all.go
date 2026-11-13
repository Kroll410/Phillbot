package main

// Експорт «за весь час» із загальних налаштувань: СабДей .csv, Аукціон .csv, усе в .zip.
// Джерела — те, що програма вже зберігає: streams/*.json (+ поточний стрім з пам'яті),
// старі архіви history/subday_*.json, аукціони history/auction_*.json (+ поточний аукціон з пам'яті).
// Коли з'явиться внутрішня БД, зміняться лише loadAllSubday / loadAllAuctions.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

var csvBOM = []byte{0xEF, 0xBB, 0xBF}

type subdayColl struct {
	Stream string
	Coll   Collection
}

type auctionRec struct {
	ID     string // дата старту: 2026-11-12_21-16-20
	Lots   []Lot
	Feed   []Bid
	Winner string
}

// усі збори СабДею: збережені стріми, поточний стрім (з пам'яті — найсвіжіший), старі архіви
func (a *App) loadAllSubday() []subdayColl {
	a.mu.Lock()
	curID := a.st.Stream.ID
	cur := append([]Collection(nil), a.st.Past...)
	if len(a.st.Entries) > 0 {
		c := a.currentCollectionLocked()
		if c.N == 0 {
			c.N = len(a.st.Past) + 1
		}
		cur = append(cur, c)
	}
	a.mu.Unlock()

	var out []subdayColl
	files, _ := filepath.Glob(filepath.Join(a.streamsDir, "*.json"))
	for _, p := range files {
		var f StreamFile
		if b, err := os.ReadFile(p); err != nil || json.Unmarshal(b, &f) != nil {
			continue
		}
		if f.ID == "" {
			f.ID = strings.TrimSuffix(filepath.Base(p), ".json")
		}
		if f.ID == curID {
			continue
		}
		for _, c := range f.Collections {
			c.Settings.normalize() // правила балів фіксовані; старі файли могли їх не мати
			out = append(out, subdayColl{f.ID, c})
		}
	}
	for _, c := range cur {
		out = append(out, subdayColl{curID, c})
	}
	// архіви до появи стрімів: кожен — окремий «стрім» з одним збором
	old, _ := filepath.Glob(filepath.Join(a.historyDir, "subday_*.json"))
	for _, p := range old {
		var f struct {
			Entries  []Entry `json:"entries"`
			Settings Settings
			Session  Timer `json:"session"`
		}
		if b, err := os.ReadFile(p); err != nil || json.Unmarshal(b, &f) != nil || len(f.Entries) == 0 {
			continue
		}
		f.Settings.normalize()
		id := "архів_" + strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "subday_"), ".json")
		out = append(out, subdayColl{id, Collection{N: 1, StartedAt: f.Session.StartedAt, FinishedAt: f.Session.FinishedAt, Settings: f.Settings, Entries: f.Entries}})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stream != out[j].Stream {
			return out[i].Stream < out[j].Stream
		}
		return out[i].Coll.N < out[j].Coll.N
	})
	return out
}

func auctionID(start int64) string {
	if start == 0 {
		start = time.Now().UnixMilli()
	}
	return time.UnixMilli(start).Format("2006-01-02_15-04-05")
}

// усі аукціони: history/auction_*.json, поточний — з пам'яті (новіший за файл)
func (a *App) loadAllAuctions() []auctionRec {
	byID := map[string]auctionRec{}
	files, _ := filepath.Glob(filepath.Join(a.historyDir, "auction_*.json"))
	for _, p := range files {
		var f struct {
			Lots   []Lot  `json:"lots"`
			Feed   []Bid  `json:"feed"`
			Winner string `json:"winner"`
		}
		if b, err := os.ReadFile(p); err != nil || json.Unmarshal(b, &f) != nil || len(f.Lots) == 0 {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "auction_"), ".json")
		byID[id] = auctionRec{id, f.Lots, f.Feed, f.Winner}
	}
	a.mu.Lock()
	au := a.st.Auction
	if len(au.Lots) > 0 {
		id := auctionID(au.Timer.StartedAt)
		byID[id] = auctionRec{id, append([]Lot(nil), au.Lots...), append([]Bid(nil), au.Feed...), au.Winner}
	}
	a.mu.Unlock()
	out := make([]auctionRec, 0, len(byID))
	for _, r := range byID {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type csvW struct{ b strings.Builder }

func newCSV() *csvW { w := &csvW{}; w.b.WriteString("sep=;\r\n"); return w }
func (w *csvW) line(cells ...string) {
	for i, c := range cells {
		if i > 0 {
			w.b.WriteString(";")
		}
		w.b.WriteString(csvCell(c))
	}
	w.b.WriteString("\r\n")
}
func (w *csvW) bytes() []byte { return append(append([]byte(nil), csvBOM...), w.b.String()...) }

func fmtTs(ms int64) string {
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

func fmtUAH(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// усі замовлення СабДею одною таблицею
func subdayAllCSV(colls []subdayColl) []byte {
	w := newCSV()
	w.line("Стрім", "Збір", "Час", "Чатерс", "Логін", "Тір", "Місяці", "Подаровані сабки", "Донат, грн", "Бали", "Замовлення", "Рахується", "Переможець збору")
	for _, sc := range colls {
		s, es := sc.Coll.Settings, sc.Coll.Entries
		counted, pts := countedIDs(s, es), entryPoints(s, es)
		for _, e := range es {
			tier, months := "—", ""
			if e.Tier > 0 {
				tier, months = strconv.Itoa(e.Tier), strconv.Itoa(e.Months)
			}
			if e.Donation {
				tier = "донат"
			}
			status := "так"
			if e.Excluded {
				status = "приховано"
			} else if !counted[e.ID] {
				status = "ні (дубль)"
			}
			gifts := ""
			if e.Gifts > 0 {
				gifts = strconv.Itoa(e.Gifts)
			}
			w.line(sc.Stream, strconv.Itoa(sc.Coll.N), fmtTs(e.Ts), e.User, e.Login, tier, months, gifts, fmtUAH(e.UAH),
				strconv.Itoa(csvPoints(s, e, counted, pts)), e.Text, status, sc.Coll.Winner)
		}
	}
	return w.bytes()
}

func lotResult(l Lot, winner string) string {
	switch {
	case l.ID == winner:
		return "переміг"
	case l.Hidden:
		return "приховано"
	case l.Out > 0:
		return "вибув у " + strconv.Itoa(l.Out) + "-му оберті"
	}
	return ""
}

func lotUsers(l Lot) string {
	var who []string
	for _, u := range l.Users {
		who = append(who, u.Name+" ("+strconv.Itoa(u.Amount)+")")
	}
	return strings.Join(who, ", ")
}

// лоти всіх аукціонів (місце — за сумою балів)
func auctionLotsCSV(recs []auctionRec) []byte {
	w := newCSV()
	w.line("Аукціон", "Місце", "Лот", "Бали", "Ставок", "Хто ставив", "Результат")
	for _, r := range recs {
		lots := append([]Lot(nil), r.Lots...)
		sort.SliceStable(lots, func(i, j int) bool { return lots[i].Amount > lots[j].Amount })
		for i, l := range lots {
			w.line(r.ID, strconv.Itoa(i+1), l.Name, strconv.Itoa(l.Amount), strconv.Itoa(l.Bids), lotUsers(l), lotResult(l, r.Winner))
		}
	}
	return w.bytes()
}

// усі ставки всіх аукціонів (у хронологічному порядку)
func auctionBidsCSV(recs []auctionRec) []byte {
	w := newCSV()
	w.line("Аукціон", "Час", "Чатерс", "Логін", "Бали", "Донат, грн", "Текст ставки", "Лот")
	for _, r := range recs {
		names := map[string]string{}
		for _, l := range r.Lots {
			names[l.ID] = l.Name
		}
		feed := append([]Bid(nil), r.Feed...)
		sort.SliceStable(feed, func(i, j int) bool { return feed[i].Ts < feed[j].Ts })
		for _, b := range feed {
			w.line(r.ID, fmtTs(b.Ts), b.User, b.Login, strconv.Itoa(b.Amount), fmtUAH(b.UAH), b.Text, names[b.LotID])
		}
	}
	return w.bytes()
}

// аукціон .csv: лоти, порожній рядок, ставки — в одному файлі
func auctionAllCSV(recs []auctionRec) []byte {
	lots, bids := auctionLotsCSV(recs), auctionBidsCSV(recs)
	bids = bytes.TrimPrefix(bids, append(append([]byte(nil), csvBOM...), "sep=;\r\n"...))
	return append(append(lots, "\r\nСтавки\r\n"...), bids...)
}

// .zip: зведені таблиці + окремий файл на кожен збір і кожен аукціон
func exportZip(colls []subdayColl, recs []auctionRec) ([]byte, error) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	now := time.Now()
	add := func(name string, data []byte) error {
		f, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now, Flags: 0x800}) // 0x800 — UTF-8 імена
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	files := []struct {
		name string
		data []byte
	}{
		{"subday.csv", subdayAllCSV(colls)},
		{"auction_lots.csv", auctionLotsCSV(recs)},
		{"auction_bids.csv", auctionBidsCSV(recs)},
	}
	for _, sc := range colls {
		files = append(files, struct {
			name string
			data []byte
		}{fmt.Sprintf("subday/%s/збір_%02d.csv", sc.Stream, sc.Coll.N),
			append(append([]byte(nil), csvBOM...), collectionCSV(sc.Coll.Settings, sc.Coll.Entries)...)})
	}
	for _, r := range recs {
		files = append(files, struct {
			name string
			data []byte
		}{"auction/" + r.ID + ".csv", auctionAllCSV([]auctionRec{r})})
	}
	for _, f := range files {
		if err := add(f.name, f.data); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ExportAll — kind: "subday" (.csv), "auction" (.csv), "zip". Питає, куди зберегти; "" — скасовано.
func (a *App) ExportAll(kind string) (string, error) {
	stamp := time.Now().Format("2006-01-02")
	var name, filter string
	var build func() ([]byte, error)
	switch kind {
	case "subday":
		name, filter = "PhillBot_СабДей_"+stamp+".csv", "*.csv"
		build = func() ([]byte, error) { return subdayAllCSV(a.loadAllSubday()), nil }
	case "auction":
		name, filter = "PhillBot_Аукціон_"+stamp+".csv", "*.csv"
		build = func() ([]byte, error) { return auctionAllCSV(a.loadAllAuctions()), nil }
	case "zip":
		name, filter = "PhillBot_"+stamp+".zip", "*.zip"
		build = func() ([]byte, error) { return exportZip(a.loadAllSubday(), a.loadAllAuctions()) }
	default:
		return "", errors.New("невідомий експорт")
	}
	dir := a.exportsDir
	_ = os.MkdirAll(dir, 0755)
	p, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		DefaultDirectory: dir, DefaultFilename: name,
		Filters: []wruntime.FileFilter{{DisplayName: strings.TrimPrefix(filter, "*."), Pattern: filter}},
	})
	if err != nil || p == "" {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(p), filepath.Ext(name)) {
		p += filepath.Ext(name)
	}
	data, err := build()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		return "", errors.New("не вдалося записати файл — можливо, він відкритий в Excel")
	}
	a.logf("Експорт: %s", p)
	if runtime.GOOS == "windows" {
		_ = exec.Command("explorer", "/select,", p).Start() // показати збережений файл
	}
	return p, nil
}
