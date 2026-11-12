package main

// Підрахунок балів, злиття замовлень і кількість куль — та сама логіка, що у frontend/dist/app.js.
// Потрібна в Go для оверлеїв OBS.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type UserPts struct {
	Name string `json:"name"`
	P    int    `json:"p"`
}

type Group struct {
	Name   string    `json:"name"`
	Points int       `json:"points"`
	Count  int       `json:"count"`
	Users  []UserPts `json:"users"`
}

var trailPunct = regexp.MustCompile(`[.!?,;:…]+$`)

func normKey(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimSpace(trailPunct.ReplaceAllString(s, ""))
}

func pointsOf(s Settings, e Entry) int {
	if e.Donation { // донат Donatello — бали вже з гривень
		return e.DonPoints
	}
	if e.Tier == 0 {
		return s.NonSubPoints
	}
	m := e.Months
	if m < 1 {
		m = 1
	}
	return s.Mult[strconv.Itoa(e.Tier)] * m
}

func userKey(e Entry) string {
	if e.UserID != "" {
		return strings.ToLower(e.UserID)
	}
	if e.Login != "" {
		return strings.ToLower(e.Login)
	}
	return strings.ToLower(e.User)
}

// Які замовлення враховуються з урахуванням правила дублікатів (last | first | all).
// Яке замовлення кожного чатерса «діє» (last — останнє, first — перше, all — усі), враховуючи й приховані.
// Старіші замовлення того ж чатерса не показуються в таблиці й не рахуються.
func effectiveIDs(s Settings, entries []Entry) map[string]bool {
	out := map[string]bool{}
	if s.DupMode == "all" {
		for _, e := range entries {
			out[e.ID] = true
		}
		return out
	}
	sorted := append([]Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Ts < sorted[j].Ts })
	byUser := map[string]string{}
	for _, e := range sorted {
		k := userKey(e)
		if _, ok := byUser[k]; ok && s.DupMode == "first" {
			continue
		}
		byUser[k] = e.ID
	}
	for _, id := range byUser {
		out[id] = true
	}
	return out
}

// Які замовлення рахуються: «діюче» замовлення чатерса, якщо воно не приховане.
// Якщо приховати останнє замовлення — від цього чатерса не рахується нічого.
func countedIDs(s Settings, entries []Entry) map[string]bool {
	eff := effectiveIDs(s, entries)
	out := map[string]bool{}
	for _, e := range entries {
		if eff[e.ID] && !e.Excluded {
			out[e.ID] = true
		}
	}
	return out
}

// Групи замовлень, відсортовані за балами (спадання).
func aggregate(s Settings, entries []Entry) []Group {
	counted := countedIDs(s, entries)
	type acc struct {
		g        Group
		variants map[string]int
		order    []string
	}
	byKey := map[string]*acc{}
	var keys []string
	for _, e := range entries {
		if !counted[e.ID] {
			continue
		}
		k := normKey(e.Text)
		if k == "" {
			continue
		}
		a, ok := byKey[k]
		if !ok {
			a = &acc{variants: map[string]int{}}
			byKey[k] = a
			keys = append(keys, k)
		}
		p := pointsOf(s, e)
		a.g.Points += p
		a.g.Count++
		a.g.Users = append(a.g.Users, UserPts{e.User, p})
		v := strings.TrimSpace(e.Text)
		if _, seen := a.variants[v]; !seen {
			a.order = append(a.order, v)
		}
		a.variants[v]++
	}
	groups := make([]Group, 0, len(keys))
	for _, k := range keys {
		a := byKey[k]
		best, bc := "", -1
		for _, v := range a.order { // при рівності — перший за часом варіант
			if a.variants[v] > bc {
				best, bc = v, a.variants[v]
			}
		}
		a.g.Name = best
		sort.SliceStable(a.g.Users, func(i, j int) bool { return a.g.Users[i].P > a.g.Users[j].P })
		groups = append(groups, a.g)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Points > groups[j].Points })
	return groups
}

// Скільки куль буде в Marbles: кожен бал = одна куля, без обмежень (сума куль = сума балів).
func marbleCounts(groups []Group) (counts []int, total int) {
	for _, g := range groups {
		if g.Points > 0 && marbleName(g.Name) != "" {
			counts = append(counts, g.Points)
			total += g.Points
		}
	}
	return
}

func sumInts(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}
