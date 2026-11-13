package main

// Модель «стрім → збори».
// СабДей — це стрім. Кожен «Старт» відкриває наступний збір замовлень цього стріму (Збір 1, Збір 2…).
// Новий стрім починається автоматично, якщо від останньої активності минуло понад streamGap, або вручну (NewStream).
//
// Збереження:
//   streams/<id стріму>.json            — увесь стрім: усі збори з налаштуваннями балів і замовленнями;
//   exports/<id стріму>/збір_NN.csv     — таблиця кожного збору для Excel (UTF-8 з BOM, роздільник «;»).
// Файли перезаписуються, коли збір завершується, і востаннє — перед наступним «Старт» (з правками після завершення).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const streamGap = 6 * time.Hour

type StreamInfo struct {
	ID        string `json:"id"`
	StartedAt int64  `json:"startedAt"`
}

// Collection — завершений (або архівований) збір замовлень.
type Collection struct {
	N          int      `json:"n"`
	StartedAt  int64    `json:"startedAt"`
	FinishedAt int64    `json:"finishedAt"`
	Settings   Settings `json:"settings"` // правила балів на момент збору
	Entries    []Entry  `json:"entries"`
	Winner     string   `json:"winner,omitempty"`
}

type StreamFile struct {
	ID          string       `json:"id"`
	StartedAt   int64        `json:"startedAt"`
	Collections []Collection `json:"collections"`
}

type StreamMeta struct {
	ID          string `json:"id"`
	StartedAt   int64  `json:"startedAt"`
	Collections int    `json:"collections"`
	Orders      int    `json:"orders"`
	Current     bool   `json:"current"`
}

func newStreamInfo(t time.Time) StreamInfo {
	return StreamInfo{ID: t.Format("2006-01-02_15-04-05"), StartedAt: t.UnixMilli()}
}

// остання активність стріму: старт/завершення збору, останнє замовлення
func (a *App) lastActivityLocked() int64 {
	st := &a.st
	last := st.Stream.StartedAt
	for _, v := range []int64{st.Session.StartedAt, st.Session.FinishedAt} {
		if v > last {
			last = v
		}
	}
	for _, e := range st.Entries {
		if e.Ts > last {
			last = e.Ts
		}
	}
	for _, c := range st.Past {
		if c.FinishedAt > last {
			last = c.FinishedAt
		}
	}
	return last
}

// поточний збір як Collection (для збереження)
func (a *App) currentCollectionLocked() Collection {
	return Collection{N: a.st.CollN, StartedAt: a.st.Session.StartedAt, FinishedAt: a.st.Session.FinishedAt,
		Settings: a.st.Settings, Entries: append([]Entry(nil), a.st.Entries...), Winner: a.st.Winner}
}

func csvCell(v string) string {
	if strings.ContainsAny(v, "\";\r\n") {
		return "\"" + strings.ReplaceAll(v, "\"", "\"\"") + "\""
	}
	return v
}

func collectionCSV(s Settings, entries []Entry) string {
	var b strings.Builder
	line := func(cells ...string) {
		for i, c := range cells {
			if i > 0 {
				b.WriteString(";")
			}
			b.WriteString(csvCell(c))
		}
		b.WriteString("\r\n")
	}
	b.WriteString("sep=;\r\n")
	counted, pts := countedIDs(s, entries), entryPoints(s, entries)
	line("Час", "Чатерс", "Логін", "Тір", "Місяці", "Бали", "Замовлення", "Рахується")
	for _, e := range entries {
		tier, months := "—", ""
		if e.Tier > 0 {
			tier, months = strconv.Itoa(e.Tier), strconv.Itoa(e.Months)
		}
		status := "так"
		if e.Excluded {
			status = "приховано"
		} else if !counted[e.ID] {
			status = "ні (дубль)"
		}
		line(time.UnixMilli(e.Ts).Format("2006-01-02 15:04:05"), e.User, e.Login, tier, months, strconv.Itoa(csvPoints(s, e, counted, pts)), e.Text, status)
	}
	b.WriteString("\r\nПідсумок\r\n")
	line("Замовлення", "Бали", "Людей", "Хто")
	for _, g := range aggregate(s, entries) {
		var who []string
		for _, u := range g.Users {
			who = append(who, u.Name+" ("+strconv.Itoa(u.P)+")")
		}
		line(g.Name, strconv.Itoa(g.Points), strconv.Itoa(g.Count), strings.Join(who, ", "))
	}
	return b.String()
}

// CSV поточного збору: exports/<стрім>/збір_NN.csv. Викликати під a.mu. Порожній збір не зберігається.
func (a *App) writeCSVLocked() string {
	if len(a.st.Entries) == 0 || a.st.Stream.ID == "" {
		return ""
	}
	dir := filepath.Join(a.exportsDir, a.st.Stream.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}
	p := filepath.Join(dir, fmt.Sprintf("збір_%02d.csv", a.st.CollN))
	data := append([]byte{0xEF, 0xBB, 0xBF}, []byte(collectionCSV(a.st.Settings, a.st.Entries))...)
	if err := os.WriteFile(p, data, 0644); err != nil {
		go a.logf("Не вдалося записати %s — можливо, файл відкритий в Excel", p)
		return ""
	}
	return p
}

// streams/<стрім>.json — усі збори стріму, включно з поточним (якщо в ньому є замовлення).
func (a *App) writeStreamLocked() {
	if a.st.Stream.ID == "" {
		return
	}
	f := StreamFile{ID: a.st.Stream.ID, StartedAt: a.st.Stream.StartedAt, Collections: append([]Collection(nil), a.st.Past...)}
	if len(a.st.Entries) > 0 {
		f.Collections = append(f.Collections, a.currentCollectionLocked())
	}
	if len(f.Collections) == 0 {
		return
	}
	if err := os.MkdirAll(a.streamsDir, 0755); err != nil {
		return
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	_ = writeFileAtomic(filepath.Join(a.streamsDir, a.st.Stream.ID+".json"), b)
}

// зберегти поточний збір (CSV + файл стріму). Викликати під a.mu.
func (a *App) persistCollectionLocked() string {
	a.initStreamLocked() // замовлення без стріму (старий стан / додано вручну до старту)
	if a.st.CollN == 0 && len(a.st.Entries) > 0 {
		a.st.CollN = len(a.st.Past) + 1
	}
	csv := a.writeCSVLocked()
	a.writeStreamLocked()
	return csv
}

// Перенести поточний збір у минулі збори стріму (після остаточного збереження). Викликати під a.mu.
func (a *App) archiveLocked() string {
	if len(a.st.Entries) == 0 {
		return ""
	}
	csv := a.persistCollectionLocked()
	a.st.Past = append(a.st.Past, a.currentCollectionLocked())
	return csv
}

// Почати новий стрім (попередній уже збережено). Викликати під a.mu.
func (a *App) beginStreamLocked(now time.Time) {
	info := newStreamInfo(now)
	base := info.ID
	for i := 2; info.ID == a.st.Stream.ID || fileExists(filepath.Join(a.streamsDir, info.ID+".json")); i++ {
		info.ID = base + "-" + strconv.Itoa(i) // ніколи не перезаписувати інший стрім
	}
	a.st.Stream = info
	a.st.Past = nil
	a.st.CollN = 0
}

// Перед новим збором: якщо стріму немає або була довга перерва — новий стрім. Викликати під a.mu.
func (a *App) ensureStreamLocked(now time.Time) {
	if a.st.Stream.ID == "" || now.Sub(time.UnixMilli(a.lastActivityLocked())) > streamGap {
		a.beginStreamLocked(now)
	}
}

// Старі стани (до появи стрімів): поточний збір стає «Збір 1» нового стріму.
func (a *App) initStreamLocked() {
	if a.st.Stream.ID != "" {
		return
	}
	t := time.Now()
	if a.st.Session.StartedAt > 0 {
		t = time.UnixMilli(a.st.Session.StartedAt)
	}
	a.beginStreamLocked(t)
	if len(a.st.Entries) > 0 || a.st.Session.Status != "idle" {
		a.st.CollN = 1
	}
}

// ---------- методи для інтерфейсу ----------

// NewStream — завершити поточний стрім і почати новий (поточний збір зберігається в старий стрім).
func (a *App) NewStream() Snapshot {
	return a.mutate(func(st *State) {
		s := &st.Session
		if s.Status == "running" || s.Status == "paused" {
			s.Status, s.RemainingMs, s.FinishedAt = "finished", 0, time.Now().UnixMilli()
		}
		a.archiveLocked()
		a.writeStreamLocked()
		a.endPlayLocked()
		st.Entries = []Entry{}
		st.SeenRedeems = []string{}
		st.Winner, st.Exported, st.Phase, st.Round = "", false, phaseCollect, 0
		st.Vote = VoteState{Status: "idle"}
		a.lastOrd = nil
		a.beginStreamLocked(time.Now())
		st.Session = Timer{Status: "idle", RemainingMs: int64(st.Settings.DurationMin) * 60000}
	})
}

var streamIDRe = regexp.MustCompile(`^[0-9_-]{8,32}$`)

// ListStreams — усі збережені стріми (найновіші зверху), включно з поточним.
func (a *App) ListStreams() []StreamMeta {
	a.mu.Lock()
	cur := a.st.Stream
	curColls, curOrders := len(a.st.Past), 0
	for _, c := range a.st.Past {
		curOrders += len(c.Entries)
	}
	if len(a.st.Entries) > 0 {
		curColls++
		curOrders += len(a.st.Entries)
	}
	a.mu.Unlock()

	out := []StreamMeta{}
	seen := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(a.streamsDir, "*.json"))
	for _, p := range files {
		id := strings.TrimSuffix(filepath.Base(p), ".json")
		if !streamIDRe.MatchString(id) || id == cur.ID {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f StreamFile
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		m := StreamMeta{ID: id, StartedAt: f.StartedAt, Collections: len(f.Collections)}
		for _, c := range f.Collections {
			m.Orders += len(c.Entries)
		}
		out = append(out, m)
		seen[id] = true
	}
	if cur.ID != "" {
		out = append(out, StreamMeta{ID: cur.ID, StartedAt: cur.StartedAt, Collections: curColls, Orders: curOrders, Current: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

// GetStream — збори минулого стріму для перегляду.
func (a *App) GetStream(id string) (StreamFile, error) {
	if !streamIDRe.MatchString(id) {
		return StreamFile{}, errors.New("невідомий стрім")
	}
	b, err := os.ReadFile(filepath.Join(a.streamsDir, id+".json"))
	if err != nil {
		return StreamFile{}, errors.New("файл стріму не знайдено")
	}
	var f StreamFile
	if err := json.Unmarshal(b, &f); err != nil {
		return StreamFile{}, err
	}
	return f, nil
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// бали в CSV: враховане замовлення — за правилом (у split — частка), неврахованe — скільки дало б саме
func csvPoints(s Settings, e Entry, counted map[string]bool, pts map[string]int) int {
	if counted[e.ID] {
		return pts[e.ID]
	}
	return pointsOf(s, e)
}
