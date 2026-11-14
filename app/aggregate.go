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

// giftPoints — бали за подаровані сабки за 30 днів: 2 сабки = 1 бал, остача не рахується (2 → 1, 5 → 2)
func giftPoints(gifts int) int {
	if gifts < 0 {
		return 0
	}
	return gifts / 2
}

// subPoints — бали за підписку: (множник тіру × місяці) / 3 з округленням угору (1 → 1, 4 → 2, 30 → 10, 35 → 12)
func subPoints(mult, months int) int {
	p := mult * months
	if p <= 0 {
		return 0
	}
	return (p + 2) / 3
}

func pointsOf(s Settings, e Entry) int {
	if e.Donation { // донат Donatello — бали вже з гривень
		return e.DonPoints
	}
	if e.Tier == 0 {
		return s.NonSubPoints + giftPoints(e.Gifts)
	}
	m := e.Months
	if m < 1 {
		m = 1
	}
	return subPoints(s.Mult[strconv.Itoa(e.Tier)], m) + giftPoints(e.Gifts)
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

// Яке замовлення кожного чатерса «діє» (last — останнє, first — перше, split — усі), враховуючи й приховані.
// Старіші замовлення того ж чатерса (у last/first) не показуються в таблиці й не рахуються.
func effectiveIDs(s Settings, entries []Entry) map[string]bool {
	out := map[string]bool{}
	if s.DupMode == "split" {
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

// Бали кожного врахованого замовлення.
// last/first — бали за тіром і стажем; split — бали глядача діляться між усіма його врахованими замовленнями:
// 10 балів на 3 замовлення = 3, 3, 4 (залишок дістається найсвіжішим).
func entryPoints(s Settings, entries []Entry) map[string]int {
	counted := countedIDs(s, entries)
	out := map[string]int{}
	if s.DupMode != "split" {
		for _, e := range entries {
			if counted[e.ID] {
				out[e.ID] = pointsOf(s, e)
			}
		}
		return out
	}
	sorted := append([]Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Ts < sorted[j].Ts })
	byUser := map[string][]Entry{}
	var users []string
	for _, e := range sorted {
		if !counted[e.ID] {
			continue
		}
		k := userKey(e)
		if _, ok := byUser[k]; !ok {
			users = append(users, k)
		}
		byUser[k] = append(byUser[k], e)
	}
	for _, k := range users {
		list := byUser[k]
		total := pointsOf(s, list[len(list)-1]) // бали глядача — за його найсвіжішим замовленням (тір і стаж)
		n := len(list)
		base, rem := total/n, total%n
		for i, e := range list {
			p := base
			if i >= n-rem {
				p++
			}
			out[e.ID] = p
		}
	}
	return out
}

// Групи замовлень, відсортовані за балами (спадання).
func aggregate(s Settings, entries []Entry) []Group {
	counted := countedIDs(s, entries)
	pts := entryPoints(s, entries)
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
		p := pts[e.ID]
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
