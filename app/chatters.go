package main

// Список активних глядачів для вкладки «Чат»: хто писав у чат за цю сесію, згрупований за роллю.
// Ролі — з бейджів повідомлень; фолов («підписник на канал») — через Helix channels/followers.

import (
	"sort"
	"time"
)

const (
	followUnknown = iota
	followChecking
	followYes
	followNo
)

type Chatter struct {
	ID     string      `json:"id"`
	Login  string      `json:"login"`
	Name   string      `json:"name"`
	Color  string      `json:"color"`
	Badges []ChatBadge `json:"badges"`
	Group  string      `json:"group"` // mod | vip | follower | sub | viewer
	LastTs int64       `json:"lastTs"`
	Count  int         `json:"count"`

	sets   map[string]bool
	follow int
}

func (c *Chatter) regroup() {
	switch {
	case c.sets["broadcaster"] || c.sets["moderator"] || c.sets["lead_moderator"]:
		c.Group = "mod"
	case c.sets["vip"]:
		c.Group = "vip"
	case c.sets["subscriber"] || c.sets["founder"]:
		c.Group = "sub"
	case c.follow == followYes:
		c.Group = "follower"
	default:
		c.Group = "viewer"
	}
}

func (c *Chatter) needsFollowCheck() bool {
	return c.follow == followUnknown && (c.Group == "viewer")
}

func (a *Channel) trackChatter(ev chatEvent, msg ChatMsg) {
	if ev.ChatterUserID == "" {
		return
	}
	a.chattersMu.Lock()
	c, ok := a.chatters[ev.ChatterUserID]
	if !ok {
		c = &Chatter{ID: ev.ChatterUserID}
		a.chatters[ev.ChatterUserID] = c
	}
	c.Login, c.Name, c.Color, c.Badges = ev.ChatterUserLogin, ev.ChatterUserName, msg.Color, msg.Badges
	c.sets = map[string]bool{}
	for _, b := range ev.Badges {
		c.sets[b.SetID] = true
	}
	c.LastTs = msg.Ts
	c.Count++
	c.regroup()
	check := c.needsFollowCheck()
	if check {
		c.follow = followChecking
	}
	a.chattersMu.Unlock()
	if check {
		go a.checkFollow(ev.ChatterUserID)
	}
	a.notifyChatters()
}

func (a *Channel) checkFollow(userID string) {
	a.mu.Lock()
	bcID := a.bc.ID
	a.mu.Unlock()
	res := followNo
	var d struct {
		Data []struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}
	if err := a.helix("GET", "channels/followers?broadcaster_id="+bcID+"&user_id="+userID, nil, &d); err == nil {
		if len(d.Data) > 0 {
			res = followYes
		}
	}
	a.chattersMu.Lock()
	if c, ok := a.chatters[userID]; ok {
		c.follow = res
		c.regroup()
	}
	a.chattersMu.Unlock()
	a.notifyChatters()
}

func (a *Channel) removeChatter(userID string) {
	a.chattersMu.Lock()
	delete(a.chatters, userID)
	a.chattersMu.Unlock()
	a.notifyChatters()
}

// GetChatters — активні глядачі, найсвіжіші зверху.
func (a *Channel) GetChatters() []Chatter {
	a.chattersMu.Lock()
	defer a.chattersMu.Unlock()
	out := make([]Chatter, 0, len(a.chatters))
	for _, c := range a.chatters {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTs > out[j].LastTs })
	return out
}

// не частіше ніж раз на 400 мс — у великих чатах подій багато
func (a *Channel) notifyChatters() {
	select {
	case a.chattersCh <- struct{}{}:
	default:
	}
}

func (a *Channel) chattersLoop() {
	for range a.chattersCh {
		a.emitEvent("chatters", a.GetChatters())
		time.Sleep(400 * time.Millisecond)
	}
}
