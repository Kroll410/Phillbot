//go:build server

package main

// Приватність у вебверсії (вимоги Угоди розробника Twitch і політики конфіденційності PhillBot):
//   - стрімер будь-коли видаляє всі дані свого каналу (POST /api/account/delete);
//   - адміністратор на запит глядача чи донатера замінює його ім'я на «видалено» в усій історії (POST /admin/api/forget);
//   - історія зборів і аукціонів, журнал доступу — 12 місяців, потім видаляються автоматично;
//   - втрачено вхід (доступ відкликано в Twitch, токен недійсний, адміністратор прибрав зі списку) —
//     токени видаляються одразу, решта даних каналу — через 30 днів, якщо стрімер не увійде знову;
//   - /privacy, /terms (і /en/…) — документи з web/legal/ з підставленими власником і контактом.

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	historyRetention = 365 * 24 * time.Hour // історія й журнал доступу
	deauthGrace      = 30 * 24 * time.Hour  // дані каналу без дійсного входу
	legalUpdated     = "13.11.2026"
	anonName         = "видалено"
)

// ---------- втрата входу ----------

func (a *authStore) markDeauth(channel string) {
	_ = a.saveTokens(channel, nil)
	if a.s.meta("deauth_since:"+channel) == "" {
		_ = a.s.setMeta("deauth_since:"+channel, strconv.FormatInt(time.Now().UnixMilli(), 10))
	}
}

func (a *authStore) clearDeauth(channel string) {
	_, _ = a.s.db.Exec(a.s.rb(`DELETE FROM meta WHERE key = ?`), "deauth_since:"+channel)
}

// канали, що понад grace без дійсного входу
func (a *authStore) deauthExpired(grace time.Duration) []string {
	rows, err := a.s.db.Query(`SELECT key, value FROM meta WHERE key LIKE 'deauth_since:%'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	cut := time.Now().Add(-grace).UnixMilli()
	var out []string
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			continue
		}
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms < cut {
			out = append(out, strings.TrimPrefix(k, "deauth_since:"))
		}
	}
	return out
}

// ---------- зупинка й видалення каналу ----------

// stopChannel — відключити канал від Twitch і вивантажити з пам'яті (дані в БД лишаються)
func (s *webServer) stopChannel(id string) {
	s.mu.Lock()
	rt := s.chans[id]
	if rt != nil {
		delete(s.chans, id)
		delete(s.bySecret, rt.secret)
		delete(s.byDon, rt.donSecret)
	}
	s.mu.Unlock()
	if rt == nil {
		return
	}
	rt.cancel()
	rt.ch.mu.Lock()
	if rt.ch.listenCancel != nil {
		rt.ch.listenCancel()
		rt.ch.listenCancel = nil
	}
	rt.ch.saveLocked()
	rt.ch.mu.Unlock()
}

// deleteChannel — усе про канал: історія, стан, токени, секрети посилань; користувач лишається в списку без прив'язки до акаунта
func (s *webServer) deleteChannel(id string) error {
	s.mu.Lock()
	rt := s.chans[id]
	s.mu.Unlock()
	if rt != nil {
		rt.ch.mu.Lock()
		rt.ch.store = nil // жодних записів після видалення
		rt.ch.mu.Unlock()
	}
	s.stopChannel(id)
	db, rb := s.base.db, s.base.rb
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM entries WHERE channel_id = ?`, `DELETE FROM collections WHERE channel_id = ?`, `DELETE FROM streams WHERE channel_id = ?`,
		`DELETE FROM lots WHERE channel_id = ?`, `DELETE FROM bids WHERE channel_id = ?`, `DELETE FROM auctions WHERE channel_id = ?`,
		`DELETE FROM seen WHERE channel_id = ?`, `DELETE FROM channels WHERE id = ?`, `DELETE FROM channel_secrets WHERE channel_id = ?`,
	} {
		if _, err := tx.Exec(rb(q), id); err != nil {
			return err
		}
	}
	for _, k := range []string{"widget_secret:", "don_secret:", "deauth_since:"} {
		if _, err := tx.Exec(rb(`DELETE FROM meta WHERE key = ?`), k+id); err != nil {
			return err
		}
	}
	var login string
	_ = tx.QueryRow(rb(`SELECT login FROM web_users WHERE twitch_id = ?`), id).Scan(&login)
	if login != "" {
		if _, err := tx.Exec(rb(`DELETE FROM web_sessions WHERE login = ?`), login); err != nil {
			return err
		}
		if _, err := tx.Exec(rb(`UPDATE web_users SET twitch_id = NULL, display_name = NULL, last_login_at = NULL WHERE login = ?`), login); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// POST /api/account/delete — стрімер видаляє всі дані свого каналу (і виходить)
func (s *webServer) serveAccountDelete(w http.ResponseWriter, r *http.Request, rt *chanRT, u *WebUser) {
	if r.Method != http.MethodPost || r.Header.Get("X-PhillBot") != "1" {
		writeErr(w, http.StatusForbidden, "лише POST з X-PhillBot")
		return
	}
	if err := s.deleteChannel(u.TwitchID); err != nil {
		log.Printf("видалення каналу %s: %v", u.TwitchID, err)
		writeErr(w, http.StatusInternalServerError, "не вдалося видалити дані — спробуйте ще раз")
		return
	}
	s.auth.audit(u.Login, "delete-channel", u.TwitchID)
	http.SetCookie(w, s.cookie(sessionCookie, "", "/", -1))
	w.WriteHeader(http.StatusNoContent)
}

// ---------- запит глядача чи донатера: замінити ім'я на «видалено» ----------

func viewerMatch(name string) func(login, user string) bool {
	n := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "@")))
	return func(login, user string) bool {
		l := strings.ToLower(login)
		return n != "" && (l == n || strings.TrimPrefix(l, "donatello:") == n || strings.ToLower(strings.TrimSpace(user)) == n)
	}
}

func anonEntry(e *Entry) {
	e.User, e.Login, e.UserID, e.Color, e.Badges, e.Source = anonName, "", "", "", nil, ""
}
func anonBid(b *Bid) { b.User, b.Login, b.UserID, b.Color, b.Badges = anonName, "", "", "", nil }
func anonLotUser(u *LotUser) {
	u.Name, u.Login, u.Color, u.Badges = anonName, "", "", nil
}

// anonymizeState — у пам'яті каналу; повертає, скільки записів змінено
func anonymizeState(c *Channel, m func(login, user string) bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	st := &c.st
	for i := range st.Entries {
		if m(st.Entries[i].Login, st.Entries[i].User) {
			anonEntry(&st.Entries[i])
			n++
		}
	}
	for p := range st.Past {
		for i := range st.Past[p].Entries {
			if e := &st.Past[p].Entries[i]; m(e.Login, e.User) {
				anonEntry(e)
				n++
			}
		}
	}
	for i := range st.Auction.Feed {
		if b := &st.Auction.Feed[i]; m(b.Login, b.User) {
			anonBid(b)
			n++
		}
	}
	for i := range st.Auction.Lots {
		for k := range st.Auction.Lots[i].Users {
			if u := &st.Auction.Lots[i].Users[k]; m(u.Login, u.Name) {
				anonLotUser(u)
				n++
			}
		}
	}
	gifts := st.Gifts[:0]
	for _, g := range st.Gifts {
		if m(g.Login, g.Name) {
			n++
			continue
		}
		gifts = append(gifts, g)
	}
	st.Gifts = gifts
	c.saveLocked()
	c.chatMu.Lock()
	chat := c.chat[:0]
	for _, msg := range c.chat {
		if !m(msg.Login, msg.Name) {
			chat = append(chat, msg)
		}
	}
	c.chat = chat
	c.chatMu.Unlock()
	return n
}

// anonymizeDB — старі стріми й аукціони в БД (усі канали)
func (s *webServer) anonymizeDB(name string, m func(login, user string) bool) (int, error) {
	db, rb := s.base.db, s.base.rb
	like := "%" + strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "@"))) + "%"
	n := 0
	type row struct{ ch, id, aid, data string }
	scan := func(q string, withAuction bool) ([]row, error) {
		rows, err := db.Query(rb(q), like)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			var err error
			if withAuction {
				err = rows.Scan(&r.ch, &r.aid, &r.id, &r.data)
			} else {
				err = rows.Scan(&r.ch, &r.id, &r.data)
			}
			if err == nil {
				out = append(out, r)
			}
		}
		return out, rows.Err()
	}
	ents, err := scan(`SELECT channel_id, id, data FROM entries WHERE LOWER(data) LIKE ?`, false)
	if err != nil {
		return n, err
	}
	for _, r := range ents {
		var e Entry
		if json.Unmarshal([]byte(r.data), &e) != nil || !m(e.Login, e.User) {
			continue
		}
		anonEntry(&e)
		if _, err := db.Exec(rb(`UPDATE entries SET login = '', data = ? WHERE channel_id = ? AND id = ?`), mustJSON(e), r.ch, r.id); err != nil {
			return n, err
		}
		n++
	}
	bids, err := scan(`SELECT channel_id, auction_id, id, data FROM bids WHERE LOWER(data) LIKE ?`, true)
	if err != nil {
		return n, err
	}
	for _, r := range bids {
		var b Bid
		if json.Unmarshal([]byte(r.data), &b) != nil || !m(b.Login, b.User) {
			continue
		}
		anonBid(&b)
		if _, err := db.Exec(rb(`UPDATE bids SET data = ? WHERE channel_id = ? AND auction_id = ? AND id = ?`), mustJSON(b), r.ch, r.aid, r.id); err != nil {
			return n, err
		}
		n++
	}
	lots, err := scan(`SELECT channel_id, auction_id, id, data FROM lots WHERE LOWER(data) LIKE ?`, true)
	if err != nil {
		return n, err
	}
	for _, r := range lots {
		var l Lot
		if json.Unmarshal([]byte(r.data), &l) != nil {
			continue
		}
		changed := false
		for k := range l.Users {
			if u := &l.Users[k]; m(u.Login, u.Name) {
				anonLotUser(u)
				changed = true
				n++
			}
		}
		if changed {
			if _, err := db.Exec(rb(`UPDATE lots SET data = ? WHERE channel_id = ? AND auction_id = ? AND id = ?`), mustJSON(l), r.ch, r.aid, r.id); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// forgetViewer — спершу канали в пам'яті (їх Sync перепише свої рядки), потім решта історії в БД
func (s *webServer) forgetViewer(name string) (int, error) {
	m := viewerMatch(name)
	s.mu.Lock()
	rts := make([]*chanRT, 0, len(s.chans))
	for _, rt := range s.chans {
		rts = append(rts, rt)
	}
	s.mu.Unlock()
	n := 0
	for _, rt := range rts {
		n += anonymizeState(rt.ch, m)
	}
	k, err := s.anonymizeDB(name, m)
	return n + k, err
}

// ---------- автоочищення ----------

// pruneHistory — стріми й аукціони, старші за retention (крім поточних), і старий журнал доступу
func (s *webServer) pruneHistory(retention time.Duration) error {
	db, rb := s.base.db, s.base.rb
	cut := time.Now().Add(-retention).UnixMilli()
	rows, err := db.Query(`SELECT id, live FROM channels`)
	if err != nil {
		return err
	}
	type cur struct{ id, stream, auction string }
	var chans []cur
	for rows.Next() {
		var id, live string
		if rows.Scan(&id, &live) != nil {
			continue
		}
		var st struct {
			Stream  StreamInfo `json:"stream"`
			Auction struct {
				ID string `json:"id"`
			} `json:"auction"`
		}
		_ = json.Unmarshal([]byte(live), &st)
		chans = append(chans, cur{id, st.Stream.ID, st.Auction.ID})
	}
	rows.Close()
	for _, c := range chans {
		for _, q := range []string{
			`DELETE FROM entries WHERE channel_id = ? AND stream_id IN (SELECT id FROM streams WHERE channel_id = ? AND started_at < ? AND id <> ?)`,
			`DELETE FROM collections WHERE channel_id = ? AND stream_id IN (SELECT id FROM streams WHERE channel_id = ? AND started_at < ? AND id <> ?)`,
		} {
			if _, err := db.Exec(rb(q), c.id, c.id, cut, c.stream); err != nil {
				return err
			}
		}
		if _, err := db.Exec(rb(`DELETE FROM streams WHERE channel_id = ? AND started_at < ? AND id <> ?`), c.id, cut, c.stream); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM lots WHERE channel_id = ? AND auction_id IN (SELECT id FROM auctions WHERE channel_id = ? AND started_at > 0 AND started_at < ? AND id <> ?)`,
			`DELETE FROM bids WHERE channel_id = ? AND auction_id IN (SELECT id FROM auctions WHERE channel_id = ? AND started_at > 0 AND started_at < ? AND id <> ?)`,
		} {
			if _, err := db.Exec(rb(q), c.id, c.id, cut, c.auction); err != nil {
				return err
			}
		}
		if _, err := db.Exec(rb(`DELETE FROM auctions WHERE channel_id = ? AND started_at > 0 AND started_at < ? AND id <> ?`), c.id, cut, c.auction); err != nil {
			return err
		}
	}
	_, err = db.Exec(rb(`DELETE FROM audit_log WHERE ts < ?`), cut)
	return err
}

// privacyCleanup — раз на добу: канали без входу понад 30 днів, історія понад 12 місяців, прострочені сесії
func (s *webServer) privacyCleanup() {
	for _, id := range s.auth.deauthExpired(deauthGrace) {
		if err := s.deleteChannel(id); err != nil {
			log.Printf("автовидалення каналу %s: %v", id, err)
			continue
		}
		s.auth.audit("system", "delete-channel", id+" (30 днів без входу)")
	}
	if err := s.pruneHistory(historyRetention); err != nil {
		log.Printf("очищення історії: %v", err)
	}
	s.auth.pruneSessions()
}

func (s *webServer) privacyLoop(ctx context.Context) {
	s.privacyCleanup()
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.privacyCleanup()
		}
	}
}

// ---------- документи: /privacy, /terms, /en/privacy, /en/terms ----------

func (s *webServer) serveLegal(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	lang := "uk"
	if strings.HasPrefix(p, "/en/") {
		lang, p = "en", strings.TrimPrefix(p, "/en")
	}
	doc := map[string]string{"/privacy": "privacy", "/terms": "terms"}[p]
	b, err := webFS.ReadFile("web/legal/" + doc + "." + lang + ".html")
	if doc == "" || err != nil {
		http.NotFound(w, r)
		return
	}
	op := s.cfg.Operator
	if op == "" {
		op = "PhillBot"
	}
	page := strings.NewReplacer("{{OPERATOR}}", html.EscapeString(op), "{{CONTACT}}", html.EscapeString(s.cfg.Contact),
		"{{UPDATED}}", legalUpdated, "{{PUBLIC_URL}}", html.EscapeString(s.cfg.Public)).Replace(string(b))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.WriteString(w, page)
}
