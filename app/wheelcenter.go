package main

// Центр колеса аукціону: емодзі каналу Twitch або власна картинка / GIF.
// Власний файл копіюється в <база>/wheel/ і віддається інтерфейсу через сервер ресурсів Wails: /userimg/wheel/<файл>.

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// WheelCenter — що показувати в центрі колеса.
type WheelCenter struct {
	Kind string `json:"kind"`           // "" — нічого | emote | file
	URL  string `json:"url,omitempty"`  // emote: адреса картинки на CDN Twitch
	Name string `json:"name,omitempty"` // emote: назва; file: ім'я файлу в теці wheel
}

type ChannelEmote struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Animated bool   `json:"animated"`
}

// GetChannelEmotes — емодзі каналу (Helix chat/emotes). Анімовані — у вигляді GIF.
func (a *App) GetChannelEmotes() ([]ChannelEmote, error) {
	a.mu.Lock()
	bcID := a.bc.ID
	a.mu.Unlock()
	if bcID == "" {
		return nil, errors.New("спершу увійдіть у Twitch")
	}
	var d struct {
		Data []struct {
			ID     string   `json:"id"`
			Name   string   `json:"name"`
			Format []string `json:"format"`
			Scale  []string `json:"scale"`
		} `json:"data"`
		Template string `json:"template"`
	}
	if err := a.helix("GET", "chat/emotes?broadcaster_id="+bcID, nil, &d); err != nil {
		return nil, err
	}
	tpl := d.Template
	if tpl == "" {
		tpl = "https://static-cdn.jtvnw.net/emoticons/v2/{{id}}/{{format}}/{{theme_mode}}/{{scale}}"
	}
	out := []ChannelEmote{}
	for _, e := range d.Data {
		format, anim := "static", false
		for _, f := range e.Format {
			if f == "animated" {
				format, anim = "animated", true
			}
		}
		scale := "3.0"
		if len(e.Scale) > 0 {
			scale = e.Scale[len(e.Scale)-1]
		}
		u := strings.NewReplacer("{{id}}", e.ID, "{{format}}", format, "{{theme_mode}}", "dark", "{{scale}}", scale).Replace(tpl)
		out = append(out, ChannelEmote{ID: e.ID, Name: e.Name, URL: u, Animated: anim})
	}
	return out, nil
}

func (a *App) wheelDir() string { return filepath.Join(a.baseDir, "wheel") }

var wheelImgExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// PickWheelImage — вибрати картинку або GIF; файл копіюється в теку wheel. Повертає ім'я файлу ("" — скасовано).
func (a *App) PickWheelImage() (string, error) {
	if a.ctx == nil {
		return "", errors.New("вікно ще не готове")
	}
	src, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:   "Картинка в центр колеса",
		Filters: []wruntime.FileFilter{{DisplayName: "Картинки та GIF (*.png;*.jpg;*.gif;*.webp)", Pattern: "*.png;*.jpg;*.jpeg;*.gif;*.webp"}},
	})
	if err != nil || src == "" {
		return "", err
	}
	ext := strings.ToLower(filepath.Ext(src))
	if !wheelImgExt[ext] {
		return "", errors.New("підтримуються PNG, JPG, GIF і WEBP")
	}
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if st.Size() > 15<<20 {
		return "", errors.New("файл завеликий (більше 15 МБ)")
	}
	if err := os.MkdirAll(a.wheelDir(), 0755); err != nil {
		return "", err
	}
	name := "center_" + time.Now().Format("20060102_150405") + ext
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.Create(filepath.Join(a.wheelDir(), name))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	return name, out.Close()
}

// SaveWheelImage — файл, перетягнутий у вікно налаштувань (вміст у base64). Повертає ім'я збереженого файлу.
func (a *App) SaveWheelImage(name, data string) (string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if !wheelImgExt[ext] {
		return "", errors.New("підтримуються PNG, JPG, GIF і WEBP")
	}
	if i := strings.Index(data, ","); i >= 0 && strings.HasPrefix(data, "data:") {
		data = data[i+1:]
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", errors.New("не вдалося прочитати файл")
	}
	if len(b) == 0 || len(b) > 15<<20 {
		return "", errors.New("файл порожній або завеликий (більше 15 МБ)")
	}
	if err := os.MkdirAll(a.wheelDir(), 0755); err != nil {
		return "", err
	}
	out := "center_" + time.Now().Format("20060102_150405") + ext
	return out, os.WriteFile(filepath.Join(a.wheelDir(), out), b, 0644)
}

// прибрати з теки wheel усі файли, крім поточного (викликається після збереження налаштувань)
func (a *App) cleanWheelDir(keep string) {
	list, _ := os.ReadDir(a.wheelDir())
	for _, f := range list {
		if !f.IsDir() && f.Name() != keep {
			_ = os.Remove(filepath.Join(a.wheelDir(), f.Name()))
		}
	}
}

// userAssets — обробник сервера ресурсів Wails для файлів користувача (/userimg/wheel/<файл>).
func (a *App) userAssets() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := strings.CutPrefix(r.URL.Path, "/userimg/wheel/")
		if !ok || name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || !wheelImgExt[strings.ToLower(filepath.Ext(name))] {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join(a.wheelDir(), name))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ct := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}[strings.ToLower(filepath.Ext(name))]
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b)
	})
}
