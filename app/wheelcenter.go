package main

// Центр колеса аукціону: емодзі каналу Twitch або власна картинка / GIF.
// Власний файл копіюється в <база>/wheel/ і віддається інтерфейсу через сервер ресурсів Wails: /userimg/wheel/<файл>.

import (
	"errors"
	"strings"
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
func (a *Channel) GetChannelEmotes() ([]ChannelEmote, error) {
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

var wheelImgExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}
