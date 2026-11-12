package main

// Експорт: файл для Marbles on Stream, таблиця для Excel (CSV з BOM), JSON.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

const marblesAppID = "1170970"

type MarblesResult struct {
	Path           string `json:"path"`
	Lines          int    `json:"lines"`
	Copied         bool   `json:"copied"`
	AlreadyRunning bool   `json:"alreadyRunning"`
	Launched       bool   `json:"launched"`
}

func encodeLines(lines []string, enc string) []byte {
	text := strings.Join(lines, "\r\n") + "\r\n"
	if enc == "utf16" {
		u := utf16.Encode([]rune(text))
		data := make([]byte, 0, 2+len(u)*2)
		data = append(data, 0xFF, 0xFE)
		for _, c := range u {
			data = append(data, byte(c), byte(c>>8))
		}
		return data
	}
	return append([]byte{0xEF, 0xBB, 0xBF}, []byte(text)...)
}

func cleanMarbleName(s string, max int) string {
	s = strings.NewReplacer(",", " ", ";", " ", "\"", " ", "\t", " ", "\r", " ", "\n", " ").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > max {
		r = r[:max]
	}
	return strings.TrimSpace(string(r))
}

// Записує subday_marbles.csv (+ копію з датою), кладе шлях у буфер обміну і запускає гру через Steam.
// Список куль формує інтерфейс (та сама логіка, що й у таблиці).
func (a *App) ExportMarbles(lines []string, launch bool) (MarblesResult, error) {
	a.mu.Lock()
	enc, max := a.st.Settings.Encoding, a.st.Settings.NameMax
	a.mu.Unlock()
	var clean []string
	for _, l := range lines {
		if n := cleanMarbleName(l, max+12); n != "" {
			clean = append(clean, n)
		}
		if len(clean) >= 1000 {
			break
		}
	}
	if len(clean) == 0 {
		return MarblesResult{}, errors.New("немає замовлень з балами > 0")
	}
	dir := filepath.Join(a.baseDir, "marbles")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return MarblesResult{}, err
	}
	data := encodeLines(clean, enc)
	p := filepath.Join(dir, "subday_marbles.csv")
	if err := os.WriteFile(p, data, 0644); err != nil {
		return MarblesResult{}, errors.New("не вдалося записати файл (" + err.Error() + ") — можливо, він відкритий в іншій програмі")
	}
	_ = os.WriteFile(filepath.Join(dir, "subday_marbles_"+time.Now().Format("2006-01-02_15-04-05")+".csv"), data, 0644)
	res := MarblesResult{Path: p, Lines: len(clean), Copied: setClipboard(p)}
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
