package main

// Експорт: файл для Marbles on Stream, таблиця для Excel (CSV з BOM), JSON.

import (
	"errors"
	"math/rand"
	"regexp"
	"strings"
)

const marblesAppID = "1170970"

type MarblesResult struct {
	Path           string `json:"path"`
	Dir            string `json:"dir"`
	Lines          int    `json:"lines"`
	Copied         bool   `json:"copied"`
	AlreadyRunning bool   `json:"alreadyRunning"`
	Launched       bool   `json:"launched"`
}

const marbleNameMax = 15 // довші назви обрізаються і закінчуються на "..."

// Файл для Marbles — завжди UTF-8 з BOM (кирилиця).
func encodeLines(lines []string) []byte {
	text := strings.Join(lines, "\r\n") + "\r\n"
	return append([]byte{0xEF, 0xBB, 0xBF}, []byte(text)...)
}

// Назва кулі = назва замовлення: без , ; " і переносів, максимум 15 символів, далі "...".
func marbleName(s string) string {
	s = strings.NewReplacer(",", " ", ";", " ", "\"", " ", "\t", " ", "\r", " ", "\n", " ").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > marbleNameMax {
		return strings.TrimSpace(string(r[:marbleNameMax])) + "..."
	}
	return s
}

// Кожен бал = одна куля з назвою замовлення; куль рівно стільки, скільки балів. Перемішано.
func marbleLines(s Settings, entries []Entry) []string {
	groups := aggregate(s, entries)
	var gs []Group
	for _, g := range groups {
		if g.Points > 0 && marbleName(g.Name) != "" {
			gs = append(gs, g)
		}
	}
	counts, _ := marbleCounts(gs)
	var lines []string
	for i, g := range gs {
		n := marbleName(g.Name)
		for k := 0; k < counts[i]; k++ {
			lines = append(lines, n)
		}
	}
	rand.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	return lines
}

func (a *Channel) MarbleLines() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return marbleLines(a.st.Settings, a.st.Entries)
}

var badName = regexp.MustCompile(`[^\p{L}\p{N}_\-.]+`)

// marblesFile — вміст файлу для Marbles (UTF-8 з BOM, рядок на кулю); після нього можна вибирати переможця.
// Вебверсія віддає його на завантаження (десктоп пише файл сам — ExportMarbles).
func (a *Channel) marblesFile() ([]byte, int, error) {
	clean := a.MarbleLines()
	if len(clean) == 0 {
		return nil, 0, errors.New("немає замовлень з балами > 0")
	}
	a.mu.Lock()
	if !a.st.Exported {
		a.st.Exported = true
		a.saveLocked()
		a.emitLocked()
	}
	a.mu.Unlock()
	a.logf("Файл для Marbles завантажено (%d куль)", len(clean))
	return encodeLines(clean), len(clean), nil
}
