//go:build server

package main

// Вебсервер: інтерфейс стрімера (/), виклики методів каналу (/api/call/<Метод>), події (/api/events),
// віджети OBS за секретним посиланням (/w/<секрет>/overlay/…).

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path"
	"reflect"
	"strings"
	"sync"
	"time"
)

// webHost — Host каналу у вебверсії: події йдуть у браузер стрімера й віджети через SSE.
type webHost struct {
	ch       *Channel
	ui, ov   *sseHub // браузер стрімера / віджети OBS
	notifyCh chan struct{}
	obsURL   string // https://домен/w/<секрет>

	tokMu sync.Mutex
	tok   *Tokens // токени Twitch — у пам'яті до етапу входу (там вони підуть у БД зашифрованими)
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
func (h *webHost) Decorate(s *Snapshot) { s.Obs = OverlayInfo{Running: true, URL: h.obsURL} }
func (h *webHost) SaveTokens(t *Tokens) {
	h.tokMu.Lock()
	h.tok = t
	h.tokMu.Unlock()
}
func (h *webHost) LoadTokens() *Tokens {
	h.tokMu.Lock()
	defer h.tokMu.Unlock()
	return h.tok
}
func (h *webHost) SettingsChanged(_, _ Settings) {} // ні порту віджета, ні ngrok — сервер і так публічний
func (h *webHost) KeepWheelImage(string)         {}

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

type webServer struct {
	store  *Store
	ch     *Channel
	host   *webHost
	secret string // секрет посилання на віджети
	cancel context.CancelFunc
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func newWebServer(dbFile, channelID, public string) (*webServer, error) {
	store, err := openStore(dbFile, channelID)
	if err != nil {
		return nil, err
	}
	secret := store.meta("widget_secret:" + channelID)
	if len(secret) < 32 {
		secret = randomHex(16)
		if err := store.setMeta("widget_secret:"+channelID, secret); err != nil {
			store.Close()
			return nil, err
		}
	}
	h := &webHost{ui: newHub(), ov: newHub(), notifyCh: make(chan struct{}, 1), obsURL: public + "/w/" + secret}
	ch := newChannel(h)
	h.ch = ch
	ch.store = store
	st, ok, err := store.Load()
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("читання БД: %w", err)
	}
	if ok {
		ch.st = fixState(st)
	}
	ch.initStreamLocked()
	ch.auth = AuthInfo{Status: "need_login"}
	return &webServer{store: store, ch: ch, host: h, secret: secret}, nil
}

func (s *webServer) start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.ch.ticker(ctx)
	go s.host.overlayLoop(ctx)
}

func (s *webServer) close() {
	if s.cancel != nil {
		s.cancel()
	}
	s.ch.mu.Lock()
	s.ch.saveLocked()
	s.ch.mu.Unlock()
	_ = s.store.Close()
}

func (s *webServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/call/", s.serveCall)
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, r, s.host.ui, sseFrame("state", s.ch.GetState()))
	})
	mux.HandleFunc("/w/", s.serveWidget)
	mux.HandleFunc("/", s.serveUI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer") // секрет віджета в адресі не має витікати
		if !strings.HasPrefix(r.URL.Path, "/w/") {
			h.Set("X-Frame-Options", "DENY")
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

// ---------- виклики методів каналу ----------

var errorType = reflect.TypeOf((*error)(nil)).Elem()

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// POST /api/call/<Метод> з тілом [арг1, арг2, …] → результат методу (JSON). Помилка методу — 400 {"error": …}.
// Заголовок X-PhillBot: 1 обов'язковий: чужа сторінка не може надіслати його без дозволу CORS (захист від CSRF).
func (s *webServer) serveCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "лише POST")
		return
	}
	if r.Header.Get("X-PhillBot") != "1" {
		writeErr(w, http.StatusForbidden, "немає заголовка X-PhillBot")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/call/")
	m := reflect.ValueOf(s.ch).MethodByName(name)
	if name == "" || !m.IsValid() {
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

func (s *webServer) serveWidget(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/w/")
	sec, sub, _ := strings.Cut(rest, "/")
	if subtle.ConstantTimeCompare([]byte(sec), []byte(s.secret)) != 1 {
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
		http.Redirect(w, r, "/w/"+s.secret+"/overlay/index", http.StatusFound)
	case sub == "/api/state":
		writeJSONResp(w, s.ch.overlayState())
	case sub == "/api/chat":
		writeJSONResp(w, s.ch.GetChat())
	case sub == "/api/events":
		serveSSE(w, r, s.host.ov, sseFrame("state", s.ch.overlayState()))
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

// index.html з підключеним мостом web.js (замість Wails) перед app.js
func (s *webServer) serveUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean(r.URL.Path)
	switch p {
	case "/", "/index.html":
		b, err := assets.ReadFile("frontend/dist/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		html := strings.Replace(string(b), `<script src="app.js`, `<script src="web.js"></script>`+"\n"+`<script src="app.js`, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, html)
	case "/web.js":
		w.Header().Set("Cache-Control", "no-store")
		serveEmbedded(w, r, webFS, "web/web.js")
	default:
		serveEmbedded(w, r, assets, "frontend/dist"+p)
	}
}
