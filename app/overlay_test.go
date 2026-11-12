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

	for _, p := range []string{"/overlay/timer", "/overlay/top", "/overlay/marbles", "/overlay/last", "/overlay/chat", "/overlay/index", "/overlay/overlay.js", "/overlay/chatrender.js"} {
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
