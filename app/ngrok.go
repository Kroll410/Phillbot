package main

// Вбудований ngrok: публічна HTTPS-адреса для колбеків Donatello (без окремого ngrok.exe).
// Потрібен authtoken з dashboard.ngrok.com; постійний домен (Domains) — необов'язково, але тоді посилання не змінюється.
// Через тунель доступний лише приймач донатів (donatelloHandler): POST /donatello з X-Key.

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.ngrok.com/ngrok/v2"
)

// ключ колбека Donatello має вигляд UUID; authtoken ngrok — ні
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// нормалізація домену з налаштувань: «abc.ngrok-free.app», «https://abc.ngrok-free.app/» → «abc.ngrok-free.app»
func ngrokDomain(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	return strings.TrimRight(s, "/")
}

// restartTunnel — (пере)запустити тунель з поточними налаштуваннями (при старті й після збереження).
func (a *App) restartTunnel() {
	a.mu.Lock()
	if a.tunStop != nil {
		a.tunStop()
		a.tunStop = nil
	}
	token, domain := strings.TrimSpace(a.st.Settings.NgrokToken), ngrokDomain(a.st.Settings.NgrokDomain)
	if token == "" {
		a.don = DonStatus{State: "off"}
		a.emitLocked()
		a.mu.Unlock()
		return
	}
	if uuidRe.MatchString(token) { // це ключ колбека Donatello, а не токен ngrok
		a.don = DonStatus{State: "error", Error: "У полі «Токен ngrok» — ключ Donatello. Сюди потрібен authtoken з dashboard.ngrok.com → Your Authtoken"}
		a.emitLocked()
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.tunStop = cancel
	a.don = DonStatus{State: "starting"}
	a.emitLocked()
	a.mu.Unlock()
	go a.runTunnel(ctx, token, domain)
}

func (a *App) setDon(ctx context.Context, s DonStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil { // тунель уже замінено новим
		return
	}
	a.don = s
	a.emitLocked()
}

// runTunnel — підключення до ngrok; якщо з'єднання обірвалось, пробує знову (5 с, 10 с … до 1 хв).
func (a *App) runTunnel(ctx context.Context, token, domain string) {
	wait := 5 * time.Second
	for ctx.Err() == nil {
		agent, err := ngrok.NewAgent(ngrok.WithAuthtoken(token), ngrok.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		if err == nil {
			var opts []ngrok.EndpointOption
			if domain != "" {
				opts = append(opts, ngrok.WithURL("https://"+domain))
			}
			// контекст — увесь час роботи тунелю: ngrok бере його і для своїх перепідключень,
			// тож короткий (скасований після Listen) контекст давав безкінечні «обриви»
			ln, lerr := agent.Listen(ctx, opts...)
			if lerr == nil {
				pub := strings.TrimRight(ln.URL().String(), "/") + "/donatello"
				a.setDon(ctx, DonStatus{State: "on", URL: pub})
				a.logf("ngrok: посилання для Donatello — %s", pub)
				wait = 5 * time.Second
				srv := &http.Server{Handler: a.donatelloHandler(), ReadHeaderTimeout: 10 * time.Second}
				done := make(chan struct{})
				go func() { _ = srv.Serve(ln); close(done) }()
				select {
				case <-ctx.Done():
					_ = srv.Close()
					_ = agent.Disconnect()
					return
				case <-done: // з'єднання з ngrok обірвалось — перепідключитися
				}
				_ = agent.Disconnect()
				a.setDon(ctx, DonStatus{State: "starting", Error: "з'єднання з ngrok обірвалось — перепідключення"})
			} else {
				err = lerr
				_ = agent.Disconnect()
			}
		}
		if err != nil {
			a.setDon(ctx, DonStatus{State: "error", Error: ngrokErrText(err)})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait < time.Minute {
			wait *= 2
		}
	}
}

// зрозумілий текст помилки ngrok
func ngrokErrText(err error) string {
	s := err.Error()
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "authtoken") || strings.Contains(low, "err_ngrok_105") || strings.Contains(low, "err_ngrok_107"):
		return "ngrok не прийняв токен — перевірте authtoken на dashboard.ngrok.com"
	case strings.Contains(low, "err_ngrok_108"):
		return "ngrok уже запущено з цим акаунтом в іншому місці (безкоштовний тариф дозволяє одне з'єднання)"
	case strings.Contains(low, "domain") || strings.Contains(low, "err_ngrok_320") || strings.Contains(low, "err_ngrok_319"):
		return "ngrok не дав цей домен — перевірте, що домен належить вашому акаунту (dashboard.ngrok.com → Domains)"
	case strings.Contains(low, "timeout") || strings.Contains(low, "dial"):
		return "немає зв'язку з ngrok — перевірте інтернет"
	}
	if len(s) > 220 {
		s = s[:220] + "…"
	}
	return "ngrok: " + s
}
