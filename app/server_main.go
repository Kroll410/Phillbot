//go:build server

// PhillBot як вебсервіс: той самий інтерфейс і та сама логіка каналу, але в браузері.
// Збірка: go build -tags server -o phillbot-server .
//
// Налаштування — змінні оточення:
//
//	PHILLBOT_ADDR        адреса сервера (за замовчуванням 127.0.0.1:8080 — лише цей комп'ютер)
//	PHILLBOT_DATA        тека з базою даних (за замовчуванням ./data)
//	PHILLBOT_PUBLIC_URL  як сервер видно ззовні, для посилань на віджети (за замовчуванням http://<ADDR>)
//	PHILLBOT_DEV=1       розробка: інтерфейс без входу в Twitch (лише коли сервер слухає 127.0.0.1/localhost)
//
// Етап 1 міграції: один канал, без входу. Вхід через Twitch і білий список — наступний етап;
// до того сервер не можна відкривати в інтернет (тому типово слухає лише 127.0.0.1).
package main

import (
	"context"
	"embed"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed web
var webFS embed.FS

func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func main() {
	addr := env("PHILLBOT_ADDR", "127.0.0.1:8080")
	dataDir := env("PHILLBOT_DATA", "data")
	public := strings.TrimRight(env("PHILLBOT_PUBLIC_URL", "http://"+addr), "/")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal(err)
	}
	ws, err := newWebServer(filepath.Join(dataDir, "phillbot.db"), "local", public)
	if err != nil {
		log.Fatal(err)
	}
	if os.Getenv("PHILLBOT_DEV") == "1" {
		if !isLoopback(addr) {
			log.Fatal("PHILLBOT_DEV=1 дозволено лише на 127.0.0.1 / localhost")
		}
		ws.ch.auth = AuthInfo{Status: "ok", User: &User{ID: "local", Login: "local", DisplayName: "Розробка"}}
		log.Print("⚠ режим розробки: вхід у Twitch вимкнено")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ws.start(ctx)

	srv := &http.Server{Addr: addr, Handler: ws.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("PhillBot %s: %s (віджет OBS: %s)", version, public, ws.host.obsURL+"/overlay/widget")
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	ws.close()
}
