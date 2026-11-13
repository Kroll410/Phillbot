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
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// ExportAll — kind: "subday" (.csv), "auction" (.csv), "zip". Питає, куди зберегти; "" — скасовано.
func (a *App) ExportAll(kind string) (string, error) {
	stamp := time.Now().Format("2006-01-02")
	var name, filter string
	var build func() ([]byte, error)
	switch kind {
	case "subday":
		name, filter = "PhillBot_СабДей_"+stamp+".csv", "*.csv"
		build = func() ([]byte, error) { return subdayAllCSV(a.loadAllSubday()), nil }
	case "auction":
		name, filter = "PhillBot_Аукціон_"+stamp+".csv", "*.csv"
		build = func() ([]byte, error) { return auctionAllCSV(a.loadAllAuctions()), nil }
	case "zip":
		name, filter = "PhillBot_"+stamp+".zip", "*.zip"
		build = func() ([]byte, error) { return exportZip(a.loadAllSubday(), a.loadAllAuctions()) }
	default:
		return "", errors.New("невідомий експорт")
	}
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
	data, err := build()
	if err != nil {
		return "", err
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
