//go:build server

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*webServer, *httptest.Server) {
	t.Helper()
	ws, err := newWebServer(filepath.Join(t.TempDir(), "phillbot.db"), "local", "http://example.test")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(ws.handler())
	t.Cleanup(func() { srv.Close(); ws.close() })
	return ws, srv
}

func callAPI(t *testing.T, srv *httptest.Server, method string, args ...any) (int, []byte) {
	t.Helper()
	if args == nil {
		args = []any{}
	}
	b, _ := json.Marshal(args)
	req, _ := http.NewRequest("POST", srv.URL+"/api/call/"+method, strings.NewReader(string(b)))
	req.Header.Set("X-PhillBot", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// Методи каналу викликаються через HTTP; помилки методу — 400 з текстом; чужі запити без заголовка — 403.
func TestServerCalls(t *testing.T) {
	ws, srv := newTestServer(t)
	if code, _ := callAPI(t, srv, "StartTimer"); code != 200 {
		t.Fatal("StartTimer", code)
	}
	code, body := callAPI(t, srv, "AddManual", "Phill", 3, 2, "Hades")
	var snap Snapshot
	if code != 200 || json.Unmarshal(body, &snap) != nil || len(snap.Entries) != 1 || snap.Entries[0].Text != "Hades" {
		t.Fatalf("AddManual: %d %s", code, body)
	}
	if !strings.HasPrefix(snap.Obs.URL, "http://example.test/w/") || len(snap.Obs.URL) < len("http://example.test/w/")+32 {
		t.Fatal("widget url", snap.Obs.URL)
	}
	if code, body := callAPI(t, srv, "AddManual", "", 1, 1, "x"); code != 400 || !strings.Contains(string(body), "error") {
		t.Fatalf("method error must be 400: %d %s", code, body)
	}
	if code, _ := callAPI(t, srv, "AddManual", "only one arg"); code != 400 {
		t.Fatal("arg count", code)
	}
	if code, _ := callAPI(t, srv, "ExportMarbles", false); code != 404 { // десктопний метод
		t.Fatal("desktop-only method must be 404", code)
	}
	if code, _ := callAPI(t, srv, "saveLocked"); code != 404 {
		t.Fatal("unexported method reachable", code)
	}
	// CSRF: без X-PhillBot — заборонено, GET — заборонено
	resp, _ := http.Post(srv.URL+"/api/call/StartTimer", "application/json", strings.NewReader("[]"))
	if resp.StatusCode != 403 {
		t.Fatal("csrf", resp.StatusCode)
	}
	resp.Body.Close()
	if resp, _ := http.Get(srv.URL + "/api/call/GetState"); resp.StatusCode != 405 {
		t.Fatal("GET call", resp.StatusCode)
	}
	// стан у БД
	st, ok, err := ws.store.Load()
	if err != nil || !ok || len(st.Entries) != 1 {
		t.Fatalf("not saved: %v %v %d", ok, err, len(st.Entries))
	}
}

func readSSE(t *testing.T, r io.Reader, want string, d time.Duration) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		br := bufio.NewReader(r)
		ev := ""
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				got <- ""
				return
			}
			if strings.HasPrefix(l, "event: ") {
				ev = strings.TrimSpace(l[7:])
			}
			if strings.HasPrefix(l, "data: ") && ev == want {
				got <- l
				return
			}
		}
	}()
	select {
	case l := <-got:
		return l
	case <-time.After(d):
		return ""
	}
}

// Інтерфейс отримує повний стан; віджет — лише за секретом і лише свій зріз (без налаштувань і ключів).
func TestServerEventsAndWidget(t *testing.T) {
	ws, srv := newTestServer(t)
	ws.start(t.Context())
	callAPI(t, srv, "SaveSettings", Settings{DonKey: "secret-don-key"})

	ui, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer ui.Body.Close()
	if l := readSSE(t, ui.Body, "state", 2*time.Second); !strings.Contains(l, `"settings"`) {
		t.Fatal("ui initial state", l)
	}

	if resp, _ := http.Get(srv.URL + "/w/wrongsecret0000000000000000000000/api/state"); resp.StatusCode != 404 {
		t.Fatal("widget without secret", resp.StatusCode)
	}
	wbase := srv.URL + "/w/" + ws.secret
	for _, p := range []string{"/overlay/widget", "/overlay/overlay.js", "/overlay/chatrender.js", "/api/state"} {
		resp, err := http.Get(wbase + p)
		if err != nil || resp.StatusCode != 200 {
			t.Fatal(p, err, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if resp, _ := http.Get(wbase + "/overlay/../../api/call/GetState"); resp.StatusCode == 200 {
		t.Fatal("traversal out of widget")
	}
	ov, err := http.Get(wbase + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer ov.Body.Close()
	first := readSSE(t, ov.Body, "state", 2*time.Second)
	callAPI(t, srv, "StartTimer")
	next := readSSE(t, ov.Body, "state", 2*time.Second)
	for _, l := range []string{first, next} {
		if !strings.Contains(l, `"serverNow"`) || strings.Contains(l, "secret-don-key") || strings.Contains(l, `"settings"`) {
			t.Fatalf("widget state: %.200s", l)
		}
	}
	if !strings.Contains(next, `"status":"running"`) {
		t.Fatalf("widget did not see the timer start: %.300s", next)
	}
}

// Інтерфейс: index.html з мостом web.js перед app.js; файли фронтенду віддаються, чужі шляхи — ні.
func TestServerUI(t *testing.T) {
	_, srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(b)
	if i, j := strings.Index(html, `src="web.js"`), strings.Index(html, `src="app.js`); i < 0 || j < 0 || i > j {
		t.Fatal("web.js must load before app.js")
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatal("frame protection")
	}
	for _, p := range []string{"/web.js", "/app.js", "/style.css"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil || resp.StatusCode != 200 {
			t.Fatal(p, err, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if resp, _ := http.Get(srv.URL + "/../server_web.go"); resp.StatusCode == 200 {
		t.Fatal("source reachable")
	}
}

func TestIsLoopback(t *testing.T) {
	for addr, want := range map[string]bool{"127.0.0.1:8080": true, "localhost:80": true, "[::1]:9": true,
		"0.0.0.0:8080": false, ":8080": false, "192.168.1.5:80": false, "example.com:80": false} {
		if isLoopback(addr) != want {
			t.Errorf("%s: want %v", addr, want)
		}
	}
}
