//go:build !server

package main

// Імпорт даних у програму на ПК: файл бази PhillBot (phillbot.db з іншого ПК або витяг з вебверсії).
// Поточні дані спершу копіюються в phillbot.before-import-<дата>.db поруч із програмою.

import (
	"errors"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type ImportPickResult struct {
	File     string          `json:"file"`
	Channels []ImportChannel `json:"channels"`
}

// ImportPick — вибрати файл бази й показати, які канали в ньому є.
func (a *App) ImportPick() (ImportPickResult, error) {
	if a.ctx == nil {
		return ImportPickResult{}, errors.New("вікно ще не готове")
	}
	file, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:   "Імпорт даних PhillBot",
		Filters: []wruntime.FileFilter{{DisplayName: "База PhillBot (*.db)", Pattern: "*.db"}},
	})
	if err != nil || file == "" {
		return ImportPickResult{}, err
	}
	chs, err := listImportChannels(file)
	if err != nil {
		return ImportPickResult{}, errors.New("не вдалося прочитати файл: " + err.Error())
	}
	if len(chs) == 0 {
		return ImportPickResult{}, errors.New("у файлі немає даних каналу")
	}
	return ImportPickResult{File: file, Channels: chs}, nil
}

// ImportApply — замінити дані на ПК даними каналу id з файлу. Налаштування цього ПК (OBS, ngrok, Donatello, чат) лишаються.
func (a *App) ImportApply(file, id string) (Snapshot, error) {
	a.mu.Lock()
	if a.store == nil {
		a.mu.Unlock()
		return a.GetState(), errors.New("база даних недоступна — імпорт неможливий")
	}
	bak := importBackupName(a.baseDir, time.Now())
	if err := a.store.backupTo(bak); err != nil {
		a.mu.Unlock()
		return a.GetState(), errors.New("не вдалося зробити резервну копію: " + err.Error())
	}
	local := a.st.Settings
	if err := a.store.importChannel(file, id); err != nil {
		a.mu.Unlock()
		return a.GetState(), errors.New("імпорт не вдався (дані на ПК не змінено): " + err.Error())
	}
	st, ok, err := a.store.Load()
	if err != nil || !ok {
		a.mu.Unlock()
		if err == nil {
			err = errors.New("порожні дані")
		}
		return a.GetState(), errors.New("дані імпортовано, але не прочитано: " + err.Error() + ". Резервна копія: " + bak)
	}
	st = fixState(st)
	keepLocalSettings(&st.Settings, local)
	a.st = st
	a.lastOrd = nil
	a.saveLocked()
	a.emitLocked()
	snap := a.snapshotLocked()
	a.mu.Unlock()
	a.logf("Імпортовано дані з %s; попередні — у %s", file, bak)
	return snap, nil
}
