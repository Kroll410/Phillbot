//go:build server

package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func countRows(t *testing.T, ws *webServer, table, col, id string) int {
	t.Helper()
	var n int
	if err := ws.base.db.QueryRow(ws.base.rb(`SELECT COUNT(*) FROM `+table+` WHERE `+col+` = ?`), id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Стрімер видаляє всі дані свого каналу: історія, стан, токени, сесії; у списку доступу він лишається.
func TestPrivacyDeleteOwnChannel(t *testing.T) {
	ws, srv := authServer(t)
	admin := newClient()
	login(t, admin, srv.URL, "admin_one")
	callWith(t, admin, srv.URL, "StartTimer")
	callWith(t, admin, srv.URL, "AddManual", "Viewer", 1, 2, "Hades")
	callWith(t, admin, srv.URL, "NewStream")
	if countRows(t, ws, "entries", "channel_id", "100") == 0 {
		t.Fatal("setup: no entries")
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/account/delete", nil)
	resp, _ := admin.Do(req)
	if resp.StatusCode != 403 { // без X-PhillBot
		t.Fatal("csrf", resp.StatusCode)
	}
	req.Header.Set("X-PhillBot", "1")
	resp, _ = admin.Do(req)
	if resp.StatusCode != 204 {
		t.Fatal("delete", resp.StatusCode)
	}
	for _, tb := range []string{"entries", "streams", "collections", "channel_secrets"} {
		if n := countRows(t, ws, tb, "channel_id", "100"); n != 0 {
			t.Fatalf("%s left: %d", tb, n)
		}
	}
	if countRows(t, ws, "channels", "id", "100") != 0 || ws.auth.loadTokens("100") != nil {
		t.Fatal("channel state or tokens left")
	}
	if code, _ := callWith(t, admin, srv.URL, "GetState"); code != 401 {
		t.Fatal("session must end", code)
	}
	if u, ok := ws.auth.user("admin_one"); !ok || !u.Allowed || u.TwitchID != "" {
		t.Fatalf("whitelist row: %+v", u)
	}
	if code := login(t, admin, srv.URL, "admin_one"); code != 302 { // можна почати з чистого аркуша
		t.Fatal("login after delete", code)
	}
}

// Запит глядача: ім'я → «видалено» в пам'яті й у старій історії БД; чужі записи не чіпаються.
func TestPrivacyForgetViewer(t *testing.T) {
	ws, srv := devServer(t)
	callAPI(t, srv, "StartTimer")
	callAPI(t, srv, "AddManual", "Mira", 1, 2, "Hades")
	callAPI(t, srv, "AddManual", "Taras", 1, 2, "Doom")
	callAPI(t, srv, "NewStream") // стрім Mira тепер лише в БД
	callAPI(t, srv, "StartTimer")
	callAPI(t, srv, "AddManual", "mira", 1, 1, "Celeste") // і в поточному
	rt, _ := ws.channel("local")
	rt.ch.mu.Lock()
	rt.ch.st.Gifts = append(rt.ch.st.Gifts, GiftEvent{Ts: time.Now().UnixMilli(), GifterID: "g", Login: "mira", Name: "Mira", Count: 2})
	rt.ch.mu.Unlock()

	n, err := ws.forgetViewer("@Mira")
	if err != nil || n < 3 {
		t.Fatalf("forget: n=%d err=%v", n, err)
	}
	var cnt int
	_ = ws.base.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE LOWER(data) LIKE '%mira%'`).Scan(&cnt)
	if cnt != 0 {
		t.Fatal("Mira left in DB", cnt)
	}
	_ = ws.base.db.QueryRow(`SELECT COUNT(*) FROM entries WHERE data LIKE '%Taras%'`).Scan(&cnt)
	if cnt != 1 {
		t.Fatal("other viewer touched", cnt)
	}
	rt.ch.mu.Lock()
	cur := rt.ch.st.Entries[0]
	gifts := len(rt.ch.st.Gifts)
	rt.ch.mu.Unlock()
	if cur.User != anonName || cur.Login != "" || cur.Text != "Celeste" || gifts != 0 {
		t.Fatalf("memory not anonymized: %+v gifts=%d", cur, gifts)
	}
}

// Вхід утрачено: токени — одразу; дані — через 30 днів (якщо не увійшов); новий вхід скасовує відлік.
func TestPrivacyDeauth(t *testing.T) {
	ws, srv := authServer(t)
	admin := newClient()
	login(t, admin, srv.URL, "admin_one")
	callWith(t, admin, srv.URL, "AddManual", "Viewer", 1, 1, "Hades")
	rt, _ := ws.channel("100")
	rt.host.AuthLost()
	if ws.auth.loadTokens("100") != nil || ws.base.meta("deauth_since:100") == "" {
		t.Fatal("tokens must go at once, countdown must start")
	}
	ws.privacyCleanup() // ще не 30 днів
	if countRows(t, ws, "channels", "id", "100") != 1 {
		t.Fatal("deleted too early")
	}
	login(t, admin, srv.URL, "admin_one") // повернувся — відлік скасовано
	if ws.base.meta("deauth_since:100") != "" {
		t.Fatal("login must clear the countdown")
	}
	rt.host.AuthLost()
	_ = ws.base.setMeta("deauth_since:100", strconv.FormatInt(time.Now().Add(-31*24*time.Hour).UnixMilli(), 10))
	ws.privacyCleanup()
	if countRows(t, ws, "channels", "id", "100") != 0 || countRows(t, ws, "entries", "channel_id", "100") != 0 {
		t.Fatal("channel must be deleted after 30 days without login")
	}
}

// Адмін прибрав стрімера: канал зупинено, токени видалено, дані чекають 30 днів.
func TestPrivacyRevokeStopsChannel(t *testing.T) {
	ws, srv := authServer(t)
	admin, two := newClient(), newClient()
	login(t, admin, srv.URL, "admin_one")
	req, _ := http.NewRequest("POST", srv.URL+"/admin/api/allow", strings.NewReader(`{"login":"streamer_two"}`))
	req.Header.Set("X-PhillBot", "1")
	admin.Do(req)
	login(t, two, srv.URL, "streamer_two")
	callWith(t, two, srv.URL, "AddManual", "Viewer", 1, 1, "Hades")
	req, _ = http.NewRequest("POST", srv.URL+"/admin/api/revoke", strings.NewReader(`{"login":"streamer_two"}`))
	req.Header.Set("X-PhillBot", "1")
	if resp, _ := admin.Do(req); resp.StatusCode != 204 {
		t.Fatal("revoke", resp.StatusCode)
	}
	ws.mu.Lock()
	_, loaded := ws.chans["200"]
	ws.mu.Unlock()
	if loaded || ws.auth.loadTokens("200") != nil || ws.base.meta("deauth_since:200") == "" {
		t.Fatal("revoked channel must stop, lose tokens and start the 30-day countdown")
	}
	if countRows(t, ws, "entries", "channel_id", "200") != 1 {
		t.Fatal("data must wait 30 days")
	}
}

// Історія старша за 12 місяців видаляється; поточний стрім — ніколи.
func TestPrivacyPruneHistory(t *testing.T) {
	ws, srv := devServer(t)
	callAPI(t, srv, "StartTimer")
	callAPI(t, srv, "AddManual", "Old", 1, 1, "Hades")
	callAPI(t, srv, "NewStream")
	callAPI(t, srv, "StartTimer")
	callAPI(t, srv, "AddManual", "New", 1, 1, "Doom")
	old := time.Now().Add(-400 * 24 * time.Hour).UnixMilli()
	_, _ = ws.base.db.Exec(`UPDATE streams SET started_at = ?`, old) // і старий, і поточний — «давні»
	_, _ = ws.base.db.Exec(`INSERT INTO audit_log (ts, actor, action) VALUES (?, 'x', 'old')`, old)
	if err := ws.pruneHistory(historyRetention); err != nil {
		t.Fatal(err)
	}
	var streams, entries, audit int
	_ = ws.base.db.QueryRow(`SELECT COUNT(*) FROM streams`).Scan(&streams)
	_ = ws.base.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&entries)
	_ = ws.base.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'old'`).Scan(&audit)
	if streams != 1 || entries != 1 || audit != 0 {
		t.Fatalf("streams=%d entries=%d audit=%d (current stream must stay)", streams, entries, audit)
	}
}
