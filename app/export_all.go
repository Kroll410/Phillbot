package main

// Експорт «за весь час» із загальних налаштувань: СабДей .csv, Аукціон .csv, усе в .zip.
// Джерело — внутрішня БД (store.go) + поточний збір і аукціон з пам'яті.

import (
	"archive/zip"
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
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

// усі збори СабДею: завершені збори з БД + поточний збір з пам'яті
func (a *Channel) loadAllSubday() []subdayColl {
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
	if a.store != nil {
		for _, sc := range a.store.allCollections() {
			if sc.Stream != curID {
				out = append(out, sc)
			}
		}
	}
	for _, c := range cur {
		out = append(out, subdayColl{curID, c})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stream != out[j].Stream {
			return out[i].Stream < out[j].Stream
		}
		return out[i].Coll.N < out[j].Coll.N
	})
	return out
}

// усі аукціони: з БД; поточний — з пам'яті (у БД його ставки можуть бути повніші, ніж останні 300 у пам'яті — беремо з БД, якщо він там є)
func (a *Channel) loadAllAuctions() []auctionRec {
	byID := map[string]auctionRec{}
	if a.store != nil {
		for _, r := range a.store.allAuctions() {
			byID[r.ID] = r
		}
	}
	a.mu.Lock()
	au := a.st.Auction
	if len(au.Lots) > 0 {
		id := au.ID
		if id == "" {
			id = auctionID(au.Timer.StartedAt)
		}
		r := auctionRec{id, append([]Lot(nil), au.Lots...), append([]Bid(nil), au.Feed...), au.Winner}
		if old, ok := byID[id]; ok && len(old.Feed) > len(r.Feed) {
			r.Feed = old.Feed
		}
		byID[id] = r
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
