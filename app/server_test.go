//go:build server

package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T, cfg serverConfig) (*webServer, *httptest.Server) {
	t.Helper()
	if cfg.Public == "" {
		cfg.Public = "http://example.test"
	}
	ws, err := newWebServer(cfg, filepath.Join(t.TempDir(), "phillbot.db"))
	if err != nil {
		t.Fatal(err)
	}
	ws.connect = false // без справжнього Twitch
	srv := httptest.NewServer(ws.handler())
	t.Cleanup(func() { srv.Close(); ws.close() })
	return ws, srv
}

func devServer(t *testing.T) (*webServer, *httptest.Server) {
	ws, srv := newTestServer(t, serverConfig{Dev: true})
	ws.start()
	return ws, srv
}

func callWith(t *testing.T, c *http.Client, base, method string, args ...any) (int, []byte) {
	t.Helper()
	if args == nil {
		args = []any{}
	}
	b, _ := json.Marshal(args)
	req, _ := http.NewRequest("POST", base+"/api/call/"+method, strings.NewReader(string(b)))
	req.Header.Set("X-PhillBot", "1")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

func callAPI(t *testing.T, srv *httptest.Server, method string, args ...any) (int, []byte) {
	return callWith(t, http.DefaultClient, srv.URL, method, args...)
}

// Методи каналу викликаються через HTTP; помилки методу — 400 з текстом; чужі запити без заголовка — 403.
func TestServerCalls(t *testing.T) {
	ws, srv := devServer(t)
	if code, _ := callAPI(t, srv, "StartTimer"); code != 200 {
		t.Fatal("StartTimer", code)
	}
	code, body := callAPI(t, srv, "AddManual", "Phill", 3, 2, "Hades")
	var snap Snapshot
	if code != 200 || json.Unmarshal(body, &snap) != nil || len(snap.Entries) != 1 || snap.Entries[0].Text != "Hades" {
		t.Fatalf("AddManual: %d %s", code, body)
	}
	if !strings.HasPrefix(snap.Obs.URL, "http://example.test/w/") || len(snap.Obs.URL) != len("http://example.test/w/")+32 {
		t.Fatal("widget url", snap.Obs.URL)
	}
	if code, body := callAPI(t, srv, "AddManual", "", 1, 1, "x"); code != 400 || !strings.Contains(string(body), "error") {
		t.Fatalf("method error must be 400: %d %s", code, body)
	}
	if code, _ := callAPI(t, srv, "AddManual", "only one arg"); code != 400 {
		t.Fatal("arg count", code)
	}
	for _, m := range []string{"ExportMarbles", "saveLocked", "Logout"} { // десктопний, неекспортований, заборонений у вебі
		if code, _ := callAPI(t, srv, m); code != 404 {
			t.Fatal(m, "must be 404", code)
		}
	}
	resp, _ := http.Post(srv.URL+"/api/call/StartTimer", "application/json", strings.NewReader("[]"))
	if resp.StatusCode != 403 {
		t.Fatal("csrf", resp.StatusCode)
	}
	resp.Body.Close()
	if resp, _ := http.Get(srv.URL + "/api/call/GetState"); resp.StatusCode != 405 {
		t.Fatal("GET call", resp.StatusCode)
	}
	rt, _ := ws.channel("local")
	st, ok, err := rt.ch.store.Load()
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
	ws, srv := devServer(t)
	callAPI(t, srv, "SaveSettings", Settings{DonKey: "secret-don-key"})

	ui, err := http.Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer ui.Body.Close()
	if l := readSSE(t, ui.Body, "state", 2*time.Second); !strings.Contains(l, `"settings"`) {
		t.Fatal("ui initial state", l)
	}
	if resp, _ := http.Get(srv.URL + "/w/00000000000000000000000000000000/api/state"); resp.StatusCode != 404 {
		t.Fatal("widget without secret", resp.StatusCode)
	}
	rt, _ := ws.channel("local")
	wbase := srv.URL + "/w/" + rt.secret
	for _, p := range []string{"/overlay/widget", "/overlay/overlay.js", "/overlay/chatrender.js", "/api/state"} {
		resp, err := http.Get(wbase + p)
		if err != nil || resp.StatusCode != 200 {
			t.Fatal(p, err, resp.StatusCode)
		}
		resp.Body.Close()
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
	_, srv := devServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(b)
	if i, j := strings.Index(page, `src="web.js"`), strings.Index(page, `src="app.js`); i < 0 || j < 0 || i > j {
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

// ---------- вхід через Twitch і білий список ----------

// несправжній Twitch: код "code-<login>" дає токен цього користувача
func fakeTwitch(t *testing.T) {
	t.Helper()
	users := map[string]User{"admin_one": {ID: "100", Login: "admin_one", DisplayName: "Admin"},
		"streamer_two": {ID: "200", Login: "streamer_two", DisplayName: "Streamer"},
		"stranger":     {ID: "300", Login: "stranger", DisplayName: "Stranger"}}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			_ = r.ParseForm()
			if r.Form.Get("client_secret") != "sec" || r.Form.Get("grant_type") != "authorization_code" {
				w.WriteHeader(400)
				return
			}
			login := strings.TrimPrefix(r.Form.Get("code"), "code-")
			writeJSONResp(w, map[string]any{"access_token": "tok-" + login, "refresh_token": "ref-" + login, "expires_in": 3600})
		case "/helix/users":
			login := strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "tok-")
			u, ok := users[login]
			if !ok {
				w.WriteHeader(401)
				return
			}
			writeJSONResp(w, map[string]any{"data": []User{u}})
		default:
			w.WriteHeader(404)
		}
	}))
	oldID, oldHelix := twitchIDBase, helixBase
	twitchIDBase, helixBase = fake.URL, fake.URL+"/helix/"
	t.Cleanup(func() { twitchIDBase, helixBase = oldID, oldHelix; fake.Close() })
}

func newClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// повний вхід: /auth/login → (Twitch) → /auth/callback; повертає код відповіді callback
func login(t *testing.T, c *http.Client, base, who string) int {
	t.Helper()
	resp, err := c.Get(base + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != 302 || !strings.HasSuffix(loc.Path, "/oauth2/authorize") || loc.Query().Get("redirect_uri") != "http://example.test/auth/callback" {
		t.Fatalf("authorize redirect: %d %s", resp.StatusCode, loc)
	}
	resp, err = c.Get(base + "/auth/callback?code=code-" + who + "&state=" + loc.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func authServer(t *testing.T) (*webServer, *httptest.Server) {
	fakeTwitch(t)
	ws, srv := newTestServer(t, serverConfig{ClientID: "cid", ClientSecret: "sec", Key: strings.Repeat("k", 32), Admin: "Admin_One"})
	ws.start()
	return ws, srv
}

func TestServerRequiresConfig(t *testing.T) {
	for _, cfg := range []serverConfig{{ClientID: "c", ClientSecret: "s", Key: "short", Admin: "a"},
		{ClientSecret: "s", Key: strings.Repeat("k", 32), Admin: "a"}, {ClientID: "c", ClientSecret: "s", Key: strings.Repeat("k", 32)}} {
		if _, err := newWebServer(cfg, filepath.Join(t.TempDir(), "x.db")); err == nil {
			t.Fatalf("config accepted: %+v", cfg)
		}
	}
}

// Вхід: лише білий список; без сесії — сторінка входу й 401; підроблений OAuth state — відмова.
func TestServerLoginWhitelist(t *testing.T) {
	ws, srv := authServer(t)
	anon := newClient()
	resp, _ := anon.Get(srv.URL + "/")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), "/auth/login") || strings.Contains(string(b), "app.js") {
		t.Fatal("anonymous must get the login page")
	}
	if code, _ := callWith(t, anon, srv.URL, "GetState"); code != 401 {
		t.Fatal("api without session", code)
	}
	if resp, _ := anon.Get(srv.URL + "/auth/callback?code=code-admin_one&state=forged"); resp.StatusCode != 400 {
		t.Fatal("forged state accepted", resp.StatusCode)
	}
	stranger := newClient()
	if code := login(t, stranger, srv.URL, "stranger"); code != 403 {
		t.Fatal("stranger let in", code)
	}
	if code, _ := callWith(t, stranger, srv.URL, "GetState"); code != 401 {
		t.Fatal("stranger has api", code)
	}
	if ws.auth.loadTokens("300") != nil {
		t.Fatal("stranger tokens stored")
	}
	admin := newClient()
	if code := login(t, admin, srv.URL, "admin_one"); code != 302 {
		t.Fatal("admin login", code)
	}
	code, body := callWith(t, admin, srv.URL, "GetState")
	if code != 200 {
		t.Fatal("admin api", code, string(body))
	}
	if tok := ws.auth.loadTokens("100"); tok == nil || tok.AccessToken != "tok-admin_one" {
		t.Fatal("tokens not stored")
	}
	var raw string
	_ = ws.base.db.QueryRow(`SELECT value FROM channel_secrets WHERE channel_id = '100'`).Scan(&raw)
	if raw == "" || strings.Contains(raw, "tok-admin_one") {
		t.Fatal("tokens must be encrypted at rest")
	}
	req, _ := http.NewRequest("POST", srv.URL+"/auth/logout", nil)
	req.Header.Set("X-PhillBot", "1")
	resp, _ = admin.Do(req)
	resp.Body.Close()
	if code, _ := callWith(t, admin, srv.URL, "GetState"); code != 401 {
		t.Fatal("api after logout", code)
	}
}

// Адмін додає й прибирає людей; прибраний втрачає доступ одразу; канали стрімерів ізольовані.
func TestServerAdminAndIsolation(t *testing.T) {
	ws, srv := authServer(t)
	admin, two := newClient(), newClient()
	login(t, admin, srv.URL, "admin_one")
	post := func(c *http.Client, op, body string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/admin/api/"+op, strings.NewReader(body))
		req.Header.Set("X-PhillBot", "1")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(admin, "allow", `{"login":"Streamer_Two"}`); code != 204 {
		t.Fatal("allow", code)
	}
	if code := post(admin, "allow", `{"login":"bad login!"}`); code != 400 {
		t.Fatal("invalid login accepted", code)
	}
	if code := login(t, two, srv.URL, "streamer_two"); code != 302 {
		t.Fatal("allowed streamer login", code)
	}
	if resp, _ := two.Get(srv.URL + "/admin"); resp.StatusCode != 404 {
		t.Fatal("non-admin sees admin page", resp.StatusCode)
	}
	if code := post(two, "allow", `{"login":"stranger"}`); code != 404 {
		t.Fatal("non-admin can allow", code)
	}
	callWith(t, two, srv.URL, "AddManual", "Viewer", 1, 1, "Hades")
	_, b := callWith(t, admin, srv.URL, "GetState")
	var adminSnap Snapshot
	_ = json.Unmarshal(b, &adminSnap)
	_, b = callWith(t, two, srv.URL, "GetState")
	var twoSnap Snapshot
	_ = json.Unmarshal(b, &twoSnap)
	if len(adminSnap.Entries) != 0 || len(twoSnap.Entries) != 1 || adminSnap.Obs.URL == twoSnap.Obs.URL {
		t.Fatalf("channels not isolated: admin=%d two=%d", len(adminSnap.Entries), len(twoSnap.Entries))
	}
	resp, _ := http.Get(strings.Replace(twoSnap.Obs.URL, "http://example.test", srv.URL, 1) + "/api/state")
	var ov OverlayState
	_ = json.NewDecoder(resp.Body).Decode(&ov)
	resp.Body.Close()
	if ov.Totals.Entries != 1 {
		t.Fatalf("widget of channel 2: %+v", ov.Totals)
	}
	if code := post(admin, "revoke", `{"login":"admin_one"}`); code != 400 {
		t.Fatal("self revoke", code)
	}
	if code := post(admin, "revoke", `{"login":"streamer_two"}`); code != 204 {
		t.Fatal("revoke", code)
	}
	if code, _ := callWith(t, two, srv.URL, "GetState"); code != 401 {
		t.Fatal("revoked user still has access", code)
	}
	if code := login(t, two, srv.URL, "streamer_two"); code != 403 {
		t.Fatal("revoked user can log in again", code)
	}
	if rt, _ := ws.channel("200"); len(rt.ch.st.Entries) != 1 {
		t.Fatal("channel data lost on revoke")
	}
}

// Donatello у вебверсії: колбек на /donatello/<секрет каналу> з X-Key із налаштувань — донат іде в канал;
// чужий секрет, секрет віджета, неправильний ключ і GET — відхилено.
func TestServerDonatello(t *testing.T) {
	ws, srv := devServer(t)
	rt, _ := ws.channel("local")
	_, b := callAPI(t, srv, "GetState")
	var snap Snapshot
	_ = json.Unmarshal(b, &snap)
	if snap.Don.State != "off" || !strings.HasSuffix(snap.Don.URL, "/donatello/"+rt.donSecret) || rt.donSecret == rt.secret {
		t.Fatalf("don status without key: %+v", snap.Don)
	}
	callAPI(t, srv, "SaveSettings", Settings{DonKey: "kkk"})
	_, b = callAPI(t, srv, "GetState")
	_ = json.Unmarshal(b, &snap)
	if snap.Don.State != "on" {
		t.Fatalf("don status with key: %+v", snap.Don)
	}
	callAPI(t, srv, "StartTimer")
	send := func(path, key, method string) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"pubId":"p1","clientName":"Olena","message":"Hades II","amount":"120","currency":"UAH"}`))
		req.Header.Set("X-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := send("/donatello/"+rt.secret, "kkk", "POST"); c != 404 {
		t.Fatal("widget secret accepted for donations", c)
	}
	if c := send("/donatello/00000000000000000000000000000000", "kkk", "POST"); c != 404 {
		t.Fatal("unknown secret", c)
	}
	if c := send("/donatello/"+rt.donSecret, "wrong", "POST"); c != 403 {
		t.Fatal("wrong key", c)
	}
	if c := send("/donatello/"+rt.donSecret, "kkk", "GET"); c != 405 {
		t.Fatal("GET", c)
	}
	if c := send("/donatello/"+rt.donSecret, "kkk", "POST"); c != 200 {
		t.Fatal("donation", c)
	}
	send("/donatello/"+rt.donSecret, "kkk", "POST") // повтор того самого донату
	rt.ch.mu.Lock()
	n := len(rt.ch.st.Entries)
	var e Entry
	if n > 0 {
		e = rt.ch.st.Entries[0]
	}
	rt.ch.mu.Unlock()
	if n != 1 || !e.Donation || e.Text != "Hades II" || e.DonPoints != 4 {
		t.Fatalf("donation not recorded once: n=%d %+v", n, e)
	}
}

// Експорт за весь час у вебверсії — завантаження файлу свого каналу; без входу — 401.
func TestServerExport(t *testing.T) {
	_, srv := devServer(t)
	callAPI(t, srv, "StartTimer")
	callAPI(t, srv, "AddManual", "Phill", 1, 2, "Hades")
	for kind, ct := range map[string]string{"subday": "text/csv", "auction": "text/csv", "zip": "application/zip"} {
		resp, err := http.Get(srv.URL + "/api/export/" + kind)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), ct) || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
			t.Fatal(kind, resp.StatusCode, resp.Header)
		}
		if kind == "subday" && !strings.Contains(string(b), "Hades") {
			t.Fatal("subday csv without the order")
		}
	}
	if resp, _ := http.Get(srv.URL + "/api/export/nope"); resp.StatusCode != 404 {
		t.Fatal("unknown export", resp.StatusCode)
	}
}

func TestServerExportNeedsLogin(t *testing.T) {
	_, srv := authServer(t)
	if resp, _ := http.Get(srv.URL + "/api/export/zip"); resp.StatusCode != 401 {
		t.Fatal("export without login", resp.StatusCode)
	}
}

// Marbles у вебверсії: файл на завантаження, після нього можна вибирати переможця; без замовлень — 400.
func TestServerMarbles(t *testing.T) {
	ws, srv := devServer(t)
	if resp, _ := http.Get(srv.URL + "/api/marbles"); resp.StatusCode != 400 {
		t.Fatal("empty marbles must be 400", resp.StatusCode)
	}
	callAPI(t, srv, "StartTimer")
	callAPI(t, srv, "AddManual", "Phill", 1, 3, "Hades")
	callAPI(t, srv, "FinishTimer")
	resp, err := http.Get(srv.URL + "/api/marbles")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Lines") != "3" || !strings.Contains(resp.Header.Get("Content-Disposition"), "subday_marbles.csv") {
		t.Fatal(resp.StatusCode, resp.Header)
	}
	if string(b) != "\xEF\xBB\xBFHades\r\nHades\r\nHades\r\n" {
		t.Fatalf("%q", b)
	}
	rt, _ := ws.channel("local")
	rt.ch.mu.Lock()
	exported := rt.ch.st.Exported
	rt.ch.mu.Unlock()
	if !exported {
		t.Fatal("winner pick must be enabled after the Marbles file")
	}
}

// Іконка у вкладці: файли віддаються, сторінки на них посилаються.
func TestServerFavicon(t *testing.T) {
	_, srv := devServer(t)
	for p, ct := range map[string]string{"/favicon.ico": "image/x-icon", "/favicon.png": "image/png", "/apple-touch-icon.png": "image/png"} {
		resp, err := http.Get(srv.URL + p)
		if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != ct {
			t.Fatal(p, err, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		resp.Body.Close()
	}
	for _, p := range []string{"/", "/privacy", "/admin"} {
		resp, _ := http.Get(srv.URL + p)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(b), `rel="icon" href="/favicon.ico"`) {
			t.Fatal(p, "has no favicon link")
		}
	}
}

// /demo відкривається без входу, працює на вигаданих даних у браузері й не підключає місток до API.
func TestServerDemoPublic(t *testing.T) {
	_, srv := authServer(t)
	resp, err := http.Get(srv.URL + "/demo")
	if err != nil || resp.StatusCode != 200 {
		t.Fatal(err, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(b)
	if !strings.Contains(page, `/demo/demo-backend.js`) || strings.Contains(page, `src="web.js"`) || strings.Contains(page, "лише за запрошенням") {
		t.Fatal("demo page must load the demo backend, not web.js or the login page")
	}
	for _, p := range []string{"/demo/demo-backend.js", "/demo/demo.js", "/app.js", "/style.css"} {
		r, err := http.Get(srv.URL + p)
		if err != nil || r.StatusCode != 200 {
			t.Fatal(p, err, r.StatusCode)
		}
		r.Body.Close()
	}
	// API без входу так і лишається закритим
	r, _ := http.Post(srv.URL+"/api/call/GetState", "application/json", strings.NewReader("[]"))
	if r.StatusCode == 200 {
		t.Fatal("API must stay closed without login")
	}
	r.Body.Close()
}
