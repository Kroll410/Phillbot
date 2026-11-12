package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestOverlayServer(t *testing.T) {
	a := newTestApp(t)
	a.overlay = newOverlayServer(a)
	a.overlay.start(28411)
	defer a.overlay.stop()
	inf := a.overlay.info()
	if !inf.Running {
		t.Fatal(inf.Error)
	}
	base := inf.URL
	a.AddManual("Phill", 3, 46, "Dark Souls")

	resp, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var st OverlayState
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.Totals.Points != 138 || st.Totals.Marbles != 138 || len(st.Top) != 1 || st.Top[0].Name != "Dark Souls" {
		t.Fatalf("%+v", st)
	}

	// чужий Host (DNS-rebinding) — заборонено
	req, _ := http.NewRequest("GET", base+"/api/state", nil)
	req.Host = "evil.example:" + strconv.Itoa(inf.Port)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 403 {
		t.Fatal("host check", resp.StatusCode)
	}
	resp.Body.Close()

	for _, p := range []string{"/overlay/widget", "/overlay/chat", "/overlay/index", "/overlay/overlay.js", "/overlay/chatrender.js"} {
		resp, err := http.Get(base + p)
		if err != nil || resp.StatusCode != 200 {
			t.Fatal(p, err, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if resp, _ := http.Get(base + "/overlay/../app.go"); resp.StatusCode == 200 {
		t.Fatal("path traversal")
	}

	// SSE: початковий стан, потім подія чату
	resp, err = http.Get(base + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	readEvent := func() string {
		done := make(chan string, 1)
		go func() {
			for {
				line, err := br.ReadString('\n')
				if err != nil {
					done <- ""
					return
				}
				if strings.HasPrefix(line, "event: ") {
					done <- strings.TrimSpace(line[7:])
					return
				}
			}
		}()
		select {
		case e := <-done:
			return e
		case <-time.After(3 * time.Second):
			return "timeout"
		}
	}
	if e := readEvent(); e != "state" {
		t.Fatal("first event", e)
	}
	raw := json.RawMessage(`{"chatter_user_id":"9","chatter_user_login":"viewer","chatter_user_name":"Viewer","message_id":"m1","color":"",
		"badges":[{"set_id":"subscriber","id":"2006","info":"6"}],"message_type":"text","channel_points_custom_reward_id":"r1",
		"message":{"text":"Hades Kappa","fragments":[{"type":"text","text":"Hades "},{"type":"emote","text":"Kappa","emote":{"id":"25"}}]}}`)
	a.st.Settings.RewardID = "r1"
	a.handleChatNotification("channel.chat.message", raw)
	if e := readEvent(); e != "chat" {
		t.Fatal("chat event", e)
	}
	msgs := a.GetChat()
	if len(msgs) != 1 || !msgs[0].Order || len(msgs[0].Fragments) != 2 || msgs[0].Fragments[1].EmoteID != "25" {
		t.Fatalf("%+v", msgs)
	}
	if c, ok := a.badgeCache["9|r1"]; !ok || c.tier != 2 || c.months != 6 {
		t.Fatal("reward-linked badge cache")
	}
	a.handleChatNotification("channel.chat.message_delete", json.RawMessage(`{"message_id":"m1"}`))
	if len(a.GetChat()) != 0 {
		t.Fatal("delete")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(br, 0))
}

func TestChatWindowLock(t *testing.T) {
	a := newTestApp(t)
	a.overlay = newOverlayServer(a)
	a.overlay.start(28431)
	defer a.overlay.stop()
	if a.GetState().ChatWindow {
		t.Fatal("locked at start")
	}
	resp, err := http.Get(a.overlay.info().URL + "/api/events?window=1")
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(resp.Body)
	br.ReadString('\n') // дочекатися підключення
	if !a.GetState().ChatWindow {
		t.Fatal("not locked while window connected")
	}
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for a.GetState().ChatWindow && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if a.GetState().ChatWindow {
		t.Fatal("still locked after window closed")
	}
}

func TestBackfillLookFromChat(t *testing.T) {
	a := newTestApp(t)
	a.st.Entries = []Entry{{ID: "1", UserID: "9", User: "Viewer", Tier: 2, Months: 6, Text: "Hades"}}
	a.badgeImgs["vip/1"] = badgeImg{url: "https://x/vip.png", title: "VIP"}
	a.badgeImgs["subscriber/2006"] = badgeImg{url: "https://x/sub.png", title: "Sub"}
	a.handleChatNotification("channel.chat.message", json.RawMessage(`{"chatter_user_id":"9","chatter_user_login":"viewer","chatter_user_name":"Viewer","message_id":"m","color":"#FF0000",
		"badges":[{"set_id":"vip","id":"1","info":""},{"set_id":"subscriber","id":"2006","info":"6"}],"message_type":"text","message":{"text":"hi","fragments":[{"type":"text","text":"hi"}]}}`))
	e := a.st.Entries[0]
	if e.Color != "#FF0000" || len(e.Badges) != 2 || e.Badges[0].SetID != "vip" || e.Badges[1].SetID != "subscriber" {
		t.Fatalf("%+v", e)
	}
}

func TestChattersGroups(t *testing.T) {
	a := newTestApp(t)
	msg := func(id, login string, badges string) {
		a.handleChatNotification("channel.chat.message", json.RawMessage(`{"chatter_user_id":"`+id+`","chatter_user_login":"`+login+`","chatter_user_name":"`+login+`","message_id":"m`+id+`","color":"",
			"badges":[`+badges+`],"message_type":"text","message":{"text":"hi","fragments":[{"type":"text","text":"hi"}]}}`))
		time.Sleep(5 * time.Millisecond)
	}
	msg("1", "modguy", `{"set_id":"moderator","id":"1","info":""},{"set_id":"subscriber","id":"0","info":"3"}`)
	msg("2", "vipgal", `{"set_id":"vip","id":"1","info":""}`)
	msg("3", "subber", `{"set_id":"subscriber","id":"3000","info":"5"}`)
	msg("4", "lurker", ``)
	deadline := time.Now().Add(2 * time.Second)
	var list []Chatter
	for time.Now().Before(deadline) {
		list = a.GetChatters()
		if len(list) == 4 && list[0].Group != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := map[string]string{}
	for _, c := range list {
		got[c.Login] = c.Group
	}
	if got["modguy"] != "mod" || got["vipgal"] != "vip" || got["subber"] != "sub" || got["lurker"] != "viewer" {
		t.Fatal(got)
	}
	if list[0].Login != "lurker" { // найсвіжіший зверху
		t.Fatal("order", list[0].Login)
	}
	a.handleChatNotification("channel.chat.clear_user_messages", json.RawMessage(`{"target_user_id":"4"}`))
	if len(a.GetChatters()) != 3 {
		t.Fatal("banned chatter not removed")
	}
}
