package main

// Експорт: файл для Marbles on Stream, таблиця для Excel (CSV з BOM), JSON.

import (
	"errors"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
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

func (a *App) MarbleLines() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return marbleLines(a.st.Settings, a.st.Entries)
}

// Записує subday_marbles.csv (+ копію з датою), кладе шлях у буфер обміну і запускає гру через Steam.
func (a *App) ExportMarbles(launch bool) (MarblesResult, error) {
	clean := a.MarbleLines()
	if len(clean) == 0 {
		return MarblesResult{}, errors.New("немає замовлень з балами > 0")
	}
	// marbles/last — тека, де лежить ЛИШЕ файл останнього збору; її шлях копіюється в буфер.
	// Копії всіх експортів з датою — у marbles/history.
	dir := filepath.Join(a.baseDir, "marbles", "last")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return MarblesResult{}, err
	}
	if old, err := os.ReadDir(dir); err == nil {
		for _, f := range old {
			if f.Name() != "subday_marbles.csv" {
				_ = os.RemoveAll(filepath.Join(dir, f.Name()))
			}
		}
	}
	data := encodeLines(clean)
	p := filepath.Join(dir, "subday_marbles.csv")
	if err := os.WriteFile(p, data, 0644); err != nil {
		return MarblesResult{}, errors.New("не вдалося записати файл (" + err.Error() + ") — можливо, він відкритий в іншій програмі")
	}
	hist := filepath.Join(a.baseDir, "marbles", "history")
	if os.MkdirAll(hist, 0755) == nil {
		_ = os.WriteFile(filepath.Join(hist, "subday_marbles_"+time.Now().Format("2006-01-02_15-04-05")+".csv"), data, 0644)
	}
	res := MarblesResult{Path: p, Dir: dir, Lines: len(clean), Copied: setClipboard(dir)}
	if launch {
		if marblesRunning() {
			res.AlreadyRunning = true
		} else {
			openURL("steam://rungameid/" + marblesAppID)
			res.Launched = true
		}
	}
	a.logf("Файл для Marbles: %s (%d куль)", p, len(clean))
	return res, nil
}

var badName = regexp.MustCompile(`[^\p{L}\p{N}_\-.]+`)

// Зберігає текст у exports/ (CSV — з BOM, щоб Excel правильно показав кирилицю).
func (a *App) SaveExport(name, content string) (string, error) {
	name = badName.ReplaceAllString(name, "_")
	if name == "" {
		name = "subday_" + time.Now().Format("2006-01-02_15-04")
	}
	if err := os.MkdirAll(a.exportsDir, 0755); err != nil {
		return "", err
	}
	p := filepath.Join(a.exportsDir, name)
	data := []byte(content)
	if strings.HasSuffix(strings.ToLower(name), ".csv") {
		data = append([]byte{0xEF, 0xBB, 0xBF}, data...)
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		return "", errors.New("не вдалося записати " + p + " — можливо, файл відкритий в Excel")
	}
	return p, nil
}

func (a *App) OpenFolder(sub string) {
	dir := a.baseDir
	switch sub {
	case "marbles", "exports", "history":
		dir = filepath.Join(a.baseDir, sub)
		_ = os.MkdirAll(dir, 0755)
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("explorer", dir).Start()
	}
}

func (a *App) RevealFile(p string) {
	if runtime.GOOS == "windows" && strings.HasPrefix(filepath.Clean(p), filepath.Clean(a.baseDir)) {
		_ = exec.Command("explorer", "/select,", p).Start()
	}
}

func (a *App) CopyText(s string) bool { return setClipboard(s) }

func (a *App) LaunchMarbles() bool {
	if marblesRunning() {
		return false
	}
	openURL("steam://rungameid/" + marblesAppID)
	return true
}
