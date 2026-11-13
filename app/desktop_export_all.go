//go:build !server

package main

// Десктоп: експорт за весь час через діалог «Зберегти як».

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ExportAll — kind: "subday" (.csv), "auction" (.csv), "zip". Питає, куди зберегти; "" — скасовано.
func (a *App) ExportAll(kind string) (string, error) {
	name, data, err := a.exportFile(kind)
	if err != nil {
		return "", err
	}
	filter := "*" + filepath.Ext(name)
	dir := a.exportsDir
	_ = os.MkdirAll(dir, 0755)
	p, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		DefaultDirectory: dir, DefaultFilename: name,
		Filters: []wruntime.FileFilter{{DisplayName: strings.TrimPrefix(filter, "*."), Pattern: filter}},
	})
	if err != nil || p == "" {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(p), filepath.Ext(name)) {
		p += filepath.Ext(name)
	}
	if err := os.WriteFile(p, data, 0644); err != nil {
		return "", errors.New("не вдалося записати файл — можливо, він відкритий в Excel")
	}
	a.logf("Експорт: %s", p)
	if runtime.GOOS == "windows" {
		_ = exec.Command("explorer", "/select,", p).Start() // показати збережений файл
	}
	return p, nil
}
