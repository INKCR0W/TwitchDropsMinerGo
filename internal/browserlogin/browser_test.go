package browserlogin

import (
	"context"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoginURLPreservesAndroidRedirectAndState(t *testing.T) {
	raw, state, err := loginURL()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	v, _ := url.ParseQuery(u.Query().Get("redirect_params"))
	if u.Host != "www.twitch.tv" || v.Get("redirect_uri") != "https://www.twitch.tv/" || v.Get("state") != state || v.Get("response_type") != "token" || len(state) != 64 {
		t.Fatal("invalid Android authorization URL")
	}
}
func TestCallbackRequiresTrustedOriginAndState(t *testing.T) {
	for _, tt := range []struct {
		raw   string
		err   bool
		token string
	}{
		{"https://www.twitch.tv/#access_token=secret&token_type=bearer&state=valid", false, "secret"},
		{"https://evil.test/#access_token=secret&token_type=bearer&state=valid", false, ""},
		{"https://www.twitch.tv/#access_token=secret&token_type=bearer&state=wrong", true, ""},
		{"https://www.twitch.tv/#error=access_denied&state=valid", true, ""},
		{"https://www.twitch.tv/#access_token=secret&state=valid", true, ""},
		{"https://www.twitch.tv/?error=access_denied&state=valid", true, ""},
		{"https://www.twitch.tv/?error=access_denied&state=wrong", true, ""},
		{"https://www.twitch.tv/?error=access_denied&state=valid&error_description=secret", true, ""},
		{"https://www.twitch.tv/?other=value", false, ""},
		{"https://www.twitch.tv/?access_token=secret&token_type=bearer&state=valid", false, ""},
		{"https://evil.test/?error=access_denied&state=valid", false, ""},
	} {
		token, err := callbackToken(tt.raw, "valid")
		if (err != nil) != tt.err || token != tt.token {
			t.Fatal("unexpected callback result")
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Fatal("token leaked")
		}
	}
}
func TestCaptureEnablesEventsBeforeNavigation(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var command map[string]any
		if c.ReadJSON(&command) != nil {
			return
		}
		if command["method"] != "Page.enable" {
			t.Error("events not enabled first")
		}
		c.WriteJSON(map[string]any{"id": 1, "result": map[string]any{}})
		if c.ReadJSON(&command) != nil {
			return
		}
		if command["method"] != "Page.navigate" {
			t.Error("navigation missing")
		}
		c.WriteJSON(map[string]any{"method": "Page.frameNavigated", "params": map[string]any{"frame": map[string]any{"url": "https://www.twitch.tv/#access_token=secret&token_type=bearer&state=valid"}}})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	token, err := capture(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "https://www.twitch.tv/login", "valid")
	if err != nil || token != "secret" {
		t.Fatal("capture failed", err)
	}
}
func TestCaptureStopsOnCancellation(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			if _, _, err = c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := capture(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "https://www.twitch.tv/login", "valid")
	if err != context.DeadlineExceeded {
		t.Fatal("cancellation not preserved", err)
	}
}
