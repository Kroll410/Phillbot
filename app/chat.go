package main

// Лайв-чат Twitch: розбір channel.chat.message, бейджі, кільцевий буфер, видалення повідомлень.

import (
	"encoding/json"
	"strings"
	"time"
)

const chatBufferSize = 300

type ChatFragment struct {
	Type    string `json:"type"` // text | emote | cheermote | mention
	Text    string `json:"text"`
	EmoteID string `json:"emoteId,omitempty"`
}

type ChatBadge struct {
	SetID string `json:"setId"`
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

type ChatMsg struct {
	ID        string         `json:"id"`
	Ts        int64          `json:"ts"`
	UserID    string         `json:"userId"`
	Login     string         `json:"login"`
	Name      string         `json:"name"`
	Color     string         `json:"color"`
	Badges    []ChatBadge    `json:"badges"`
	Fragments []ChatFragment `json:"fragments"`
	Text      string         `json:"text"`
	RewardID  string         `json:"rewardId,omitempty"`
	Order     bool           `json:"order,omitempty"`  // повідомлення-замовлення СабДею
	Reward    bool           `json:"reward,omitempty"` // інша нагорода за бали
	Mention   bool           `json:"mention,omitempty"`
	Highlight bool           `json:"highlight,omitempty"` // «Виділити повідомлення» за бали
}

// подія EventSub channel.chat.message
type chatEvent struct {
	ChatterUserID    string  `json:"chatter_user_id"`
	ChatterUserLogin string  `json:"chatter_user_login"`
	ChatterUserName  string  `json:"chatter_user_name"`
	MessageID        string  `json:"message_id"`
	Color            string  `json:"color"`
	Badges           []Badge `json:"badges"`
	MessageType      string  `json:"message_type"`
	RewardID         string  `json:"channel_points_custom_reward_id"`
	Message          struct {
		Text      string `json:"text"`
		Fragments []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Emote *struct {
				ID string `json:"id"`
			} `json:"emote"`
		} `json:"fragments"`
	} `json:"message"`
}

type badgeImg struct{ url, title string }

func (a *Channel) buildChatMsg(ev chatEvent) ChatMsg {
	m := ChatMsg{ID: ev.MessageID, Ts: time.Now().UnixMilli(), UserID: ev.ChatterUserID, Login: ev.ChatterUserLogin,
		Name: ev.ChatterUserName, Color: ev.Color, Text: ev.Message.Text, RewardID: ev.RewardID,
		Highlight: ev.MessageType == "channel_points_highlighted"}
	a.badgeMu.Lock()
	for _, b := range ev.Badges {
		if img, ok := a.badgeImgs[b.SetID+"/"+b.ID]; ok {
			m.Badges = append(m.Badges, ChatBadge{SetID: b.SetID, URL: img.url, Title: img.title})
		}
	}
	a.badgeMu.Unlock()
	for _, f := range ev.Message.Fragments {
		cf := ChatFragment{Type: f.Type, Text: f.Text}
		if f.Type == "emote" && f.Emote != nil {
			cf.EmoteID = f.Emote.ID
		}
		m.Fragments = append(m.Fragments, cf)
	}
	if len(m.Fragments) == 0 {
		m.Fragments = []ChatFragment{{Type: "text", Text: m.Text}}
	}
	a.mu.Lock()
	bound, me := a.st.Settings.RewardID, strings.ToLower(a.bc.Login)
	a.mu.Unlock()
	if m.RewardID != "" {
		m.Order = m.RewardID == bound
		m.Reward = !m.Order
	}
	if me != "" && strings.Contains(strings.ToLower(m.Text), "@"+me) {
		m.Mention = true
	}
	return m
}

func (a *Channel) onChatMessage(raw json.RawMessage) {
	var ev chatEvent
	if json.Unmarshal(raw, &ev) != nil {
		return
	}
	msg := a.buildChatMsg(ev)
	t, mo := subFromBadges(ev.Badges)
	a.badgeMu.Lock()
	si := subInfo{tier: t, months: mo, ts: time.Now(), color: msg.Color, badges: msg.Badges}
	a.badgeCache[ev.ChatterUserID] = si
	if ev.RewardID != "" { // точний зв'язок redeem ↔ повідомлення
		a.badgeCache[ev.ChatterUserID+"|"+ev.RewardID] = si
	}
	a.badgeMu.Unlock()
	a.chatMu.Lock()
	a.chat = append(a.chat, msg)
	if len(a.chat) > chatBufferSize {
		a.chat = a.chat[len(a.chat)-chatBufferSize:]
	}
	a.chatMu.Unlock()
	a.emitEvent("chat", msg)
	a.backfillLook(msg.UserID, msg.Color, msg.Badges)
	a.trackChatter(ev, msg)
	a.onVoteMessage(ev.ChatterUserID, ev.Message.Text)
}

// видалення: ids — конкретні повідомлення; userID — усі повідомлення людини; all — очищення чату
func (a *Channel) onChatDelete(ids []string, userID string, all bool) {
	a.chatMu.Lock()
	out := a.chat[:0]
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	for _, m := range a.chat {
		if all || set[m.ID] || (userID != "" && m.UserID == userID) {
			continue
		}
		out = append(out, m)
	}
	a.chat = out
	a.chatMu.Unlock()
	a.emitEvent("chatdel", map[string]any{"ids": ids, "userId": userID, "all": all})
}

func (a *Channel) handleChatNotification(subType string, raw json.RawMessage) bool {
	switch subType {
	case "channel.chat.message":
		a.onChatMessage(raw)
	case "channel.chat.message_delete":
		var ev struct {
			MessageID string `json:"message_id"`
		}
		if json.Unmarshal(raw, &ev) == nil && ev.MessageID != "" {
			a.onChatDelete([]string{ev.MessageID}, "", false)
		}
	case "channel.chat.clear_user_messages":
		var ev struct {
			TargetUserID string `json:"target_user_id"`
		}
		if json.Unmarshal(raw, &ev) == nil && ev.TargetUserID != "" {
			a.onChatDelete(nil, ev.TargetUserID, false)
			a.removeChatter(ev.TargetUserID) // бан / таймаут — прибрати зі списку
		}
	case "channel.chat.clear":
		a.onChatDelete(nil, "", true)
	default:
		return false
	}
	return true
}

// Бейджі каналу і глобальні (Helix, скоупи не потрібні).
func (a *Channel) loadBadges(bcID string) {
	type badgeSet struct {
		SetID    string `json:"set_id"`
		Versions []struct {
			ID    string `json:"id"`
			URL1x string `json:"image_url_1x"`
			URL2x string `json:"image_url_2x"`
			Title string `json:"title"`
		} `json:"versions"`
	}
	imgs := map[string]badgeImg{}
	for _, path := range []string{"chat/badges/global", "chat/badges?broadcaster_id=" + bcID} { // канальні перекривають глобальні
		var d struct {
			Data []badgeSet `json:"data"`
		}
		if err := a.helix("GET", path, nil, &d); err != nil {
			a.logf("Бейджі чату (%s): %v", strings.SplitN(path, "?", 2)[0], err)
			continue
		}
		for _, s := range d.Data {
			for _, v := range s.Versions {
				u := v.URL2x
				if u == "" {
					u = v.URL1x
				}
				imgs[s.SetID+"/"+v.ID] = badgeImg{url: u, title: v.Title}
			}
		}
	}
	a.badgeMu.Lock()
	a.badgeImgs = imgs
	a.badgeMu.Unlock()
	a.mu.Lock()
	a.emitLocked() // оновити значки в таблиці
	a.mu.Unlock()
}

// Замовлення без кольору/бейджів (записані раніше або без повідомлення в чаті) отримують вигляд
// з першого ж повідомлення цього чатерса.
func (a *Channel) backfillLook(userID, color string, badges []ChatBadge) {
	if userID == "" || (color == "" && len(badges) == 0) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	changed := false
	for i := range a.st.Entries {
		e := &a.st.Entries[i]
		if e.UserID == userID && len(e.Badges) == 0 && (e.Color == "" || e.Color == color) {
			if color != "" {
				e.Color = color
			}
			e.Badges = badges
			changed = true
		}
	}
	if changed {
		a.saveLocked()
		a.emitLocked()
	}
}

// Колір ніку через Helix (GET /chat/color) для замовлень, де його ще немає.
func (a *Channel) fillColors() {
	a.mu.Lock()
	var ids []string
	seen := map[string]bool{}
	for _, e := range a.st.Entries {
		if e.Color == "" && e.UserID != "" && !seen[e.UserID] && len(ids) < 100 {
			seen[e.UserID] = true
			ids = append(ids, e.UserID)
		}
	}
	a.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	var d struct {
		Data []struct {
			UserID string `json:"user_id"`
			Color  string `json:"color"`
		} `json:"data"`
	}
	if err := a.helix("GET", "chat/color?user_id="+strings.Join(ids, "&user_id="), nil, &d); err != nil {
		return
	}
	colors := map[string]string{}
	for _, c := range d.Data {
		if c.Color != "" {
			colors[c.UserID] = c.Color
		}
	}
	a.mu.Lock()
	changed := false
	for i := range a.st.Entries {
		if c, ok := colors[a.st.Entries[i].UserID]; ok && a.st.Entries[i].Color == "" {
			a.st.Entries[i].Color = c
			changed = true
		}
	}
	if changed {
		a.saveLocked()
		a.emitLocked()
	}
	a.mu.Unlock()
}

// subscriber-бейджі каналу для показу стажу підписки в таблиці
func (a *Channel) subBadges() map[string]string {
	a.badgeMu.Lock()
	defer a.badgeMu.Unlock()
	out := map[string]string{}
	for k, v := range a.badgeImgs {
		if strings.HasPrefix(k, "subscriber/") {
			out[strings.TrimPrefix(k, "subscriber/")] = v.url
		}
	}
	return out
}

func (a *Channel) GetChat() []ChatMsg {
	a.chatMu.Lock()
	defer a.chatMu.Unlock()
	return append([]ChatMsg(nil), a.chat...)
}
