package main

// Store — внутрішня база даних (SQLite, файл phillbot.db поруч з exe; у хмарі — той самий код на PostgreSQL).
//
// Стан каналу, як і раніше, живе в пам'яті (State) і змінюється через mutate. Store лише зберігає його:
// Sync порівнює стан з тим, що вже записано, і пише тільки зміни — нове замовлення = один INSERT,
// правка = один UPDATE. Повного перезапису файлу на кожен клік більше немає.
//
// Таблиці (усі з channel_id — у хмарі це id каналу Twitch, у десктопі "local"):
//   channels    — «живий» стан: налаштування, таймери, фаза, голосування, аукціон без лотів і ставок (JSON)
//   streams     — стріми СабДею
//   collections — завершені збори стріму (правила балів на момент збору, переможець)
//   entries     — замовлення (coll_n = 0 — поточний збір)
//   auctions, lots, bids — аукціони; ставки не видаляються (у пам'яті — лише останні 300)
//   seen        — уже оброблені id: нагороди, донати, ставки (захист від повторів)

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

var schema = []string{
	`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS channels (id TEXT PRIMARY KEY, live TEXT NOT NULL, updated_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS streams (channel_id TEXT NOT NULL, id TEXT NOT NULL, started_at BIGINT NOT NULL,
		PRIMARY KEY (channel_id, id))`,
	`CREATE TABLE IF NOT EXISTS collections (channel_id TEXT NOT NULL, stream_id TEXT NOT NULL, n INTEGER NOT NULL,
		started_at BIGINT, finished_at BIGINT, winner TEXT, settings TEXT NOT NULL,
		PRIMARY KEY (channel_id, stream_id, n))`,
	`CREATE TABLE IF NOT EXISTS entries (channel_id TEXT NOT NULL, id TEXT NOT NULL, stream_id TEXT NOT NULL, coll_n INTEGER NOT NULL,
		ts BIGINT NOT NULL, login TEXT, text TEXT, data TEXT NOT NULL%SEQ%, PRIMARY KEY (channel_id, id))`,
	`CREATE INDEX IF NOT EXISTS entries_coll ON entries (channel_id, stream_id, coll_n)`,
	`CREATE TABLE IF NOT EXISTS auctions (channel_id TEXT NOT NULL, id TEXT NOT NULL, started_at BIGINT, finished_at BIGINT, winner TEXT,
		PRIMARY KEY (channel_id, id))`,
	`CREATE TABLE IF NOT EXISTS lots (channel_id TEXT NOT NULL, auction_id TEXT NOT NULL, id TEXT NOT NULL, ord INTEGER NOT NULL,
		name TEXT, amount BIGINT, data TEXT NOT NULL, PRIMARY KEY (channel_id, auction_id, id))`,
	`CREATE TABLE IF NOT EXISTS bids (channel_id TEXT NOT NULL, auction_id TEXT NOT NULL, id TEXT NOT NULL, ts BIGINT NOT NULL,
		amount BIGINT, data TEXT NOT NULL%SEQ%, PRIMARY KEY (channel_id, auction_id, id))`,
	`CREATE TABLE IF NOT EXISTS seen (channel_id TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL%SEQ%, PRIMARY KEY (channel_id, kind, id))`,
}

type cached struct{ scope, sig string } // scope — стрім/аукціон рядка; sig — що записано

type Store struct {
	db *sql.DB
	ch string
	pg bool // PostgreSQL (інакше SQLite)

	// що вже записано (для порівняння в Sync); торкатися лише під a.mu
	live     string
	streams  map[string]bool
	colls    map[string]cached // stream/n
	entries  map[string]cached // id
	auctions map[string]string // id → sig
	lots     map[string]cached // auction/id
	bids     map[string]string // auction/id → sig
	seen     map[string]map[string]bool
}

func openStore(file, channel string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+sqliteURIPath(file)+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // один запис за раз — без блокувань
	return initStore(db, false, channel)
}

// openDB — база за рядком підключення: postgres://… або postgresql://… відкриває PostgreSQL
// (лише у збірці з тегом server), усе інше — шлях до файлу SQLite.
func openDB(target, channel string) (*Store, error) {
	if isPostgresURL(target) {
		return openPostgres(target, channel)
	}
	return openStore(target, channel)
}

func isPostgresURL(s string) bool {
	return strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://")
}

// initStore — схема й службові записи; єдине місце, де діалекти розходяться в DDL.
func initStore(db *sql.DB, pg bool, channel string) (*Store, error) {
	s := &Store{db: db, ch: channel, pg: pg}
	for _, q := range schema {
		if _, err := db.Exec(s.ddl(q)); err != nil {
			db.Close()
			return nil, fmt.Errorf("схема БД: %w", err)
		}
	}
	if _, err := db.Exec(s.rb(`INSERT INTO meta (key, value) VALUES ('schema_version', ?) ON CONFLICT (key) DO NOTHING`), strconv.Itoa(schemaVersion)); err != nil {
		db.Close()
		return nil, err
	}
	s.resetCache()
	return s, nil
}

// шлях для URI SQLite: пробіли, «#», «?» і «%» у теці з exe не мають зламати рядок підключення
func sqliteURIPath(file string) string {
	p := filepath.ToSlash(file)
	if len(p) > 1 && p[1] == ':' {
		p = "/" + p // C:/… → /C:/…
	}
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23", " ", "%20").Replace(p)
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) resetCache() {
	s.live = ""
	s.streams, s.colls, s.entries = map[string]bool{}, map[string]cached{}, map[string]cached{}
	s.auctions, s.lots, s.bids = map[string]string{}, map[string]cached{}, map[string]string{}
	s.seen = map[string]map[string]bool{}
}

func (s *Store) meta(key string) string {
	var v string
	_ = s.db.QueryRow(s.rb(`SELECT value FROM meta WHERE key = ?`), key).Scan(&v)
	return v
}

func (s *Store) setMeta(key, v string) error {
	_, err := s.db.Exec(s.rb(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`), key, v)
	return err
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// «живий» стан без того, що лежить в окремих таблицях
func liveJSON(st *State) string {
	c := *st
	c.Entries, c.Past, c.SeenRedeems, c.DonSeen = nil, nil, nil, nil
	c.Auction.Lots, c.Auction.Feed, c.Auction.SeenIDs = nil, nil, nil
	return mustJSON(c)
}

func collMeta(c Collection) string {
	return mustJSON([]any{c.StartedAt, c.FinishedAt, c.Winner, c.Settings})
}

// ---------- запис ----------

// Sync — записати зміни стану. Викликати під a.mu.
func (s *Store) Sync(st *State) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ex := func(q string, args ...any) {
		if err == nil {
			_, err = tx.Exec(s.rb(q), args...)
		}
	}
	now := time.Now().UnixMilli()

	if l := liveJSON(st); l != s.live {
		ex(`INSERT INTO channels (id, live, updated_at) VALUES (?, ?, ?) ON CONFLICT (id) DO UPDATE SET live = excluded.live, updated_at = excluded.updated_at`, s.ch, l, now)
		defer func(l string) {
			if err == nil {
				s.live = l
			}
		}(l)
	}

	// стрім і завершені збори
	sid := st.Stream.ID
	if sid != "" && !s.streams[sid] {
		ex(`INSERT INTO streams (channel_id, id, started_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, s.ch, sid, st.Stream.StartedAt)
		s.streams[sid] = true
	}
	wantC := map[string]cached{}
	for _, c := range st.Past {
		wantC[sid+"/"+strconv.Itoa(c.N)] = cached{sid, collMeta(c)}
	}
	for k, w := range wantC {
		if s.colls[k] != w {
			c := st.Past[0]
			for _, p := range st.Past {
				if sid+"/"+strconv.Itoa(p.N) == k {
					c = p
				}
			}
			ex(`INSERT INTO collections (channel_id, stream_id, n, started_at, finished_at, winner, settings) VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (channel_id, stream_id, n) DO UPDATE SET started_at = excluded.started_at, finished_at = excluded.finished_at,
				winner = excluded.winner, settings = excluded.settings`, s.ch, sid, c.N, c.StartedAt, c.FinishedAt, c.Winner, mustJSON(c.Settings))
		}
	}
	for k, c := range s.colls {
		if _, ok := wantC[k]; !ok && c.scope == sid { // збори минулих стрімів лишаються в БД
			n, _ := strconv.Atoi(k[strings.LastIndexByte(k, '/')+1:])
			ex(`DELETE FROM collections WHERE channel_id = ? AND stream_id = ? AND n = ?`, s.ch, sid, n)
		}
	}
	s.colls = wantC

	// замовлення: завершених зборів (coll_n = N) і поточного (coll_n = 0)
	wantE := map[string]cached{}
	put := func(e Entry, n int) {
		j := mustJSON(e)
		w := cached{sid, strconv.Itoa(n) + "|" + j}
		wantE[e.ID] = w
		if s.entries[e.ID] != w {
			ex(`INSERT INTO entries (channel_id, id, stream_id, coll_n, ts, login, text, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (channel_id, id) DO UPDATE SET stream_id = excluded.stream_id, coll_n = excluded.coll_n, ts = excluded.ts,
				login = excluded.login, text = excluded.text, data = excluded.data`, s.ch, e.ID, sid, n, e.Ts, e.Login, e.Text, j)
		}
	}
	for _, c := range st.Past {
		for _, e := range c.Entries {
			put(e, c.N)
		}
	}
	for _, e := range st.Entries {
		put(e, 0)
	}
	for id, c := range s.entries {
		if _, ok := wantE[id]; !ok && c.scope == sid {
			ex(`DELETE FROM entries WHERE channel_id = ? AND id = ?`, s.ch, id)
		}
	}
	s.entries = wantE

	// аукціон
	au := &st.Auction
	if au.ID == "" && (len(au.Lots) > 0 || len(au.Feed) > 0) {
		au.ID = s.newAuctionID(tx, au.Timer.StartedAt)
	}
	wantL := map[string]cached{}
	if aid := au.ID; aid != "" {
		sig := mustJSON([]any{au.Timer.StartedAt, au.Timer.FinishedAt, au.Winner})
		if s.auctions[aid] != sig {
			ex(`INSERT INTO auctions (channel_id, id, started_at, finished_at, winner) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (channel_id, id) DO UPDATE SET started_at = excluded.started_at, finished_at = excluded.finished_at, winner = excluded.winner`,
				s.ch, aid, au.Timer.StartedAt, au.Timer.FinishedAt, au.Winner)
			s.auctions = map[string]string{aid: sig}
		}
		for i, l := range au.Lots {
			j := mustJSON(l)
			w := cached{aid, strconv.Itoa(i) + "|" + j}
			wantL[aid+"/"+l.ID] = w
			if s.lots[aid+"/"+l.ID] != w {
				ex(`INSERT INTO lots (channel_id, auction_id, id, ord, name, amount, data) VALUES (?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT (channel_id, auction_id, id) DO UPDATE SET ord = excluded.ord, name = excluded.name, amount = excluded.amount, data = excluded.data`,
					s.ch, aid, l.ID, i, l.Name, l.Amount, j)
			}
		}
		for k, c := range s.lots {
			if _, ok := wantL[k]; !ok && c.scope == aid {
				ex(`DELETE FROM lots WHERE channel_id = ? AND auction_id = ? AND id = ?`, s.ch, aid, k[len(aid)+1:])
			}
		}
		wantB := map[string]string{}
		for _, b := range au.Feed {
			j := mustJSON(b)
			wantB[aid+"/"+b.ID] = j
			if s.bids[aid+"/"+b.ID] != j {
				ex(`INSERT INTO bids (channel_id, auction_id, id, ts, amount, data) VALUES (?, ?, ?, ?, ?, ?)
					ON CONFLICT (channel_id, auction_id, id) DO UPDATE SET ts = excluded.ts, amount = excluded.amount, data = excluded.data`,
					s.ch, aid, b.ID, b.Ts, b.Amount, j)
			}
		}
		s.bids = wantB // ставки, що випали з пам'яті (понад 300), лишаються в БД
	} else {
		s.bids = map[string]string{}
	}
	s.lots = wantL

	// оброблені id
	for kind, list := range map[string][]string{"redeem": st.SeenRedeems, "don": st.DonSeen, "bid": au.SeenIDs} {
		want := make(map[string]bool, len(list))
		have := s.seen[kind]
		for _, id := range list {
			want[id] = true
			if !have[id] {
				ex(`INSERT INTO seen (channel_id, kind, id) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, s.ch, kind, id)
			}
		}
		for id := range have {
			if !want[id] {
				ex(`DELETE FROM seen WHERE channel_id = ? AND kind = ? AND id = ?`, s.ch, kind, id)
			}
		}
		s.seen[kind] = want
	}

	if err != nil {
		s.resetCache() // наступний Sync перепише все, що могло не записатися
		return err
	}
	if err = tx.Commit(); err != nil {
		s.resetCache()
	}
	return err
}

func auctionID(start int64) string {
	if start == 0 {
		start = time.Now().UnixMilli()
	}
	return time.UnixMilli(start).Format("2006-01-02_15-04-05")
}

func (s *Store) newAuctionID(tx *sql.Tx, start int64) string {
	base := auctionID(start)
	id := base
	for i := 2; ; i++ {
		var n int
		_ = tx.QueryRow(s.rb(`SELECT COUNT(*) FROM auctions WHERE channel_id = ? AND id = ?`), s.ch, id).Scan(&n)
		if n == 0 {
			return id
		}
		id = base + "-" + strconv.Itoa(i)
	}
}

// ---------- читання ----------

// Load — останній стан каналу. ok=false — у БД ще нічого немає.
func (s *Store) Load() (st State, ok bool, err error) {
	var live string
	switch err = s.db.QueryRow(s.rb(`SELECT live FROM channels WHERE id = ?`), s.ch).Scan(&live); {
	case errors.Is(err, sql.ErrNoRows):
		return defaultState(), false, nil
	case err != nil:
		return defaultState(), false, err
	}
	st = defaultState()
	if err = json.Unmarshal([]byte(live), &st); err != nil {
		return defaultState(), false, err
	}
	if st.Stream.ID != "" {
		f, _ := s.streamFile(st.Stream.ID)
		st.Past = f.Collections
		st.Entries, err = s.entriesOf(st.Stream.ID, 0)
		if err != nil {
			return st, false, err
		}
	}
	if st.Auction.ID != "" {
		st.Auction.Lots, st.Auction.Feed, err = s.auctionLots(st.Auction.ID, 300)
		if err != nil {
			return st, false, err
		}
	}
	st.SeenRedeems, st.DonSeen, st.Auction.SeenIDs = s.seenList("redeem"), s.seenList("don"), s.seenList("bid")
	s.prime(&st)
	return st, true, nil
}

// запам'ятати завантажений стан як «уже записаний»
func (s *Store) prime(st *State) {
	s.resetCache()
	s.live = liveJSON(st)
	sid := st.Stream.ID
	if sid != "" {
		s.streams[sid] = true
	}
	for _, c := range st.Past {
		s.colls[sid+"/"+strconv.Itoa(c.N)] = cached{sid, collMeta(c)}
		for _, e := range c.Entries {
			s.entries[e.ID] = cached{sid, strconv.Itoa(c.N) + "|" + mustJSON(e)}
		}
	}
	for _, e := range st.Entries {
		s.entries[e.ID] = cached{sid, "0|" + mustJSON(e)}
	}
	au := &st.Auction
	if aid := au.ID; aid != "" {
		s.auctions[aid] = mustJSON([]any{au.Timer.StartedAt, au.Timer.FinishedAt, au.Winner})
		for i, l := range au.Lots {
			s.lots[aid+"/"+l.ID] = cached{aid, strconv.Itoa(i) + "|" + mustJSON(l)}
		}
		for _, b := range au.Feed {
			s.bids[aid+"/"+b.ID] = mustJSON(b)
		}
	}
	for kind, list := range map[string][]string{"redeem": st.SeenRedeems, "don": st.DonSeen, "bid": au.SeenIDs} {
		m := map[string]bool{}
		for _, id := range list {
			m[id] = true
		}
		s.seen[kind] = m
	}
}

func (s *Store) seenList(kind string) []string {
	out := []string{}
	rows, err := s.db.Query(s.rb(`SELECT id FROM seen WHERE channel_id = ? AND kind = ? ORDER BY `)+s.seq(), s.ch, kind)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) entriesOf(stream string, n int) ([]Entry, error) {
	out := []Entry{}
	rows, err := s.db.Query(s.rb(`SELECT data FROM entries WHERE channel_id = ? AND stream_id = ? AND coll_n = ? ORDER BY `)+s.seq(), s.ch, stream, n)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var e Entry
		if rows.Scan(&d) == nil && json.Unmarshal([]byte(d), &e) == nil {
			out = append(out, e)
		}
	}
	return out, rows.Err()
}

// streamFile — завершені збори стріму з замовленнями (поточний збір не входить).
func (s *Store) streamFile(id string) (StreamFile, error) {
	f := StreamFile{ID: id, Collections: []Collection{}}
	if err := s.db.QueryRow(s.rb(`SELECT started_at FROM streams WHERE channel_id = ? AND id = ?`), s.ch, id).Scan(&f.StartedAt); err != nil {
		return f, err
	}
	rows, err := s.db.Query(s.rb(`SELECT n, started_at, finished_at, winner, settings FROM collections WHERE channel_id = ? AND stream_id = ? ORDER BY n`), s.ch, id)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var c Collection
		var st, fin sql.NullInt64
		var win sql.NullString
		var set string
		if rows.Scan(&c.N, &st, &fin, &win, &set) != nil {
			continue
		}
		c.StartedAt, c.FinishedAt, c.Winner = st.Int64, fin.Int64, win.String
		_ = json.Unmarshal([]byte(set), &c.Settings)
		c.Settings.normalize()
		f.Collections = append(f.Collections, c)
	}
	rows.Close()
	for i := range f.Collections {
		f.Collections[i].Entries, _ = s.entriesOf(id, f.Collections[i].N)
	}
	return f, nil
}

// streamExists — чи є вже стрім з таким id (новий стрім ніколи не перезаписує старий)
func (s *Store) streamExists(id string) bool {
	var n int
	_ = s.db.QueryRow(s.rb(`SELECT COUNT(*) FROM streams WHERE channel_id = ? AND id = ?`), s.ch, id).Scan(&n)
	return n > 0
}

// streamMetas — усі стріми з кількістю завершених зборів і замовлень у них
func (s *Store) streamMetas() []StreamMeta {
	out := []StreamMeta{}
	rows, err := s.db.Query(s.rb(`SELECT s.id, s.started_at,
		(SELECT COUNT(*) FROM collections c WHERE c.channel_id = s.channel_id AND c.stream_id = s.id),
		(SELECT COUNT(*) FROM entries e WHERE e.channel_id = s.channel_id AND e.stream_id = s.id AND e.coll_n > 0)
		FROM streams s WHERE s.channel_id = ?`), s.ch)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var m StreamMeta
		if rows.Scan(&m.ID, &m.StartedAt, &m.Collections, &m.Orders) == nil {
			out = append(out, m)
		}
	}
	return out
}

// auctionLots — лоти аукціону за порядком і останні limit ставок (найновіші першими; limit 0 — усі)
func (s *Store) auctionLots(id string, limit int) ([]Lot, []Bid, error) {
	lots, feed := []Lot{}, []Bid{}
	rows, err := s.db.Query(s.rb(`SELECT data FROM lots WHERE channel_id = ? AND auction_id = ? ORDER BY ord`), s.ch, id)
	if err != nil {
		return lots, feed, err
	}
	for rows.Next() {
		var d string
		var l Lot
		if rows.Scan(&d) == nil && json.Unmarshal([]byte(d), &l) == nil {
			lots = append(lots, l)
		}
	}
	rows.Close()
	q := `SELECT data FROM bids WHERE channel_id = ? AND auction_id = ? ORDER BY ts DESC, ` + s.seq() + ` DESC`
	args := []any{s.ch, id}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err = s.db.Query(s.rb(q), args...)
	if err != nil {
		return lots, feed, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		var b Bid
		if rows.Scan(&d) == nil && json.Unmarshal([]byte(d), &b) == nil {
			feed = append(feed, b)
		}
	}
	return lots, feed, rows.Err()
}

// allAuctions — усі аукціони з лотами й усіма ставками (для експорту)
func (s *Store) allAuctions() []auctionRec {
	var out []auctionRec
	rows, err := s.db.Query(s.rb(`SELECT id, COALESCE(winner, '') FROM auctions WHERE channel_id = ? ORDER BY id`), s.ch)
	if err != nil {
		return out
	}
	var recs []auctionRec
	for rows.Next() {
		var r auctionRec
		if rows.Scan(&r.ID, &r.Winner) == nil {
			recs = append(recs, r)
		}
	}
	rows.Close()
	for _, r := range recs {
		r.Lots, r.Feed, _ = s.auctionLots(r.ID, 0)
		if len(r.Lots) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// allCollections — усі завершені збори всіх стрімів (для експорту)
func (s *Store) allCollections() []subdayColl {
	var out []subdayColl
	metas := s.streamMetas()
	sort.Slice(metas, func(i, j int) bool { return metas[i].ID < metas[j].ID })
	for _, m := range metas {
		f, err := s.streamFile(m.ID)
		if err != nil {
			continue
		}
		for _, c := range f.Collections {
			out = append(out, subdayColl{m.ID, c})
		}
	}
	return out
}

// ---------- перенесення з JSON-файлів попередніх версій ----------

// migrateFiles — один раз переносить subday_state.json, streams/*.json, history/*.json у БД.
// Файли не видаляються: перейменовуються на *.migrated. Повертає стан із subday_state.json (ok=false — файлу не було).
func (s *Store) migrateFiles(baseDir string) (State, bool, error) {
	if s.meta("migrated_from_files") != "" {
		return State{}, false, nil
	}
	stateFile := filepath.Join(baseDir, "subday_state.json")
	streamsDir, historyDir := filepath.Join(baseDir, "streams"), filepath.Join(baseDir, "history")
	_, errState := os.Stat(stateFile)
	hasState := errState == nil

	tx, err := s.db.Begin()
	if err != nil {
		return State{}, false, err
	}
	defer tx.Rollback()
	ex := func(q string, args ...any) {
		if err == nil {
			_, err = tx.Exec(s.rb(q), args...)
		}
	}
	addStream := func(id string, started int64, colls []Collection) {
		ex(`INSERT INTO streams (channel_id, id, started_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, s.ch, id, started)
		used, maxN := map[int]bool{}, 0
		for _, c := range colls {
			if c.N > maxN {
				maxN = c.N
			}
		}
		for _, c := range colls {
			if c.N < 1 || used[c.N] { // старі версії могли записати два збори з тим самим номером — не зливати їх
				maxN++
				c.N = maxN
			}
			used[c.N] = true
			ex(`INSERT INTO collections (channel_id, stream_id, n, started_at, finished_at, winner, settings) VALUES (?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT (channel_id, stream_id, n) DO UPDATE SET started_at = excluded.started_at, finished_at = excluded.finished_at,
					winner = excluded.winner, settings = excluded.settings`,
				s.ch, id, c.N, c.StartedAt, c.FinishedAt, c.Winner, mustJSON(c.Settings))
			for _, e := range c.Entries {
				ex(`INSERT INTO entries (channel_id, id, stream_id, coll_n, ts, login, text, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
						ON CONFLICT (channel_id, id) DO UPDATE SET stream_id = excluded.stream_id, coll_n = excluded.coll_n, ts = excluded.ts,
						login = excluded.login, text = excluded.text, data = excluded.data`,
					s.ch, e.ID, id, c.N, e.Ts, e.Login, e.Text, mustJSON(e))
			}
		}
	}

	files, _ := filepath.Glob(filepath.Join(streamsDir, "*.json"))
	for _, p := range files {
		var f StreamFile
		if b, e := os.ReadFile(p); e != nil || json.Unmarshal(b, &f) != nil {
			continue
		}
		if f.ID == "" {
			f.ID = strings.TrimSuffix(filepath.Base(p), ".json")
		}
		addStream(f.ID, f.StartedAt, f.Collections)
	}
	// архіви до появи стрімів: кожен — окремий стрім з одним збором
	old, _ := filepath.Glob(filepath.Join(historyDir, "subday_*.json"))
	for _, p := range old {
		var f struct {
			Entries  []Entry  `json:"entries"`
			Settings Settings `json:"settings"`
			Session  Timer    `json:"session"`
		}
		if b, e := os.ReadFile(p); e != nil || json.Unmarshal(b, &f) != nil || len(f.Entries) == 0 {
			continue
		}
		f.Settings.normalize()
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "subday_"), ".json")
		started := f.Session.StartedAt
		if t, e := time.ParseInLocation("2006-01-02_15-04-05", stamp, time.Local); e == nil && started == 0 {
			started = t.UnixMilli()
		}
		addStream(stamp+"-a", started, []Collection{{N: 1, StartedAt: f.Session.StartedAt, FinishedAt: f.Session.FinishedAt, Settings: f.Settings, Entries: f.Entries}})
	}
	auc, _ := filepath.Glob(filepath.Join(historyDir, "auction_*.json"))
	for _, p := range auc {
		var f struct {
			Lots   []Lot  `json:"lots"`
			Feed   []Bid  `json:"feed"`
			Winner string `json:"winner"`
		}
		if b, e := os.ReadFile(p); e != nil || json.Unmarshal(b, &f) != nil || len(f.Lots) == 0 {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "auction_"), ".json")
		var started int64
		if t, e := time.ParseInLocation("2006-01-02_15-04-05", id, time.Local); e == nil {
			started = t.UnixMilli()
		}
		ex(`INSERT INTO auctions (channel_id, id, started_at, finished_at, winner) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (channel_id, id) DO UPDATE SET started_at = excluded.started_at, finished_at = excluded.finished_at, winner = excluded.winner`, s.ch, id, started, 0, f.Winner)
		for i, l := range f.Lots {
			ex(`INSERT INTO lots (channel_id, auction_id, id, ord, name, amount, data) VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (channel_id, auction_id, id) DO UPDATE SET ord = excluded.ord, name = excluded.name, amount = excluded.amount, data = excluded.data`, s.ch, id, l.ID, i, l.Name, l.Amount, mustJSON(l))
		}
		for _, b := range f.Feed {
			ex(`INSERT INTO bids (channel_id, auction_id, id, ts, amount, data) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (channel_id, auction_id, id) DO UPDATE SET ts = excluded.ts, amount = excluded.amount, data = excluded.data`, s.ch, id, b.ID, b.Ts, b.Amount, mustJSON(b))
		}
	}
	ex(`INSERT INTO meta (key, value) VALUES ('migrated_from_files', ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, time.Now().Format(time.RFC3339))
	if err != nil {
		return State{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return State{}, false, err
	}

	var st State
	if hasState {
		st = loadState(stateFile)
		// поточний аукціон отримує id за часом старту — так його файл з history/ і запис з subday_state.json збігаються
		if len(st.Auction.Lots) > 0 && st.Auction.ID == "" {
			st.Auction.ID = auctionID(st.Auction.Timer.StartedAt)
		}
	}
	for _, p := range []string{stateFile, streamsDir, historyDir} {
		if _, e := os.Stat(p); e == nil {
			_ = os.Rename(p, p+".migrated")
		}
	}
	return st, hasState, nil
}
