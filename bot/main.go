// СабДей — тестовий бот (чорновик).
// Ловить погашення нагород за бали каналу через Twitch EventSub і показує тір, місяці підписки та бали.
package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	scopes       = []string{"channel:read:redemptions", "channel:read:subscriptions", "user:read:chat"}
	tierMult     = map[int]int{1: 1, 2: 2, 3: 3}
	nonSubPoints = 0

	baseDir    string
	configFile string
	tokensFile string
	logFile    string

	stdin = bufio.NewReader(os.Stdin)
)

// ---------- кольори / вивід ----------
func col(code, s string) string { return "\x1b[" + code + "m" + s + "\x1b[0m" }
func green(s string) string     { return col("32", s) }
func yellow(s string) string    { return col("33", s) }
func magenta(s string) string   { return col("35", s) }
func cyan(s string) string      { return col("36", s) }
func dim(s string) string       { return col("90", s) }
func red(s string) string       { return col("31", s) }
func bold(s string) string      { return col("1", s) }
func logf(format string, a ...any) {
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, a...))
}
func ask(q string) string {
	fmt.Print(q)
	s, _ := stdin.ReadString('\n')
	return strings.TrimSpace(s)
}
func fatal(err error) {
	fmt.Println(red("\nПомилка: " + err.Error()))
	ask("Натисніть Enter для виходу…")
	os.Exit(1)
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
	_ = os.WriteFile(f, b, 0600)
}

func openBrowser(u string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	case "darwin":
		_ = exec.Command("open", u).Start()
	default:
		_ = exec.Command("xdg-open", u).Start()
	}
}

// ---------- конфіг ----------
type Config struct {
	ClientID string `json:"clientId"`
}

func getClientID() string {
	var cfg Config
	if readJSON(configFile, &cfg) && cfg.ClientID != "" {
		return cfg.ClientID
	}
	fmt.Println(bold("\nПерший запуск. Потрібен Client ID застосунку з https://dev.twitch.tv/console/apps"))
	fmt.Println("Інструкція в README.txt (Client Type = Public).")
	fmt.Println()
	id := ask("Вставте Client ID: ")
	if id == "" {
		fatal(errors.New("Client ID не вказано"))
	}
	cfg.ClientID = id
	writeJSON(configFile, cfg)
	return id
}

// ---------- авторизація: Device Code Flow ----------
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"`
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

func saveTokens(b map[string]any) *Tokens {
	t := &Tokens{AccessToken: str(b, "access_token"), RefreshToken: str(b, "refresh_token"),
		ExpiresAt: time.Now().Add(time.Duration(num(b, "expires_in", 3600)) * time.Second).UnixMilli()}
	writeJSON(tokensFile, t)
	return t
}

func deviceLogin(clientID string) *Tokens {
	code, b, err := postForm("https://id.twitch.tv/oauth2/device", url.Values{"client_id": {clientID}, "scopes": {strings.Join(scopes, " ")}})
	if err != nil {
		fatal(err)
	}
	if code != 200 {
		fatal(fmt.Errorf("не вдалося почати вхід (%d): %s — перевірте Client ID і що тип застосунку Public", code, str(b, "message")))
	}
	deviceCode, userCode, verURI := str(b, "device_code"), str(b, "user_code"), str(b, "verification_uri")
	interval := time.Duration(num(b, "interval", 5)) * time.Second
	deadline := time.Now().Add(time.Duration(num(b, "expires_in", 1800)) * time.Second)

	fmt.Println("\n" + bold("Увійдіть у Twitch під акаунтом КАНАЛУ:"))
	fmt.Println("  1) Відкрийте: " + cyan(verURI))
	fmt.Println("  2) Код (якщо попросить): " + yellow(userCode) + "\n")
	openBrowser(verURI)

	for time.Now().Before(deadline) {
		time.Sleep(interval)
		code, tb, err := postForm("https://id.twitch.tv/oauth2/token", url.Values{
			"client_id": {clientID}, "scopes": {strings.Join(scopes, " ")}, "device_code": {deviceCode},
			"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}})
		if err != nil {
			continue
		}
		if code == 200 {
			logf("%s", green("Вхід успішний"))
			return saveTokens(tb)
		}
		msg := str(tb, "message")
		if strings.Contains(msg, "authorization_pending") {
			continue
		}
		if strings.Contains(msg, "slow_down") {
			time.Sleep(5 * time.Second)
			continue
		}
		fatal(errors.New("вхід не вдався: " + msg))
	}
	fatal(errors.New("час на вхід минув, запустіть знову"))
	return nil
}

func refresh(clientID string, t *Tokens) *Tokens {
	if t == nil || t.RefreshToken == "" {
		return nil
	}
	code, b, err := postForm("https://id.twitch.tv/oauth2/token", url.Values{"client_id": {clientID}, "grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}})
	if err != nil || code != 200 {
		return nil
	}
	return saveTokens(b)
}

func getToken(clientID string) *Tokens {
	var t Tokens
	tok := &t
	if !readJSON(tokensFile, tok) {
		tok = nil
	}
	if tok != nil && tok.ExpiresAt-time.Now().UnixMilli() < 5*60*1000 {
		tok = refresh(clientID, tok)
	}
	if tok != nil && tok.AccessToken != "" {
		req, _ := http.NewRequest("GET", "https://id.twitch.tv/oauth2/validate", nil)
		req.Header.Set("Authorization", "OAuth "+tok.AccessToken)
		if resp, err := httpc.Do(req); err == nil {
			var info struct {
				Scopes []string `json:"scopes"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&info)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				have := map[string]bool{}
				for _, s := range info.Scopes {
					have[s] = true
				}
				missing := []string{}
				for _, s := range scopes {
					if !have[s] {
						missing = append(missing, s)
					}
				}
				if len(missing) == 0 {
					return tok
				}
				logf(yellow("Не вистачає дозволів: %s — потрібен повторний вхід"), strings.Join(missing, ", "))
			}
		}
	}
	return deviceLogin(clientID)
}

// ---------- Helix ----------
var (
	clientID string
	token    *Tokens
	tokMu    sync.Mutex
)

func helix(method, pathq string, body any, out any) error {
	for attempt := 0; attempt < 2; attempt++ {
		var rdr io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rdr = strings.NewReader(string(b))
		}
		req, _ := http.NewRequest(method, "https://api.twitch.tv/helix/"+pathq, rdr)
		tokMu.Lock()
		req.Header.Set("Client-Id", clientID)
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		tokMu.Unlock()
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
			tokMu.Lock()
			if nt := refresh(clientID, token); nt != nil {
				token = nt
			} else {
				token = deviceLogin(clientID)
			}
			tokMu.Unlock()
			continue
		}
		if resp.StatusCode >= 300 {
			var e map[string]any
			_ = json.Unmarshal(data, &e)
			return fmt.Errorf("%s %s → %d %s", method, pathq, resp.StatusCode, str(e, "message"))
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	}
	return errors.New("не вдалося авторизуватися")
}

type User struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
}

var broadcaster User

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

var (
	badgeCache = map[string]subInfo{}
	badgeMu    sync.Mutex
)

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

func helixTier(userID string) (int, error) {
	var d struct {
		Data []struct {
			Tier string `json:"tier"`
		} `json:"data"`
	}
	if err := helix("GET", "subscriptions?broadcaster_id="+broadcaster.ID+"&user_id="+userID, nil, &d); err != nil {
		return 0, err
	}
	if len(d.Data) == 0 {
		return 0, nil
	}
	t, _ := strconv.Atoi(d.Data[0].Tier)
	return t / 1000, nil
}

// ---------- погашення ----------
type Redemption struct {
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

var printMu sync.Mutex

func onRedemption(ev Redemption) {
	var chat *subInfo
	for i := 0; i < 8; i++ {
		badgeMu.Lock()
		c, ok := badgeCache[ev.UserID]
		badgeMu.Unlock()
		if ok && time.Since(c.ts) < 15*time.Second {
			chat = &c
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	apiTier, apiErr := helixTier(ev.UserID)
	tier := apiTier
	if apiErr != nil && chat != nil {
		tier = chat.tier
	}
	months := 0
	if tier > 0 {
		months = 1
		if chat != nil && chat.months > 0 {
			months = chat.months
		}
	}
	points := nonSubPoints
	if tier > 0 {
		points = tierMult[tier] * months
	}

	apiStr := "—"
	if apiErr != nil {
		apiStr = "помилка"
	} else if apiTier > 0 {
		apiStr = strconv.Itoa(apiTier)
	}
	badgeStr := "повідомлення не знайдено"
	if chat != nil {
		if chat.tier > 0 {
			badgeStr = fmt.Sprintf("T%d/%d міс.", chat.tier, chat.months)
		} else {
			badgeStr = "немає"
		}
	}
	subStr := "не підписник"
	if tier > 0 {
		subStr = fmt.Sprintf("тір %d, %d міс.", tier, months)
	}
	input := ev.UserInput
	if input == "" {
		input = "(без тексту)"
	}

	printMu.Lock()
	fmt.Println()
	logf("%s%s%s", magenta("★ Redeem "), bold(ev.Reward.Title), dim(fmt.Sprintf("  (ціна %d, reward_id %s)", ev.Reward.Cost, ev.Reward.ID)))
	fmt.Println("   Хто:        " + bold(ev.UserName) + dim(fmt.Sprintf(" (%s, id %s)", ev.UserLogin, ev.UserID)))
	fmt.Println("   Текст:      " + yellow(input))
	fmt.Println("   Підписка:   " + subStr + dim(fmt.Sprintf("   [API тір: %s; бейдж: %s]", apiStr, badgeStr)))
	fmt.Println("   Бали:       " + green(bold(strconv.Itoa(points))))
	if apiErr != nil {
		fmt.Println("   " + dim("API: "+apiErr.Error()))
	}

	newFile := false
	if _, err := os.Stat(logFile); err != nil {
		newFile = true
	}
	if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		if newFile {
			f.WriteString("\uFEFF")
		}
		w := csv.NewWriter(f)
		if newFile {
			w.Write([]string{"час", "reward_id", "нагорода", "логін", "нік", "тір", "місяці", "бали", "замовлення"})
		}
		w.Write([]string{time.Now().Format("2006-01-02 15:04:05"), ev.Reward.ID, ev.Reward.Title, ev.UserLogin, ev.UserName,
			strconv.Itoa(tier), strconv.Itoa(months), strconv.Itoa(points), ev.UserInput})
		w.Flush()
		f.Close()
		fmt.Println("   " + dim("✓ записано в "+logFile))
	} else {
		fmt.Println("   " + red("✗ не вдалося записати в "+logFile+" — можливо, файл відкритий в Excel? Закрийте його."))
	}
	printMu.Unlock()
	recordRedeem(ev, tier, months, points)
}

// ---------- EventSub ----------
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

const eventsubURL = "wss://eventsub.wss.twitch.tv/ws"

// run one websocket session; returns reconnect URL if Twitch asked to reconnect, "" to start fresh
func runSession(u string, subscribe bool) (string, error) {
	ws, err := wsDial(u)
	if err != nil {
		return "", err
	}
	defer ws.Close()
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
				logf("Перепідключено")
				continue
			}
			sid := m.Payload.Session.ID
			transport := map[string]string{"method": "websocket", "session_id": sid}
			err1 := helix("POST", "eventsub/subscriptions", map[string]any{"type": "channel.channel_points_custom_reward_redemption.add", "version": "1",
				"condition": map[string]string{"broadcaster_user_id": broadcaster.ID}, "transport": transport}, nil)
			err2 := helix("POST", "eventsub/subscriptions", map[string]any{"type": "channel.chat.message", "version": "1",
				"condition": map[string]string{"broadcaster_user_id": broadcaster.ID, "user_id": broadcaster.ID}, "transport": transport}, nil)
			if err1 != nil {
				logf(red("Помилка підписки на нагороди: %v"), err1)
			}
			if err2 != nil {
				logf(red("Помилка підписки на чат: %v"), err2)
			}
			if err1 == nil {
				logf("%s%s%s", green("Слухаю канал "), bold(broadcaster.DisplayName), green(". Погасіть будь-яку нагороду за бали каналу для перевірки."))
				logf("%s", dim("Закрийте вікно для виходу."))
				logf("Усі redeem-и пишуться у файл: %s", cyan(logFile))
			}
		case "session_keepalive":
		case "session_reconnect":
			return m.Payload.Session.ReconnectURL, nil
		case "revocation":
			logf(red("Twitch відкликав підписку: %s"), m.Payload.Subscription.Status)
		case "notification":
			switch m.Metadata.SubscriptionType {
			case "channel.chat.message":
				var ev struct {
					ChatterUserID string  `json:"chatter_user_id"`
					Badges        []Badge `json:"badges"`
				}
				if json.Unmarshal(m.Payload.Event, &ev) == nil {
					t, mo := subFromBadges(ev.Badges)
					badgeMu.Lock()
					badgeCache[ev.ChatterUserID] = subInfo{tier: t, months: mo, ts: time.Now()}
					badgeMu.Unlock()
				}
			case "channel.channel_points_custom_reward_redemption.add":
				var ev Redemption
				if json.Unmarshal(m.Payload.Event, &ev) == nil {
					go onRedemption(ev)
				}
			}
		}
	}
}

func listen() {
	next, subscribe := eventsubURL, true
	for {
		reconnURL, err := runSession(next, subscribe)
		if reconnURL != "" {
			next, subscribe = reconnURL, false
			continue
		}
		if err != nil {
			logf(yellow("Зв'язок втрачено (%v), перепідключення через 3 с…"), err)
		}
		time.Sleep(3 * time.Second)
		next, subscribe = eventsubURL, true
	}
}

func main() {
	setupConsole()
	exe, err := os.Executable()
	if err == nil {
		baseDir = filepath.Dir(exe)
	} else {
		baseDir = "."
	}
	configFile = filepath.Join(baseDir, "config.json")
	tokensFile = filepath.Join(baseDir, "tokens.json")
	logFile = filepath.Join(baseDir, "redemptions.csv")

	fmt.Println(bold("\n=== СабДей — тестовий бот ===\n"))
	clientID = getClientID()
	token = getToken(clientID)
	disableQuickEdit()

	var users struct {
		Data []User `json:"data"`
	}
	if err := helix("GET", "users", nil, &users); err != nil || len(users.Data) == 0 {
		fatal(fmt.Errorf("не вдалося отримати дані каналу: %v", err))
	}
	broadcaster = users.Data[0]
	logf("Канал: %s%s", bold(broadcaster.DisplayName), dim(" (id "+broadcaster.ID+")"))
	if low := strings.ToLower(baseDir); strings.Contains(low, `\temp\`) || strings.Contains(low, `\tmp\`) {
		fmt.Println(yellow("\n⚠ Програму запущено прямо з архіву (тимчасова папка). Файли config/tokens/redemptions зникнуть після закриття."))
		fmt.Println(yellow("  Розпакуйте SubDayBot-test.exe в звичайну папку і запускайте звідти.\n"))
	}
	go listen()
	time.Sleep(1500 * time.Millisecond)
	logf("%s", dim("Введіть help, щоб побачити команди (marbles, list, reset…)"))
	commandLoop()
}
