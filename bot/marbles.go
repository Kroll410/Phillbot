package main

// Консольні команди + експорт у Marbles on Stream.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	marblesAppID   = "1170970"
	marbleCap      = 1000
	marbleNameMax  = 30
	subdayKeyword  = "сабдей" // нагороди, в назві яких є це слово, йдуть у СабДей
	subdayKeyword2 = "subday"
)

type sessionEntry struct {
	ts       time.Time
	rewardID string
	reward   string
	userID   string
	userName string
	text     string
	tier     int
	months   int
	points   int
}

var (
	session   []sessionEntry
	sessionMu sync.Mutex
)

func recordRedeem(ev Redemption, tier, months, points int) {
	sessionMu.Lock()
	session = append(session, sessionEntry{time.Now(), ev.Reward.ID, ev.Reward.Title, ev.UserID, ev.UserName, ev.UserInput, tier, months, points})
	sessionMu.Unlock()
}

func isSubdayReward(title string) bool {
	t := strings.ToLower(title)
	return strings.Contains(t, subdayKeyword) || strings.Contains(t, subdayKeyword2)
}

type orderGroup struct {
	name   string
	points int
	users  []string
}

var trailPunct = regexp.MustCompile(`[.!?,;:…]+$`)

func normKey(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimSpace(trailPunct.ReplaceAllString(s, ""))
}

// Від кожного чатерса рахується лише ОСТАННЄ замовлення; однакові замовлення зливаються.
func aggregateSession() (groups []orderGroup, used int, filteredByTitle bool) {
	sessionMu.Lock()
	entries := append([]sessionEntry(nil), session...)
	sessionMu.Unlock()

	var sub []sessionEntry
	for _, e := range entries {
		if isSubdayReward(e.reward) {
			sub = append(sub, e)
		}
	}
	if len(sub) > 0 {
		entries, filteredByTitle = sub, true
	}
	last := map[string]sessionEntry{}
	for _, e := range entries {
		if strings.TrimSpace(e.text) != "" {
			last[e.userID] = e
		}
	}
	byKey := map[string]*orderGroup{}
	variants := map[string]map[string]int{}
	for _, e := range last {
		k := normKey(e.text)
		if k == "" {
			continue
		}
		g, ok := byKey[k]
		if !ok {
			g = &orderGroup{}
			byKey[k] = g
			variants[k] = map[string]int{}
		}
		g.points += e.points
		g.users = append(g.users, fmt.Sprintf("%s(%d)", e.userName, e.points))
		variants[k][strings.TrimSpace(e.text)]++
		used++
	}
	for k, g := range byKey {
		best, bc := "", -1
		for v, c := range variants[k] {
			if c > bc || (c == bc && v < best) {
				best, bc = v, c
			}
		}
		g.name = best
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].points > groups[j].points })
	return
}

func cleanMarbleName(s string) string {
	s = strings.NewReplacer(",", " ", ";", " ", "\"", " ", "\t", " ", "\r", " ", "\n", " ").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > marbleNameMax {
		r = r[:marbleNameMax]
	}
	return strings.TrimSpace(string(r))
}

// Кожен бал = одна куля. Якщо балів більше за ліміт гри (1000) — пропорційно зменшуємо (мінімум 1 куля).
func marbleLines(groups []orderGroup) (lines []string, total int, scaled bool) {
	var gs []orderGroup
	for _, g := range groups {
		if g.points > 0 && cleanMarbleName(g.name) != "" {
			gs = append(gs, g)
			total += g.points
		}
	}
	counts := make([]int, len(gs))
	for i, g := range gs {
		counts[i] = g.points
	}
	if total > marbleCap {
		scaled = true
		sum := 0
		for i, g := range gs {
			counts[i] = int(math.Max(1, math.Round(float64(g.points)*marbleCap/float64(total))))
			sum += counts[i]
		}
		for sum > marbleCap {
			mi := 0
			for i := range counts {
				if counts[i] > counts[mi] {
					mi = i
				}
			}
			if counts[mi] <= 1 {
				break
			}
			counts[mi]--
			sum--
		}
	}
	for i, g := range gs {
		n := cleanMarbleName(g.name)
		for k := 0; k < counts[i] && len(lines) < marbleCap; k++ {
			lines = append(lines, n)
		}
	}
	rand.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	return
}

// UTF-8 з BOM за замовчуванням; "utf16" — UTF-16 LE з BOM (на випадок, якщо кирилиця в грі покаже кракозябри).
func writeMarblesFile(lines []string, enc string) (string, error) {
	dir := filepath.Join(baseDir, "marbles")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	text := strings.Join(lines, "\r\n") + "\r\n"
	var data []byte
	if enc == "utf16" {
		u := utf16.Encode([]rune(text))
		data = []byte{0xFF, 0xFE}
		for _, c := range u {
			data = append(data, byte(c), byte(c>>8))
		}
	} else {
		data = append([]byte{0xEF, 0xBB, 0xBF}, []byte(text)...)
	}
	p := filepath.Join(dir, "subday_marbles.csv")
	if err := os.WriteFile(p, data, 0644); err != nil {
		return "", err
	}
	_ = os.WriteFile(filepath.Join(dir, "subday_marbles_"+time.Now().Format("2006-01-02_15-04-05")+".csv"), data, 0644)
	return p, nil
}

func marblesRunning() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(out)), "marbles")
}

func launchMarbles() {
	if runtime.GOOS == "windows" {
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", "steam://rungameid/"+marblesAppID).Start()
	} else {
		openBrowser("steam://rungameid/" + marblesAppID)
	}
}

func printSummary(groups []orderGroup, used int, filtered bool) {
	printMu.Lock()
	defer printMu.Unlock()
	fmt.Println()
	if len(groups) == 0 {
		fmt.Println(yellow("   Замовлень поки немає."))
		return
	}
	if !filtered {
		fmt.Println(yellow("   Нагород зі словом «СабДей» у назві не знайдено — враховано всі нагороди з текстом."))
	}
	fmt.Println(bold(fmt.Sprintf("   Підсумок: %d замовлень від %d чатерсів (від кожного — останнє)", len(groups), used)))
	for i, g := range groups {
		fmt.Printf("   %2d. %s %s  %s\n", i+1, green(fmt.Sprintf("%5d", g.points)), bold(g.name), dim(strings.Join(g.users, ", ")))
	}
}

func cmdMarbles(enc string) {
	groups, used, filtered := aggregateSession()
	printSummary(groups, used, filtered)
	lines, total, scaled := marbleLines(groups)
	if len(lines) == 0 {
		fmt.Println(yellow("   Немає замовлень з балами > 0 — файл не створено."))
		return
	}
	p, err := writeMarblesFile(lines, enc)
	if err != nil {
		fmt.Println(red("   Не вдалося записати файл: " + err.Error()))
		return
	}
	copied := setClipboard(p)
	fmt.Println()
	fmt.Println(green("   ✓ Файл для Marbles: ") + cyan(p))
	info := fmt.Sprintf("   %d куль", len(lines))
	if scaled {
		info += fmt.Sprintf(" (зменшено пропорційно з %d балів до ліміту гри %d)", total, marbleCap)
	}
	fmt.Println(info)
	if copied {
		fmt.Println(green("   ✓ Шлях до файлу скопійовано в буфер обміну"))
	}
	if marblesRunning() {
		fmt.Println(dim("   Marbles on Stream уже запущено."))
	} else {
		fmt.Println("   Запускаю Marbles on Stream через Steam…")
		launchMarbles()
	}
	fmt.Println(bold("\n   У грі:") + " Race → Simulate → ⚙ Settings (праворуч унизу) → Simulation Settings →")
	fmt.Println("   увімкнути Use Custom Names → Add Names → Open → " + yellow("Ctrl+V") + " у полі «Ім'я файлу» → Enter.")
	fmt.Printf("   Потім гонка Bots на %s.\n", bold(fmt.Sprintf("%d куль", len(lines))))
	if enc != "utf16" {
		fmt.Println(dim("   Якщо кирилиця в грі відображається кракозябрами — введіть: marbles utf16"))
	}
}

func cmdHelp() {
	fmt.Println(bold("\n   Команди:"))
	fmt.Println("   " + cyan("marbles") + "        — зібрати .csv для Marbles, запустити гру, скопіювати шлях")
	fmt.Println("   " + cyan("marbles utf16") + "  — те саме, але файл у кодуванні UTF-16")
	fmt.Println("   " + cyan("list") + "           — підсумок замовлень")
	fmt.Println("   " + cyan("reset") + "          — очистити замовлення (почати новий збір)")
	fmt.Println("   " + cyan("folder") + "         — відкрити папку з файлами")
	fmt.Println("   " + cyan("help") + "           — ця довідка")
}

func commandLoop() {
	for {
		line, err := stdin.ReadString('\n')
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		f := strings.Fields(strings.ToLower(line))
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "marbles", "marble", "m":
			enc := ""
			if len(f) > 1 && (f[1] == "utf16" || f[1] == "utf-16") {
				enc = "utf16"
			}
			cmdMarbles(enc)
		case "list", "l":
			g, u, fl := aggregateSession()
			printSummary(g, u, fl)
		case "reset":
			sessionMu.Lock()
			n := len(session)
			session = nil
			sessionMu.Unlock()
			logf("Очищено %d записів. Новий збір почато.", n)
		case "folder", "dir":
			if runtime.GOOS == "windows" {
				_ = exec.Command("explorer", baseDir).Start()
			}
		case "help", "?", "h":
			cmdHelp()
		default:
			fmt.Println(dim("   Невідома команда. Введіть help"))
		}
	}
}
