//go:build !server

package main

// Десктоп: локальний сервер віджетів OBS (127.0.0.1) і вікно чату.

import (
	"context"
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
