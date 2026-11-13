//go:build server

// PhillBot як вебсервіс: той самий інтерфейс і та сама логіка каналу, але в браузері, для кількох стрімерів.
// Збірка: go build -tags server -o phillbot-server .
//
// Налаштування — змінні оточення:
//
//	PHILLBOT_ADDR          адреса сервера (за замовчуванням 127.0.0.1:8080)
//	PHILLBOT_DATA          тека з базою даних (за замовчуванням ./data)
//	PHILLBOT_PUBLIC_URL    адреса, за якою сервер видно ззовні: https://домен (для входу через Twitch і віджетів)
//	TWITCH_CLIENT_ID       застосунок Twitch типу Confidential, OAuth Redirect URL: <PUBLIC_URL>/auth/callback
//	TWITCH_CLIENT_SECRET   його секрет
//	PHILLBOT_KEY           щонайменше 32 випадкові символи — ним шифруються токени каналів у БД (не змінювати!)
//	PHILLBOT_ADMIN         логін Twitch адміністратора: він додає інших на сторінці /admin
//	PHILLBOT_DEV=1         розробка: без входу, один канал (лише коли сервер слухає 127.0.0.1/localhost)
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
	cfg := serverConfig{
		Public:       env("PHILLBOT_PUBLIC_URL", "http://"+addr),
		ClientID:     env("TWITCH_CLIENT_ID", ""),
		ClientSecret: env("TWITCH_CLIENT_SECRET", ""),
		Key:          os.Getenv("PHILLBOT_KEY"),
		Admin:        env("PHILLBOT_ADMIN", ""),
		Dev:          os.Getenv("PHILLBOT_DEV") == "1",
	}
	if cfg.Dev && !isLoopback(addr) {
		log.Fatal("PHILLBOT_DEV=1 дозволено лише на 127.0.0.1 / localhost")
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Fatal(err)
	}
	ws, err := newWebServer(cfg, filepath.Join(dataDir, "phillbot.db"))
	if err != nil {
		log.Fatal(err)
	}
	ws.start()
	if cfg.Dev {
		log.Print("⚠ режим розробки: вхід у Twitch вимкнено")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: addr, Handler: ws.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Printf("PhillBot %s: %s", version, cfg.Public)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	ws.close()
}
