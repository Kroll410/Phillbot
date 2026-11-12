package main

// Локальний HTTP-сервер для OBS (Browser Source): сторінки оверлеїв + SSE з оновленнями.
// Слухає лише 127.0.0.1 і перевіряє заголовок Host (захист від DNS-rebinding). Токенів і шляхів назовні не віддає.

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed overlay
var overlayFS embed.FS

const defaultObsPort = 17841

type OverlayInfo struct {
	Running bool   `json:"running"`
	Port    int    `json:"port"`
	URL     string `json:"url"`
	Error   string `json:"error,omitempty"`
}

type LastOrder struct {
	User   string `json:"user"`
	Text   string `json:"text"`
	Points int    `json:"points"`
	Tier   int    `json:"tier"`
	Months int    `json:"months"`
	Ts     int64  `json:"ts"`
}

type OverlayTotals struct {
	Entries int `json:"entries"`
	Users   int `json:"users"`
	Orders  int `json:"orders"`
	Points  int `json:"points"`
	Marbles int `json:"marbles"`
}

type OverlayState struct {
	Session     Timer         `json:"session"`
	ServerNow   int64         `json:"serverNow"`
	RewardTitle string        `json:"rewardTitle"`
	Channel     string        `json:"channel"`
	Totals      OverlayTotals `json:"totals"`
	Top         []Group       `json:"top"`
	Last        *LastOrder    `json:"last"`
	Phase       string        `json:"phase"`  // collect | play
	Game        Timer         `json:"game"`   // таймер гри переможця
	Winner      string        `json:"winner"` // переможне замовлення
	CollN       int           `json:"collN"`
	Round       int           `json:"round"`
	Vote        OverlayVote   `json:"vote"`
}

type OverlayVote struct {
	Status     string `json:"status"`
	EndsAt     int64  `json:"endsAt"`
	FinishedAt int64  `json:"finishedAt"`
	Yes        int    `json:"yes"`
	No         int    `json:"no"`
	Result     string `json:"result"`
}

// ---------- SSE ----------

type sseHub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

func (h *sseHub) add() chan []byte {
	c := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *sseHub) remove(c chan []byte) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

func (h *sseHub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func sseFrame(name string, data any) []byte {
	b, _ := json.Marshal(data)
	return []byte("event: " + name + "\ndata: " + string(b) + "\n\n")
}

func (h *sseHub) broadcast(name string, data any) {
	if h.count() == 0 {
		return
	}
	frame := sseFrame(name, data)
	h.mu.Lock()
	for c := range h.clients {
		select {
		case c <- frame:
		default: // повільний клієнт — пропускаємо кадр
		}
	}
	h.mu.Unlock()
}

// ---------- сервер ----------

type overlayServer struct {
	a        *App
	hub      *sseHub
	notifyCh chan struct{}

	mu   sync.Mutex
	srv  *http.Server
	port int
	err  string
}

func newOverlayServer(a *App) *overlayServer {
	o := &overlayServer{a: a, hub: &sseHub{clients: map[chan []byte]struct{}{}}, notifyCh: make(chan struct{}, 1)}
	go o.stateLoop()
	return o
}

// стан змінився — надіслати оверлеям (не частіше ніж раз на 200 мс)
func (o *overlayServer) notify() {
	select {
	case o.notifyCh <- struct{}{}:
	default:
	}
}

func (o *overlayServer) stateLoop() {
	for range o.notifyCh {
		if o.hub.count() > 0 {
			o.hub.broadcast("state", o.a.overlayState())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (o *overlayServer) info() OverlayInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	inf := OverlayInfo{Running: o.srv != nil, Port: o.port, Error: o.err}
	if inf.Running {
		inf.URL = "http://127.0.0.1:" + strconv.Itoa(o.port)
	}
	return inf
}

func (o *overlayServer) start(port int) {
	if port <= 0 {
		port = defaultObsPort
	}
	var ln net.Listener
	var err error
	for p := port; p <= port+10; p++ { // якщо порт зайнятий — пробуємо наступні
		ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err == nil {
			port = p
			break
		}
	}
	if err != nil {
		msg := fmt.Sprintf("Не вдалося відкрити порт %d–%d: %v", port, port+10, err)
		o.mu.Lock()
		o.srv, o.err = nil, msg
		o.mu.Unlock()
		o.a.logf("OBS: %s", msg) // logf читає info() — не тримати o.mu
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.port, o.err = port, ""
	o.srv = &http.Server{Handler: o.handler(port), ReadHeaderTimeout: 10 * time.Second}
	go func(s *http.Server) {
		if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			o.mu.Lock()
			o.err = err.Error()
			o.mu.Unlock()
		}
	}(o.srv)
}

func (o *overlayServer) stop() {
	o.mu.Lock()
	s := o.srv
	o.srv = nil
	o.mu.Unlock()
	if s != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
		_ = s.Close()
	}
}

func (o *overlayServer) handler(port int) http.Handler {
	allowed := map[string]bool{"127.0.0.1:" + strconv.Itoa(port): true, "localhost:" + strconv.Itoa(port): true}
	pages, _ := fs.Sub(overlayFS, "overlay")
	mux := http.NewServeMux()

	mux.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) { writeJSONResp(w, o.a.overlayState()) })
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) { writeJSONResp(w, o.a.GetChat()) })
	mux.HandleFunc("/api/events", o.serveSSE)
	mux.HandleFunc("/overlay/chatrender.js", func(w http.ResponseWriter, r *http.Request) {
		b, err := assets.ReadFile("frontend/dist/chatrender.js")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Write(b)
	})
	mux.HandleFunc("/overlay/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/overlay/")
		if path.Ext(name) == "" {
			name += ".html"
		}
		b, err := fs.ReadFile(pages, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ct := map[string]string{".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8"}[path.Ext(name)]
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Write(b)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/overlay/index", http.StatusFound)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[strings.ToLower(r.Host)] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(w, r)
	})
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func (o *overlayServer) serveSSE(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	if r.URL.Query().Get("window") == "1" { // окреме вікно чату: поки підключене — вкладка «Чат» заблокована
		o.a.chatWindowDelta(1)
		defer o.a.chatWindowDelta(-1)
	}
	c := o.hub.add()
	defer o.hub.remove(c)
	w.Write([]byte("retry: 2000\n\n"))
	w.Write(sseFrame("state", o.a.overlayState()))
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

// ---------- публічний зріз стану ----------

func (a *App) overlayState() OverlayState {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.st
	groups := aggregate(st.Settings, st.Entries)
	counted := countedIDs(st.Settings, st.Entries)
	users := map[string]bool{}
	n := 0
	for _, e := range st.Entries {
		if counted[e.ID] {
			n++
			users[userKey(e)] = true
		}
	}
	counts, total := marbleCounts(groups)
	var top []Group // у топі й лічильнику — лише замовлення з балами > 0 (ті, що стануть кулями)
	for _, g := range groups {
		if g.Points > 0 {
			top = append(top, g)
		}
	}
	if len(top) > 50 {
		top = top[:50]
	}
	out := OverlayState{Session: st.Session, ServerNow: time.Now().UnixMilli(), RewardTitle: st.Settings.RewardTitle,
		Channel: a.bc.DisplayName, Top: append([]Group(nil), top...), Phase: st.Phase, Game: st.Game, Winner: st.Winner, CollN: st.CollN, Round: st.Round,
		Vote:   OverlayVote{Status: st.Vote.Status, EndsAt: st.Vote.EndsAt, FinishedAt: st.Vote.FinishedAt, Yes: st.Vote.Yes, No: st.Vote.No, Result: st.Vote.Result},
		Totals: OverlayTotals{Entries: n, Users: len(users), Orders: len(counts), Points: total, Marbles: sumInts(counts)}}
	for i := range out.Top {
		if len(out.Top[i].Users) > 10 {
			out.Top[i].Users = out.Top[i].Users[:10]
		}
	}
	if a.lastOrd != nil {
		lo := *a.lastOrd
		out.Last = &lo
	}
	return out
}

// ---------- методи для інтерфейсу ----------

func (a *App) overlayURL(p string) string {
	inf := a.overlay.info()
	if !inf.Running {
		return ""
	}
	return inf.URL + p
}

// Відкрити сторінку оверлею у звичайному браузері (для перевірки).
func (a *App) OpenOverlay(p string) {
	if u := a.overlayURL(p); u != "" && strings.HasPrefix(p, "/overlay/") {
		openURL(u)
	}
}

// Чат в окремому вікні: Edge у режимі застосунку (без адресного рядка), інакше — браузер за замовчуванням.
// Поки вікно відкрите, вкладка «Чат» у програмі заблокована.
func (a *App) OpenChatWindow() error {
	u := a.overlayURL("/overlay/chat?window=1")
	if u == "" {
		return errors.New("сервер для OBS не запущено — перевірте порт у загальних налаштуваннях")
	}
	a.mu.Lock()
	if a.chatWinClients > 0 || a.chatWinPending {
		a.mu.Unlock()
		return nil
	}
	a.chatWinPending = true
	a.emitLocked()
	a.mu.Unlock()
	go func() { // якщо вікно так і не підключилось — розблокувати вкладку
		time.Sleep(15 * time.Second)
		a.mu.Lock()
		if a.chatWinPending {
			a.chatWinPending = false
			a.emitLocked()
		}
		a.mu.Unlock()
	}()
	for _, p := range []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("LocalAppData"), `Microsoft\Edge\Application\msedge.exe`),
	} {
		if _, err := os.Stat(p); err == nil {
			cmd := exec.Command(p, "--app="+u, "--window-size=440,820", "--no-first-run", "--user-data-dir="+filepath.Join(a.baseDir, "chat-window"))
			if err := cmd.Start(); err == nil {
				a.mu.Lock()
				a.chatWinProc = cmd.Process
				a.mu.Unlock()
				go cmd.Wait()
				return nil
			}
		}
	}
	openURL(u)
	return nil
}

// Закрити окреме вікно чату і повернути чат у вкладку.
func (a *App) CloseChatWindow() {
	a.overlay.hub.broadcast("closewin", true) // сторінка вікна закриє себе сама
	a.mu.Lock()
	p := a.chatWinProc
	a.chatWinProc = nil
	a.chatWinPending = false
	a.mu.Unlock()
	if p != nil {
		_ = p.Kill()
	}
	go func() { // дати вікну відключитись, потім оновити стан
		time.Sleep(700 * time.Millisecond)
		a.mu.Lock()
		if a.chatWinClients > 0 {
			a.chatWinClients = 0 // браузер за замовчуванням не дає закрити вкладку — розблоковуємо все одно
		}
		a.emitLocked()
		a.mu.Unlock()
	}()
}

func (a *App) chatWindowDelta(d int) {
	a.mu.Lock()
	a.chatWinClients += d
	if a.chatWinClients < 0 {
		a.chatWinClients = 0
	}
	if d > 0 {
		a.chatWinPending = false
	}
	a.emitLocked()
	a.mu.Unlock()
}
