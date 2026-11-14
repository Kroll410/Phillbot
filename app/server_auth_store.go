//go:build server

package main

// Доступ до вебверсії: білий список (web_users), сесії (web_sessions), секрети каналів (channel_secrets) і журнал доступу.
// Запити переносні між SQLite і PostgreSQL: BIGINT/TEXT, ON CONFLICT, «?» через s.rb.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

var authSchema = []string{
	`CREATE TABLE IF NOT EXISTS web_users (login TEXT PRIMARY KEY, twitch_id TEXT, display_name TEXT,
		role TEXT NOT NULL DEFAULT 'streamer', allowed INTEGER NOT NULL DEFAULT 1,
		added_by TEXT, added_at BIGINT, last_login_at BIGINT)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS web_users_tid ON web_users (twitch_id)`,
	`CREATE TABLE IF NOT EXISTS web_sessions (id_hash TEXT PRIMARY KEY, login TEXT NOT NULL, created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS channel_secrets (channel_id TEXT NOT NULL, name TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (channel_id, name))`,
	`CREATE TABLE IF NOT EXISTS audit_log (ts BIGINT NOT NULL, actor TEXT, action TEXT NOT NULL, details TEXT)`,
}

type WebUser struct {
	Login       string `json:"login"`
	TwitchID    string `json:"twitchId,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	Role        string `json:"role"` // streamer | admin
	Allowed     bool   `json:"allowed"`
	AddedBy     string `json:"addedBy,omitempty"`
	AddedAt     int64  `json:"addedAt,omitempty"`
	LastLoginAt int64  `json:"lastLoginAt,omitempty"` // останній вхід через Twitch (OAuth)
	LastSeenAt  int64  `json:"lastSeenAt,omitempty"`  // останнє відкриття застосунку (будь-який запит із сесією)
}

type authStore struct {
	s   *Store
	key []byte // AES-256 для токенів і ключів каналів

	seenMu sync.Mutex
	seen   map[string]int64 // login → коли востаннє записали last_seen_at (пишемо не частіше ніж раз на хвилину)
}

func newAuthStore(s *Store, secret string) (*authStore, error) {
	for _, q := range authSchema {
		if _, err := s.db.Exec(q); err != nil {
			return nil, err
		}
	}
	// колонка з'явилася пізніше: у наявних базах додаємо (помилку «вже є» ігноруємо)
	_, _ = s.db.Exec(`ALTER TABLE web_users ADD COLUMN last_seen_at BIGINT`)
	k := sha256.Sum256([]byte(secret)) // PHILLBOT_KEY будь-якої довжини → 32 байти
	return &authStore{s: s, key: k[:], seen: map[string]int64{}}, nil
}

func normLogin(l string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "@")))
}

func (a *authStore) audit(actor, action, details string) {
	_, _ = a.s.db.Exec(a.s.rb(`INSERT INTO audit_log (ts, actor, action, details) VALUES (?, ?, ?, ?)`), time.Now().UnixMilli(), actor, action, details)
}

// ---------- білий список ----------

const userCols = `login, COALESCE(twitch_id, ''), COALESCE(display_name, ''), role, allowed, COALESCE(added_by, ''), COALESCE(added_at, 0), COALESCE(last_login_at, 0), COALESCE(last_seen_at, 0)`

func scanUser(r interface{ Scan(...any) error }) (WebUser, error) {
	var u WebUser
	var allowed int
	err := r.Scan(&u.Login, &u.TwitchID, &u.DisplayName, &u.Role, &allowed, &u.AddedBy, &u.AddedAt, &u.LastLoginAt, &u.LastSeenAt)
	u.Allowed = allowed == 1
	return u, err
}

func (a *authStore) users() ([]WebUser, error) {
	rows, err := a.s.db.Query(`SELECT ` + userCols + ` FROM web_users ORDER BY role, login`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WebUser{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (a *authStore) user(login string) (WebUser, bool) {
	u, err := scanUser(a.s.db.QueryRow(a.s.rb(`SELECT `+userCols+` FROM web_users WHERE login = ?`), normLogin(login)))
	return u, err == nil
}

// allow — додати логін у білий список (або повернути доступ); роль не знижується
func (a *authStore) allow(login, role, by string) error {
	login = normLogin(login)
	if !validLogin(login) {
		return errors.New("некоректний логін Twitch")
	}
	if role != "admin" {
		role = "streamer"
	}
	_, err := a.s.db.Exec(a.s.rb(`INSERT INTO web_users (login, role, allowed, added_by, added_at) VALUES (?, ?, 1, ?, ?)
		ON CONFLICT (login) DO UPDATE SET allowed = 1, role = CASE WHEN web_users.role = 'admin' THEN 'admin' ELSE excluded.role END`),
		login, role, by, time.Now().UnixMilli())
	if err == nil {
		a.audit(by, "allow", login+" ("+role+")")
	}
	return err
}

// revoke — забрати доступ і завершити всі сесії користувача
func (a *authStore) revoke(login, by string) error {
	login = normLogin(login)
	if _, err := a.s.db.Exec(a.s.rb(`UPDATE web_users SET allowed = 0 WHERE login = ?`), login); err != nil {
		return err
	}
	_, err := a.s.db.Exec(a.s.rb(`DELETE FROM web_sessions WHERE login = ?`), login)
	a.audit(by, "revoke", login)
	return err
}

func validLogin(l string) bool {
	if len(l) < 3 || len(l) > 25 {
		return false
	}
	for _, c := range l {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// loginOK — вхід через Twitch: користувач має бути в білому списку (за id або за логіном, якщо ще не входив).
// Перейменований на Twitch акаунт знаходиться за id, і логін у списку оновлюється.
func (a *authStore) loginOK(u User) (WebUser, bool) {
	now := time.Now().UnixMilli()
	login := normLogin(u.Login)
	var byID string
	_ = a.s.db.QueryRow(a.s.rb(`SELECT login FROM web_users WHERE twitch_id = ?`), u.ID).Scan(&byID)
	if byID != "" && byID != login {
		if _, ok := a.user(login); !ok { // логін змінився на Twitch
			_, _ = a.s.db.Exec(a.s.rb(`UPDATE web_users SET login = ? WHERE twitch_id = ?`), login, u.ID)
			_, _ = a.s.db.Exec(a.s.rb(`UPDATE web_sessions SET login = ? WHERE login = ?`), login, byID)
		}
	}
	wu, ok := a.user(login)
	if !ok || !wu.Allowed || (wu.TwitchID != "" && wu.TwitchID != u.ID) {
		a.audit(login, "denied", u.ID)
		return wu, false
	}
	_, _ = a.s.db.Exec(a.s.rb(`UPDATE web_users SET twitch_id = ?, display_name = ?, last_login_at = ? WHERE login = ?`), u.ID, u.DisplayName, now, login)
	a.audit(login, "login", u.ID)
	wu, _ = a.user(login)
	return wu, true
}

// touch — стрімер відкрив застосунок: оновити «останній вхід» (не частіше ніж раз на хвилину)
func (a *authStore) touch(login string) {
	now := time.Now().UnixMilli()
	a.seenMu.Lock()
	if now-a.seen[login] < 60000 {
		a.seenMu.Unlock()
		return
	}
	a.seen[login] = now
	a.seenMu.Unlock()
	_, _ = a.s.db.Exec(a.s.rb(`UPDATE web_users SET last_seen_at = ? WHERE login = ?`), now, login)
}

// ---------- сесії ----------

const sessionTTL = 30 * 24 * time.Hour

func hashToken(t string) string { h := sha256.Sum256([]byte(t)); return hex.EncodeToString(h[:]) }

// newSession — випадковий токен для cookie; у БД лише його хеш
func (a *authStore) newSession(login string) (string, error) {
	tok := randomHex(32)
	now := time.Now()
	_, err := a.s.db.Exec(a.s.rb(`INSERT INTO web_sessions (id_hash, login, created_at, expires_at) VALUES (?, ?, ?, ?)`),
		hashToken(tok), normLogin(login), now.UnixMilli(), now.Add(sessionTTL).UnixMilli())
	return tok, err
}

// session — користувач за токеном сесії; доступ перевіряється на кожен запит (видалення зі списку діє одразу)
func (a *authStore) session(tok string) (WebUser, bool) {
	if len(tok) != 64 {
		return WebUser{}, false
	}
	var login string
	var exp int64
	err := a.s.db.QueryRow(a.s.rb(`SELECT login, expires_at FROM web_sessions WHERE id_hash = ?`), hashToken(tok)).Scan(&login, &exp)
	if err != nil || exp < time.Now().UnixMilli() {
		return WebUser{}, false
	}
	u, ok := a.user(login)
	if !ok || !u.Allowed || u.TwitchID == "" {
		return WebUser{}, false
	}
	return u, true
}

func (a *authStore) endSession(tok string) {
	_, _ = a.s.db.Exec(a.s.rb(`DELETE FROM web_sessions WHERE id_hash = ?`), hashToken(tok))
}

func (a *authStore) pruneSessions() {
	_, _ = a.s.db.Exec(a.s.rb(`DELETE FROM web_sessions WHERE expires_at < ?`), time.Now().UnixMilli())
}

// ---------- зашифровані секрети каналу (токени Twitch) ----------

func (a *authStore) seal(plain []byte) (string, error) {
	blk, err := aes.NewCipher(a.key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, plain, nil)), nil
}

func (a *authStore) open(enc string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(a.key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	if len(b) < g.NonceSize() {
		return nil, errors.New("пошкоджений секрет")
	}
	return g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], nil)
}

func (a *authStore) saveTokens(channel string, t *Tokens) error {
	if t == nil {
		_, err := a.s.db.Exec(a.s.rb(`DELETE FROM channel_secrets WHERE channel_id = ? AND name = 'twitch_tokens'`), channel)
		return err
	}
	b, _ := json.Marshal(t)
	enc, err := a.seal(b)
	if err != nil {
		return err
	}
	_, err = a.s.db.Exec(a.s.rb(`INSERT INTO channel_secrets (channel_id, name, value) VALUES (?, 'twitch_tokens', ?)
		ON CONFLICT (channel_id, name) DO UPDATE SET value = excluded.value`), channel, enc)
	return err
}

func (a *authStore) loadTokens(channel string) *Tokens {
	var enc string
	if err := a.s.db.QueryRow(a.s.rb(`SELECT value FROM channel_secrets WHERE channel_id = ? AND name = 'twitch_tokens'`), channel).Scan(&enc); err != nil {
		return nil
	}
	b, err := a.open(enc)
	if err != nil {
		return nil // інший PHILLBOT_KEY — потрібен повторний вхід
	}
	var t Tokens
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return nil
	}
	return &t
}

// channelsWithTokens — канали, які треба підключити до Twitch при старті сервера
func (a *authStore) channelsWithTokens() []string {
	rows, err := a.s.db.Query(`SELECT channel_id FROM channel_secrets WHERE name = 'twitch_tokens'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// channelBySecret — канал за секретом посилання (kind: widget_secret | don_secret | view_secret)
func (a *authStore) channelBySecret(kind, secret string) string {
	var key string
	err := a.s.db.QueryRow(a.s.rb(`SELECT key FROM meta WHERE key LIKE ? AND value = ?`), kind+":%", secret).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return ""
	}
	return strings.TrimPrefix(key, kind+":")
}
