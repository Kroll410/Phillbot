//go:build server

package main

// Вебсервер PhillBot.
//   /                      інтерфейс стрімера (після входу) або сторінка входу
//   /auth/login, /auth/callback, /auth/logout   вхід через Twitch (OAuth, Confidential-клієнт), лише білий список
//   /api/call/<Метод>      виклик методу свого каналу; /api/events — події свого каналу (SSE)
//   /admin                 «Доступ»: білий список (лише адмін)
//   /w/<секрет>/overlay/…  віджети OBS каналу за секретним посиланням
//   /donatello/<секрет>    колбеки донатів Donatello для каналу (плюс X-Key із налаштувань каналу)

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

var twitchIDBase = "https://id.twitch.tv" // підміняється в тестах

type serverConfig struct {
	Public       string // https://домен (без / в кінці)
	ClientID     string
	ClientSecret string
	Key          string // PHILLBOT_KEY — шифрування токенів каналів
	Admin        string // логін Twitch першого адміна
	Operator     string // власник сервісу (для /privacy, /terms)
	Contact      string // пошта для запитів щодо даних
	Dev          bool   // без входу: один канал "local", користувач-адмін
}

func (c serverConfig) secure() bool { return strings.HasPrefix(c.Public, "https://") }

// ---------- Host каналу ----------

// webHost — Host каналу у вебверсії: події — у браузери стрімера й віджети (SSE), токени — у БД зашифровано.
type webHost struct {
	ch       *Channel
	chID     string
	auth     *authStore
	ui, ov   *sseHub // браузери стрімера / віджети OBS
	notifyCh chan struct{}
	obsURL   string // https://домен/w/<секрет>
	donURL   string // https://домен/donatello/<секрет> — вставити в Donatello → API → Колбеки
}

func newHub() *sseHub { return &sseHub{clients: map[chan []byte]struct{}{}} }

func (h *webHost) Emit(name string, data any) {
	h.ui.broadcast(name, data)
	h.ov.broadcast(name, data)
}
func (h *webHost) EmitState(s Snapshot) { h.ui.broadcast("state", s) }
func (h *webHost) Notify() {
	select {
	case h.notifyCh <- struct{}{}:
	default:
	}
}

// викликається під c.mu; Donatello «on», коли в налаштуваннях є ключ колбека
func (h *webHost) Decorate(s *Snapshot) {
	s.Obs = OverlayInfo{Running: true, URL: h.obsURL}
	s.Don = DonStatus{State: "off", URL: h.donURL}
	if strings.TrimSpace(s.Settings.DonKey) != "" {
		s.Don.State = "on"
	}
}
func (h *webHost) SaveTokens(t *Tokens) {
	if h.auth != nil {
		if err := h.auth.saveTokens(h.chID, t); err != nil {
			log.Printf("канал %s: токени не збережено: %v", h.chID, err)
		}
	}
}
func (h *webHost) LoadTokens() *Tokens {
	if h.auth == nil {
		return nil
	}
	return h.auth.loadTokens(h.chID)
}
func (h *webHost) SettingsChanged(_, _ Settings) {} // ні порту віджета, ні ngrok — сервер і так публічний
func (h *webHost) KeepWheelImage(string)         {}

// AuthLost — токени видаляються одразу; якщо стрімер не увійде знову за 30 днів, дані каналу видаляються (privacyLoop)
func (h *webHost) AuthLost() {
	if h.auth == nil {
		return
	}
	h.auth.markDeauth(h.chID)
	h.ch.setAuth(AuthInfo{Status: "error", Message: "Twitch більше не приймає вхід — увійдіть ще раз"})
}

// віджетам — їхній зріз стану, не частіше ніж раз на 200 мс
func (h *webHost) overlayLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.notifyCh:
			if h.ov.count() > 0 {
				h.ov.broadcast("state", h.ch.overlayState())
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// ---------- сервер і реєстр каналів ----------

type chanRT struct {
	cancel    context.CancelFunc // зупинити таймери, віджети й підключення каналу
	ch        *Channel
	host      *webHost
	secret    string // віджети
	donSecret string // колбеки Donatello
}

type webServer struct {
	cfg     serverConfig
	base    *Store
	auth    *authStore
	ctx     context.Context
	cancel  context.CancelFunc
	connect bool // підключати канали до Twitch (у тестах — ні)

	mu       sync.Mutex
	chans    map[string]*chanRT
	bySecret map[string]string
	byDon    map[string]string
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newWebServer(cfg serverConfig, dbFile string) (*webServer, error) {
	cfg.Public = strings.TrimRight(cfg.Public, "/")
	if !cfg.Dev {
		switch {
		case cfg.ClientID == "" || cfg.ClientSecret == "":
			return nil, errors.New("потрібні TWITCH_CLIENT_ID і TWITCH_CLIENT_SECRET (застосунок Twitch типу Confidential)")
		case len(cfg.Key) < 32:
			return nil, errors.New("потрібен PHILLBOT_KEY — щонайменше 32 випадкові символи (ним шифруються токени каналів)")
		case normLogin(cfg.Admin) == "":
			return nil, errors.New("потрібен PHILLBOT_ADMIN — логін Twitch адміністратора")
		}
	}
	base, err := openDB(dbFile, "")
	if err != nil {
		return nil, err
	}
	key := cfg.Key
	if cfg.Dev && key == "" {
		key = "dev-only-" + randomHex(16) // у розробці токени не зберігаються між запусками
	}
	auth, err := newAuthStore(base, key)
	if err != nil {
		base.Close()
		return nil, err
	}
	if a := normLogin(cfg.Admin); a != "" {
		if err := auth.allow(a, "admin", "PHILLBOT_ADMIN"); err != nil {
			base.Close()
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &webServer{cfg: cfg, base: base, auth: auth, ctx: ctx, cancel: cancel, connect: !cfg.Dev,
		chans: map[string]*chanRT{}, bySecret: map[string]string{}, byDon: map[string]string{}}, nil
}

// start — підняти канали, що вже входили (таймери й EventSub працюють і без відкритого браузера)
func (s *webServer) start() {
	for _, id := range s.auth.channelsWithTokens() {
		if _, err := s.channel(id); err != nil {
			log.Printf("канал %s: %v", id, err)
		}
	}
	if s.cfg.Dev {
		if rt, err := s.channel("local"); err == nil {
			rt.ch.mu.Lock()
			rt.ch.auth = AuthInfo{Status: "ok", User: &User{ID: "local", Login: "local", DisplayName: "Розробка"}}
			rt.ch.mu.Unlock()
		}
	}
	go s.privacyLoop(s.ctx) // раз на добу: канали без входу 30 днів, історія старша за 12 місяців, сесії
}

// channel — канал за id (id каналу Twitch); завантажується з БД при першому зверненні
func (s *webServer) channel(id string) (*chanRT, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rt := s.chans[id]; rt != nil {
		return rt, nil
	}
	store := s.base.forChannel(id)
	secret, err := channelSecret(store, "widget_secret:"+id)
	if err != nil {
		return nil, err
	}
	donSecret, err := channelSecret(store, "don_secret:"+id)
	if err != nil {
		return nil, err
	}
	h := &webHost{chID: id, auth: s.auth, ui: newHub(), ov: newHub(), notifyCh: make(chan struct{}, 1),
		obsURL: s.cfg.Public + "/w/" + secret, donURL: s.cfg.Public + "/donatello/" + donSecret}
	ch := newChannel(h)
	h.ch = ch
	ch.store = store
	ch.clientID, ch.clientSecret = s.cfg.ClientID, s.cfg.ClientSecret
	st, ok, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("читання БД: %w", err)
	}
	if ok {
		ch.st = fixState(st)
	}
	ch.initStreamLocked()
	ch.auth = AuthInfo{Status: "need_login"}
	cctx, cancel := context.WithCancel(s.ctx)
	go ch.ticker(cctx)
	go h.overlayLoop(cctx)
	rt := &chanRT{cancel: cancel, ch: ch, host: h, secret: secret, donSecret: donSecret}
	s.chans[id], s.bySecret[secret], s.byDon[donSecret] = rt, id, id
	if s.connect {
		if tok := s.auth.loadTokens(id); tok != nil {
			ch.auth = AuthInfo{Status: "checking"}
			go s.connectChannel(rt, tok)
		}
	}
	return rt, nil
}

// channelSecret — випадковий секрет посилання каналу (створюється один раз і зберігається в meta)
func channelSecret(store *Store, key string) (string, error) {
	v := store.meta(key)
	if len(v) == 32 {
		return v, nil
	}
	v = randomHex(16)
	return v, store.setMeta(key, v)
}

func (s *webServer) connectChannel(rt *chanRT, tok *Tokens) {
	if _, err := rt.ch.connect(tok); err != nil {
		rt.ch.setAuth(AuthInfo{Status: "error", Message: "Не вдалося підключитися до Twitch — увійдіть ще раз"})
		log.Printf("канал %s: %v", rt.host.chID, err)
		if errors.Is(err, errNeedLogin) {
			s.auth.markDeauth(rt.host.chID)
		}
	}
}

func (s *webServer) close() {
	s.cancel()
	s.mu.Lock()
	for _, rt := range s.chans {
		rt.ch.mu.Lock()
		if rt.ch.listenCancel != nil {
			rt.ch.listenCancel()
		}
		rt.ch.saveLocked()
		rt.ch.mu.Unlock()
	}
	s.mu.Unlock()
	_ = s.base.Close()
}

// ---------- запити ----------

const sessionCookie = "pb_session"

// user — хто робить запит (nil — не увійшов або доступ забрали)
func (s *webServer) user(r *http.Request) *WebUser {
	if s.cfg.Dev {
		return &WebUser{Login: "local", TwitchID: "local", DisplayName: "Розробка", Role: "admin", Allowed: true}
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil
	}
	u, ok := s.auth.session(c.Value)
	if !ok {
		return nil
	}
	return &u
}

func (s *webServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/login", s.serveLogin)
	mux.HandleFunc("/auth/callback", s.serveCallback)
	mux.HandleFunc("/auth/logout", s.serveLogout)
	mux.HandleFunc("/api/call/", s.withChannel(s.serveCall))
	mux.HandleFunc("/api/events", s.withChannel(func(w http.ResponseWriter, r *http.Request, rt *chanRT, _ *WebUser) {
		serveSSE(w, r, rt.host.ui, sseFrame("state", rt.ch.GetState()))
	}))
	mux.HandleFunc("/api/export/", s.withChannel(s.serveExport))
	mux.HandleFunc("/api/marbles", s.withChannel(s.serveMarbles))
	mux.HandleFunc("/api/account/delete", s.withChannel(s.serveAccountDelete))
	for _, p := range []string{"/privacy", "/terms", "/en/privacy", "/en/terms"} {
		mux.HandleFunc(p, s.serveLegal)
	}
	mux.HandleFunc("/admin", s.serveAdminPage)
	mux.HandleFunc("/admin/api/", s.serveAdminAPI)
	mux.HandleFunc("/w/", s.serveWidget)
	mux.HandleFunc("/donatello/", s.serveDonatello)
	mux.HandleFunc("/", s.serveUI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer") // секрет віджета в адресі не має витікати
		switch {
		case r.URL.Path == "/demo":
			h.Set("X-Frame-Options", "SAMEORIGIN") // кадри на сторінці /demo/all
		case !strings.HasPrefix(r.URL.Path, "/w/"):
			h.Set("X-Frame-Options", "DENY")
		}
		if s.cfg.secure() {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		defer func() {
			if v := recover(); v != nil {
				log.Printf("panic %s: %v", r.URL.Path, v)
				http.Error(w, "внутрішня помилка", http.StatusInternalServerError)
			}
		}()
		mux.ServeHTTP(w, r)
	})
}

// withChannel — лише для того, хто увійшов: його власний канал
func (s *webServer) withChannel(fn func(http.ResponseWriter, *http.Request, *chanRT, *WebUser)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.user(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "потрібно увійти")
			return
		}
		rt, err := s.channel(u.TwitchID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "канал недоступний")
			log.Printf("канал %s: %v", u.TwitchID, err)
			return
		}
		fn(w, r, rt, u)
	}
}

// ---------- вхід через Twitch ----------

func (s *webServer) cookie(name, value, p string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: p, MaxAge: maxAge, HttpOnly: true, Secure: s.cfg.secure(), SameSite: http.SameSiteLaxMode}
}

func (s *webServer) serveLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Dev {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	state := randomHex(16)
	http.SetCookie(w, s.cookie("pb_state", state, "/auth", 600))
	q := url.Values{"client_id": {s.cfg.ClientID}, "redirect_uri": {s.cfg.Public + "/auth/callback"},
		"response_type": {"code"}, "scope": {strings.Join(scopes, " ")}, "state": {state}}
	http.Redirect(w, r, twitchIDBase+"/oauth2/authorize?"+q.Encode(), http.StatusFound)
}

func (s *webServer) serveCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := r.Cookie("pb_state")
	http.SetCookie(w, s.cookie("pb_state", "", "/auth", -1))
	if err != nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		s.loginPage(w, http.StatusBadRequest, "Вхід перервано — спробуйте ще раз.")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		s.loginPage(w, http.StatusOK, "Вхід скасовано.")
		return
	}
	tok, err := s.exchangeCode(q.Get("code"))
	if err != nil {
		log.Printf("вхід: обмін коду: %v", err)
		s.loginPage(w, http.StatusBadGateway, "Twitch не підтвердив вхід — спробуйте ще раз.")
		return
	}
	u, err := fetchTwitchUser(s.cfg.ClientID, tok.AccessToken)
	if err != nil {
		log.Printf("вхід: користувач: %v", err)
		s.loginPage(w, http.StatusBadGateway, "Не вдалося отримати дані акаунта Twitch.")
		return
	}
	wu, ok := s.auth.loginOK(u)
	if !ok {
		s.loginPage(w, http.StatusForbidden, "Доступ за запрошенням: акаунта «"+u.Login+"» немає в списку. Попросіть адміністратора додати вас.")
		return
	}
	s.auth.clearDeauth(u.ID)
	if err := s.auth.saveTokens(u.ID, tok); err != nil {
		log.Printf("вхід: токени: %v", err)
		s.loginPage(w, http.StatusInternalServerError, "Не вдалося зберегти вхід.")
		return
	}
	rt, err := s.channel(u.ID)
	if err != nil {
		log.Printf("вхід: канал %s: %v", u.ID, err)
		s.loginPage(w, http.StatusInternalServerError, "Канал недоступний.")
		return
	}
	if s.connect {
		rt.ch.setAuth(AuthInfo{Status: "checking"})
		go s.connectChannel(rt, tok)
	}
	sess, err := s.auth.newSession(wu.Login)
	if err != nil {
		s.loginPage(w, http.StatusInternalServerError, "Не вдалося створити сесію.")
		return
	}
	http.SetCookie(w, s.cookie(sessionCookie, sess, "/", int(sessionTTL.Seconds())))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *webServer) exchangeCode(code string) (*Tokens, error) {
	st, b, err := postForm(twitchIDBase+"/oauth2/token", url.Values{"client_id": {s.cfg.ClientID}, "client_secret": {s.cfg.ClientSecret},
		"code": {code}, "grant_type": {"authorization_code"}, "redirect_uri": {s.cfg.Public + "/auth/callback"}})
	if err != nil {
		return nil, err
	}
	if st != 200 || str(b, "access_token") == "" {
		return nil, fmt.Errorf("HTTP %d", st)
	}
	return &Tokens{AccessToken: str(b, "access_token"), RefreshToken: str(b, "refresh_token"),
		ExpiresAt: time.Now().Add(time.Duration(num(b, "expires_in", 3600)) * time.Second).UnixMilli()}, nil
}

func fetchTwitchUser(clientID, access string) (User, error) {
	req, _ := http.NewRequest("GET", helixBase+"users", nil)
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := httpc.Do(req)
	if err != nil {
		return User{}, err
	}
	defer resp.Body.Close()
	var d struct {
		Data []User `json:"data"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d) != nil || len(d.Data) == 0 || d.Data[0].ID == "" {
		return User{}, fmt.Errorf("helix users: HTTP %d", resp.StatusCode)
	}
	return d.Data[0], nil
}

func (s *webServer) serveLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("X-PhillBot") != "1" {
		writeErr(w, http.StatusForbidden, "лише POST з X-PhillBot")
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.endSession(c.Value)
	}
	http.SetCookie(w, s.cookie(sessionCookie, "", "/", -1))
	w.WriteHeader(http.StatusNoContent)
}

// сторінка входу (і повідомлення про відмову)
func (s *webServer) loginPage(w http.ResponseWriter, code int, msg string) {
	b, err := webFS.ReadFile("web/login.html")
	if err != nil {
		http.Error(w, msg, code)
		return
	}
	page := strings.Replace(string(b), "<!--MSG-->", html.EscapeString(msg), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, page)
}

// ---------- виклики методів каналу ----------

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// методи каналу, яких у вебверсії немає: вихід — це кінець сесії (/auth/logout), а не відключення каналу від Twitch
var webDenied = map[string]bool{"Logout": true}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// POST /api/call/<Метод> з тілом [арг1, арг2, …] → результат методу (JSON). Помилка методу — 400 {"error": …}.
// Заголовок X-PhillBot: 1 обов'язковий: чужа сторінка не може надіслати його без дозволу CORS (захист від CSRF).
func (s *webServer) serveCall(w http.ResponseWriter, r *http.Request, rt *chanRT, _ *WebUser) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "лише POST")
		return
	}
	if r.Header.Get("X-PhillBot") != "1" {
		writeErr(w, http.StatusForbidden, "немає заголовка X-PhillBot")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/call/")
	m := reflect.ValueOf(rt.ch).MethodByName(name)
	if name == "" || webDenied[name] || !m.IsValid() {
		writeErr(w, http.StatusNotFound, "у вебверсії недоступно: "+name)
		return
	}
	t := m.Type()
	var raw []json.RawMessage
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err == nil && len(strings.TrimSpace(string(body))) > 0 {
		err = json.Unmarshal(body, &raw)
	}
	if err != nil || t.IsVariadic() || len(raw) != t.NumIn() {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s: очікується %d аргумент(и)", name, t.NumIn()))
		return
	}
	args := make([]reflect.Value, len(raw))
	for i := range raw {
		v := reflect.New(t.In(i))
		if err := json.Unmarshal(raw[i], v.Interface()); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("%s: аргумент %d: %v", name, i+1, err))
			return
		}
		args[i] = v.Elem()
	}
	out := m.Call(args)
	if n := len(out); n > 0 && t.Out(n-1) == errorType {
		if e, _ := out[n-1].Interface().(error); e != nil {
			writeErr(w, http.StatusBadRequest, e.Error())
			return
		}
		out = out[:n-1]
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var res any
	if len(out) > 0 {
		res = out[0].Interface()
	}
	_ = json.NewEncoder(w).Encode(res)
}

// GET /api/export/<subday|auction|zip> — експорт за весь час своїм каналом, як завантаження файлу
func (s *webServer) serveExport(w http.ResponseWriter, r *http.Request, rt *chanRT, _ *WebUser) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "лише GET")
		return
	}
	name, data, err := rt.ch.exportFile(strings.TrimPrefix(r.URL.Path, "/api/export/"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	ct := "text/csv; charset=utf-8"
	if strings.HasSuffix(name, ".zip") {
		ct = "application/zip"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="PhillBot`+path.Ext(name)+`"; filename*=UTF-8''`+url.PathEscape(name))
	_, _ = w.Write(data)
}

// GET /api/marbles — файл для Marbles on Stream як завантаження (у браузері гру не запустиш із файлом — його вибирають у грі)
func (s *webServer) serveMarbles(w http.ResponseWriter, r *http.Request, rt *chanRT, _ *WebUser) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "лише GET")
		return
	}
	data, n, err := rt.ch.marblesFile()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="subday_marbles.csv"`)
	w.Header().Set("X-Lines", strconv.Itoa(n))
	_, _ = w.Write(data)
}

// ---------- «Доступ»: білий список ----------

func (s *webServer) admin(r *http.Request) *WebUser {
	if u := s.user(r); u != nil && u.Role == "admin" {
		return u
	}
	return nil
}

func (s *webServer) serveAdminPage(w http.ResponseWriter, r *http.Request) {
	if s.admin(r) == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	serveEmbedded(w, r, webFS, "web/admin.html")
}

// GET /admin/api/users; POST /admin/api/allow {"login","role"}; POST /admin/api/revoke {"login"}
func (s *webServer) serveAdminAPI(w http.ResponseWriter, r *http.Request) {
	me := s.admin(r)
	if me == nil {
		writeErr(w, http.StatusNotFound, "немає")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	op := strings.TrimPrefix(r.URL.Path, "/admin/api/")
	if op == "users" && r.Method == http.MethodGet {
		list, err := s.auth.users()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSONResp(w, map[string]any{"me": me.Login, "users": list})
		return
	}
	if r.Method != http.MethodPost || r.Header.Get("X-PhillBot") != "1" {
		writeErr(w, http.StatusForbidden, "лише POST з X-PhillBot")
		return
	}
	var req struct{ Login, Role string }
	if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req) != nil {
		writeErr(w, http.StatusBadRequest, "некоректний запит")
		return
	}
	var err error
	switch op {
	case "allow":
		err = s.auth.allow(req.Login, req.Role, me.Login)
	case "revoke":
		if normLogin(req.Login) == me.Login {
			err = errors.New("себе прибрати не можна")
		} else if err = s.auth.revoke(req.Login, me.Login); err == nil {
			// без доступу канал не працює: токени — геть одразу, дані — через 30 днів, якщо доступ не повернуть
			if u, ok := s.auth.user(req.Login); ok && u.TwitchID != "" {
				s.stopChannel(u.TwitchID)
				s.auth.markDeauth(u.TwitchID)
			}
		}
	case "forget":
		var n int
		if n, err = s.forgetViewer(req.Login); err == nil {
			s.auth.audit(me.Login, "forget-viewer", strconv.Itoa(n)+" записів")
			writeJSONResp(w, map[string]int{"changed": n})
			return
		}
	default:
		writeErr(w, http.StatusNotFound, "немає")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- події (SSE) ----------

func serveSSE(w http.ResponseWriter, r *http.Request, hub *sseHub, first []byte) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // проксі (nginx/Caddy) не мають буферизувати потік
	c := hub.add()
	defer hub.remove(c)
	_, _ = w.Write([]byte("retry: 2000\n\n"))
	_, _ = w.Write(first)
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case frame := <-c:
			if _, err := w.Write(frame); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// ---------- віджети OBS: /w/<секрет>/overlay/<сторінка>, /w/<секрет>/api/… ----------

// secretChannel — канал за секретом посилання (віджет або Donatello); канал піднімається з БД, якщо ще не в пам'яті
func (s *webServer) secretChannel(kind, secret string) *chanRT {
	if len(secret) != 32 {
		return nil
	}
	s.mu.Lock()
	m := s.bySecret
	if kind == "don_secret" {
		m = s.byDon
	}
	id, ok := m[secret]
	s.mu.Unlock()
	if !ok {
		if id = s.auth.channelBySecret(kind, secret); id == "" {
			return nil
		}
	}
	rt, err := s.channel(id)
	if err != nil {
		return nil
	}
	want := rt.secret
	if kind == "don_secret" {
		want = rt.donSecret
	}
	if subtle.ConstantTimeCompare([]byte(want), []byte(secret)) != 1 {
		return nil
	}
	return rt
}

// POST /donatello/<секрет> — колбек Donatello для каналу; ключ X-Key перевіряє сам канал (його налаштування)
func (s *webServer) serveDonatello(w http.ResponseWriter, r *http.Request) {
	rt := s.secretChannel("don_secret", strings.Trim(strings.TrimPrefix(r.URL.Path, "/donatello/"), "/"))
	if rt == nil {
		http.NotFound(w, r)
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/donatello"
	rt.ch.donatelloHandler().ServeHTTP(w, r2)
}

func (s *webServer) serveWidget(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/w/")
	sec, sub, _ := strings.Cut(rest, "/")
	rt := s.secretChannel("widget_secret", sec)
	if rt == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch sub = path.Clean("/" + sub); {
	case sub == "/" || sub == "/overlay":
		http.Redirect(w, r, "/w/"+rt.secret+"/overlay/index", http.StatusFound)
	case sub == "/api/state":
		writeJSONResp(w, rt.ch.overlayState())
	case sub == "/api/chat":
		writeJSONResp(w, rt.ch.GetChat())
	case sub == "/api/events":
		serveSSE(w, r, rt.host.ov, sseFrame("state", rt.ch.overlayState()))
	case sub == "/overlay/chatrender.js":
		serveEmbedded(w, r, assets, "frontend/dist/chatrender.js")
	case strings.HasPrefix(sub, "/overlay/"):
		name := strings.TrimPrefix(sub, "/overlay/")
		if path.Ext(name) == "" {
			name += ".html"
		}
		serveEmbedded(w, r, overlayFS, "overlay/"+name)
	default:
		http.NotFound(w, r)
	}
}

// іконка PhillBot у вкладці браузера (і на головному екрані телефона)
const faviconTags = `<link rel="icon" href="/favicon.ico" sizes="any"><link rel="icon" type="image/png" href="/favicon.png"><link rel="apple-touch-icon" href="/apple-touch-icon.png">`

var contentTypes = map[string]string{".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8",
	".js": "text/javascript; charset=utf-8", ".woff2": "font/woff2", ".png": "image/png", ".svg": "image/svg+xml", ".ico": "image/x-icon"}

func serveEmbedded(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if ct := contentTypes[path.Ext(name)]; ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	_, _ = w.Write(b)
}

// ---------- інтерфейс стрімера ----------

// index.html з мостом web.js (замість Wails) перед app.js; хто не увійшов — сторінка входу
func (s *webServer) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean(r.URL.Path)
	switch p {
	case "/", "/index.html":
		if s.user(r) == nil {
			s.loginPage(w, http.StatusOK, "")
			return
		}
		b, err := assets.ReadFile("frontend/dist/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		page := strings.Replace(string(b), `<script src="app.js`, `<script src="web.js"></script>`+"\n"+`<script src="app.js`, 1)
		page = strings.Replace(page, "</head>", faviconTags+"</head>", 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, page)
	case "/demo":
		// публічне демо без входу: той самий інтерфейс, але «бекенд» працює в браузері з вигаданими даними —
		// жодного звернення до API, каналів чи БД
		b, err := assets.ReadFile("frontend/dist/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		page := strings.Replace(string(b), `<script src="app.js`, `<script src="/demo/demo-backend.js"></script><script src="/demo/demo.js"></script>`+"\n"+`<script src="/app.js`, 1)
		page = strings.Replace(page, "</head>", faviconTags+"</head>", 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, page)
	case "/demo/all":
		// «палітра»: усі екрани демо поруч, кожен — живе /demo?scene=… у зменшеному кадрі
		w.Header().Set("Cache-Control", "no-store")
		serveEmbedded(w, r, webFS, "web/demo/all.html")
	case "/demo/demo-backend.js", "/demo/demo.js":
		w.Header().Set("Cache-Control", "no-store")
		serveEmbedded(w, r, webFS, "web"+p)
	case "/web.js":
		w.Header().Set("Cache-Control", "no-store")
		serveEmbedded(w, r, webFS, "web/web.js")
	case "/favicon.ico", "/favicon.png", "/apple-touch-icon.png":
		w.Header().Set("Cache-Control", "public, max-age=86400")
		serveEmbedded(w, r, webFS, "web"+p)
	default:
		// статика інтерфейсу: шрифти не змінюються — місяць у кеші; app.js / style.css — година, щоб оновлення доходили швидко
		if strings.HasPrefix(p, "/fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=2592000")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		serveEmbedded(w, r, assets, "frontend/dist"+p)
	}
}
