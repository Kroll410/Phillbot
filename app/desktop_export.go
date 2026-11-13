//go:build !server

package main

// Десктоп: файл для Marbles, теки, буфер обміну, Steam.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Записує subday_marbles.csv, кладе шлях у буфер обміну і запускає гру через Steam.
func (a *App) ExportMarbles(launch bool) (MarblesResult, error) {
	clean := a.MarbleLines()
	if len(clean) == 0 {
		return MarblesResult{}, errors.New("немає замовлень з балами > 0")
	}
	// marbles/last — тека, де лежить ЛИШЕ файл останнього збору; її шлях копіюється в буфер.
	// Лише останній файл для гри; історія замовлень — у БД.
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
	res := MarblesResult{Path: p, Dir: dir, Lines: len(clean), Copied: setClipboard(dir)}
	a.mu.Lock()
	if !a.st.Exported { // після першого експорту можна вибрати переможця
		a.st.Exported = true
		a.saveLocked()
		a.emitLocked()
	}
	a.mu.Unlock()
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
