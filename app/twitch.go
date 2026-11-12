package main

// Twitch: вхід (Device Code Flow), Helix API, EventSub WebSocket. Логіка перенесена з bot/main.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var scopes = []string{"channel:read:redemptions", "channel:read:subscriptions", "user:read:chat"}

const eventsubURL = "wss://eventsub.wss.twitch.tv/ws"

var errNeedLogin = errors.New("потрібен повторний вхід у Twitch")

type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
}

type Config struct {
	ClientID string `json:"clientId"`
}

type User struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
}

type Reward struct {
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Cost            int          `json:"cost"`
	IsEnabled       bool         `json:"is_enabled"`
	InputRequired   bool         `json:"is_user_input_required"`
	BackgroundColor string       `json:"background_color"`
	Image           *RewardImage `json:"image"`
	DefaultImage    *RewardImage `json:"default_image"`
}

type RewardImage struct {
	URL1x string `json:"url_1x"`
	URL2x string `json:"url_2x"`
	URL4x string `json:"url_4x"`
}

var httpc = &http.Client{Timeout: 20 * time.Second}

func postForm(u string, vals url.Values) (int, map[string]any, error) {
	resp, err := httpc.PostForm(u, vals)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body, nil
}

func str(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func num(m map[string]any, k string, def float64) float64 {
	if v, ok := m[k].(float64); ok {
		return v
	}
	return def
}

func readJSON(f string, v any) bool {
	b, err := os.ReadFile(f)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func writeJSON(f string, v any) {
	b, _ := json.MarshalIndent(v, "", "  ")
	_ = writeFileAtomic(f, b)
}

// ---------- токени ----------

func (a *App) saveTokens(b map[string]any) *Tokens {
	t := &Tokens{AccessToken: str(b, "access_token"), RefreshToken: str(b, "refresh_token"),
		ExpiresAt: time.Now().Add(time.Duration(num(b, "expires_in", 3600)) * time.Second).UnixMilli()}
	writeJSON(a.tokensFile, t)
	return t
}

func (a *App) refresh(t *Tokens) *Tokens {
	if t == nil || t.RefreshToken == "" {
		return nil
	}
	code, b, err := postForm("https://id.twitch.tv/oauth2/token", url.Values{"client_id": {a.clientID},
		"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}})
	if err != nil || code != 200 {
		return nil
	}
	return a.saveTokens(b)
}

// перевіряє збережені токени; nil — потрібен новий вхід
func (a *App) storedToken() *Tokens {
	var t Tokens
	tok := &t
	if !readJSON(a.tokensFile, tok) {
		return nil
	}
	if tok.ExpiresAt-time.Now().UnixMilli() < 5*60*1000 {
		tok = a.refresh(tok)
	}
	if tok == nil || tok.AccessToken == "" {
		return nil
	}
	req, _ := http.NewRequest("GET", "https://id.twitch.tv/oauth2/validate", nil)
	req.Header.Set("Authorization", "OAuth "+tok.AccessToken)
	resp, err := httpc.Do(req)
	if err != nil {
		return tok // офлайн — спробуємо пізніше
	}
	defer resp.Body.Close()
	var info struct {
		Scopes []string `json:"scopes"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&info)
	if resp.StatusCode != 200 {
		return a.refresh(tok)
	}
	have := map[string]bool{}
	for _, s := range info.Scopes {
		have[s] = true
	}
	for _, s := range scopes {
		if !have[s] {
			return nil
		}
	}
	return tok
}

// Device Code Flow: показує код у вікні, відкриває браузер і чекає підтвердження.
func (a *App) deviceLogin(ctx context.Context) (*Tokens, error) {
	code, b, err := postForm("https://id.twitch.tv/oauth2/device", url.Values{"client_id": {a.clientID}, "scopes": {strings.Join(scopes, " ")}})
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("не вдалося почати вхід (%d): %s — перевірте Client ID і що тип застосунку Public", code, str(b, "message"))
	}
	deviceCode, userCode, verURI := str(b, "device_code"), str(b, "user_code"), str(b, "verification_uri")
	interval := time.Duration(num(b, "interval", 5)) * time.Second
	deadline := time.Now().Add(time.Duration(num(b, "expires_in", 1800)) * time.Second)

	a.setAuth(AuthInfo{Status: "pending", Code: userCode, URI: verURI})
	openURL(verURI)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		code, tb, err := postForm("https://id.twitch.tv/oauth2/token", url.Values{
			"client_id": {a.clientID}, "scopes": {strings.Join(scopes, " ")}, "device_code": {deviceCode},
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
		if err != nil {
			continue
		}
		if code == 200 {
			return a.saveTokens(tb), nil
		}
		msg := str(tb, "message")
		if strings.Contains(msg, "authorization_pending") {
			continue
		}
		if strings.Contains(msg, "slow_down") {
			interval += 5 * time.Second
			continue
		}
		return nil, errors.New("вхід не вдався: " + msg)
	}
	return nil, errors.New("час на вхід минув — натисніть «Увійти» ще раз")
}

// ---------- Helix ----------

func (a *App) helix(method, pathq string, body any, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		a.tokMu.Lock()
		tok := a.token
		a.tokMu.Unlock()
		if tok == nil {
			return errNeedLogin
		}
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = strings.NewReader(string(b))
		}
		req, _ := http.NewRequest(method, "https://api.twitch.tv/helix/"+pathq, rdr)
		req.Header.Set("Client-Id", a.clientID)
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := httpc.Do(req)
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 401 && attempt == 0 {
			a.tokMu.Lock()
			nt := a.refresh(a.token)
			a.token = nt
			a.tokMu.Unlock()
			if nt == nil {
				a.setAuth(AuthInfo{Status: "need_login", Message: "Сесія Twitch закінчилась — увійдіть ще раз."})
				return errNeedLogin
			}
			continue
		}
		if resp.StatusCode >= 300 {
			var e map[string]any
			_ = json.Unmarshal(data, &e)
			return fmt.Errorf("%s %s → %d %s", method, strings.SplitN(pathq, "?", 2)[0], resp.StatusCode, str(e, "message"))
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	}
	return errNeedLogin
}

func (a *App) fetchUser() (User, error) {
	var users struct {
		Data []User `json:"data"`
	}
	if err := a.helix("GET", "users", nil, &users); err != nil {
		return User{}, err
	}
	if len(users.Data) == 0 {
		return User{}, errors.New("Twitch не повернув дані каналу")
	}
	return users.Data[0], nil
}

func (a *App) fetchRewards(broadcasterID string) ([]Reward, error) {
	var d struct {
		Data []Reward `json:"data"`
	}
	if err := a.helix("GET", "channel_points/custom_rewards?broadcaster_id="+broadcasterID, nil, &d); err != nil {
		return nil, err
	}
	return d.Data, nil
}

func (a *App) helixTier(broadcasterID, userID string) (int, error) {
	var d struct {
		Data []struct {
			Tier string `json:"tier"`
		} `json:"data"`
	}
	if err := a.helix("GET", "subscriptions?broadcaster_id="+broadcasterID+"&user_id="+userID, nil, &d); err != nil {
		return 0, err
	}
	if len(d.Data) == 0 {
		return 0, nil
	}
	t, _ := strconv.Atoi(d.Data[0].Tier)
	return t / 1000, nil
}

// ---------- бейджі (місяці підписки) ----------

type Badge struct {
	SetID string `json:"set_id"`
	ID    string `json:"id"`
	Info  string `json:"info"`
}

type subInfo struct {
	tier, months int
	ts           time.Time
}

func subFromBadges(badges []Badge) (tier, months int) {
	var b *Badge
	for i := range badges {
		if badges[i].SetID == "subscriber" {
			b = &badges[i]
			break
		}
	}
	if b == nil {
		for i := range badges {
			if badges[i].SetID == "founder" {
				b = &badges[i]
				break
			}
		}
	}
	if b == nil {
		return 0, 0
	}
	v, _ := strconv.Atoi(b.ID)
	switch {
	case v >= 3000:
		tier = 3
	case v >= 2000:
		tier = 2
	default:
		tier = 1
	}
	months, _ = strconv.Atoi(b.Info)
	if months < 1 {
		months = 1
	}
	return
}

// ---------- EventSub ----------

type Redemption struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	UserLogin string `json:"user_login"`
	UserName  string `json:"user_name"`
	UserInput string `json:"user_input"`
	Reward    struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Cost  int    `json:"cost"`
	} `json:"reward"`
}

type wsMsg struct {
	Metadata struct {
		MessageType      string `json:"message_type"`
		SubscriptionType string `json:"subscription_type"`
	} `json:"metadata"`
	Payload struct {
		Session struct {
			ID                      string `json:"id"`
			KeepaliveTimeoutSeconds int    `json:"keepalive_timeout_seconds"`
			ReconnectURL            string `json:"reconnect_url"`
		} `json:"session"`
		Subscription struct {
			Status string `json:"status"`
		} `json:"subscription"`
		Event json.RawMessage `json:"event"`
	} `json:"payload"`
}

func (a *App) runSession(ctx context.Context, u string, subscribe bool, bc User) (string, error) {
	ws, err := wsDial(u)
	if err != nil {
		return "", err
	}
	defer ws.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			ws.Close()
		case <-stop:
		}
	}()
	keepalive := 30
	for {
		ws.SetReadDeadline(time.Now().Add(time.Duration(keepalive+10) * time.Second))
		data, err := ws.ReadMessage()
		if err != nil {
			return "", err
		}
		var m wsMsg
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Metadata.MessageType {
		case "session_welcome":
			if m.Payload.Session.KeepaliveTimeoutSeconds > 0 {
				keepalive = m.Payload.Session.KeepaliveTimeoutSeconds
			}
			if !subscribe {
				a.setConn("on", "Слухаю канал "+bc.DisplayName)
				continue
			}
			transport := map[string]string{"method": "websocket", "session_id": m.Payload.Session.ID}
			err1 := a.helix("POST", "eventsub/subscriptions", map[string]any{"type": "channel.channel_points_custom_reward_redemption.add", "version": "1",
				"condition": map[string]string{"broadcaster_user_id": bc.ID}, "transport": transport}, nil)
			err2 := a.helix("POST", "eventsub/subscriptions", map[string]any{"type": "channel.chat.message", "version": "1",
				"condition": map[string]string{"broadcaster_user_id": bc.ID, "user_id": bc.ID}, "transport": transport}, nil)
			switch {
			case err1 != nil:
				a.setConn("error", fmtErr("Помилка підписки на нагороди", err1))
			case err2 != nil:
				a.setConn("on", "Слухаю канал "+bc.DisplayName+" (без чату: місяці підписки = 1)")
				a.logf("Помилка підписки на чат: %v", err2)
			default:
				a.setConn("on", "Слухаю канал "+bc.DisplayName)
			}
			if err2 == nil { // видалення повідомлень / очищення чату (той самий скоуп user:read:chat)
				for _, t := range []string{"channel.chat.message_delete", "channel.chat.clear_user_messages", "channel.chat.clear"} {
					if err := a.helix("POST", "eventsub/subscriptions", map[string]any{"type": t, "version": "1",
						"condition": map[string]string{"broadcaster_user_id": bc.ID, "user_id": bc.ID}, "transport": transport}, nil); err != nil {
						a.logf("Підписка %s: %v", t, err)
					}
				}
			}
		case "session_keepalive":
		case "session_reconnect":
			return m.Payload.Session.ReconnectURL, nil
		case "revocation":
			a.logf("Twitch відкликав підписку: %s", m.Payload.Subscription.Status)
		case "notification":
			if a.handleChatNotification(m.Metadata.SubscriptionType, m.Payload.Event) {
				continue
			}
			switch m.Metadata.SubscriptionType {
			case "channel.channel_points_custom_reward_redemption.add":
				var ev Redemption
				if json.Unmarshal(m.Payload.Event, &ev) == nil {
					go a.onRedemption(ev, bc)
				}
			}
		}
	}
}

func (a *App) listen(ctx context.Context, bc User) {
	next, subscribe := eventsubURL, true
	for ctx.Err() == nil {
		a.setConn("wait", "Підключення до Twitch…")
		reconnURL, err := a.runSession(ctx, next, subscribe, bc)
		if ctx.Err() != nil {
			return
		}
		if reconnURL != "" {
			next, subscribe = reconnURL, false
			continue
		}
		if errors.Is(err, errNeedLogin) {
			return
		}
		a.setConn("wait", fmt.Sprintf("Зв'язок втрачено (%v), перепідключення…", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		next, subscribe = eventsubURL, true
	}
}

// Визначає тір (Helix — точно) і місяці (бейдж з повідомлення чату, що приходить разом з redeem-ом).
func (a *App) lookupSub(bcID, userID, rewardID string) (tier, months int, source string) {
	var chat *subInfo
	for i := 0; i < 8; i++ {
		a.badgeMu.Lock()
		c, ok := a.badgeCache[userID+"|"+rewardID] // повідомлення, точно прив'язане до цієї нагороди
		if !ok || time.Since(c.ts) >= 15*time.Second {
			c, ok = a.badgeCache[userID]
		}
		a.badgeMu.Unlock()
		if ok && time.Since(c.ts) < 15*time.Second {
			chat = &c
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	apiTier, apiErr := a.helixTier(bcID, userID)
	tier = apiTier
	if apiErr != nil && chat != nil {
		tier = chat.tier
	}
	if tier > 0 {
		months = 1
		if chat != nil && chat.months > 0 {
			months = chat.months
		}
	}
	apiStr := "—"
	if apiErr != nil {
		apiStr = "помилка"
	} else if apiTier > 0 {
		apiStr = "T" + strconv.Itoa(apiTier)
	}
	badgeStr := "немає повідомлення"
	if chat != nil {
		if chat.tier > 0 {
			badgeStr = fmt.Sprintf("T%d/%d міс.", chat.tier, chat.months)
		} else {
			badgeStr = "без бейджа"
		}
	}
	source = "API: " + apiStr + "; бейдж: " + badgeStr
	return
}
