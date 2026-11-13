//go:build !server

package main

// Вхід у Twitch на десктопі: Device Code flow.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

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
