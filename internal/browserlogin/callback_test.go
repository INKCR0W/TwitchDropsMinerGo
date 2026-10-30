package browserlogin

import (
	"context"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCaptureSameDocumentRedirect(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var command map[string]any
		if c.ReadJSON(&command) != nil {
			return
		}
		if err := c.WriteJSON(map[string]any{"id": 1, "result": map[string]any{}}); err != nil {
			t.Error(err)
			return
		}
		if c.ReadJSON(&command) != nil {
			return
		}
		for _, event := range []map[string]any{
			{"method": "Page.frameNavigated", "params": map[string]any{"frame": map[string]any{"id": "main", "url": "https://www.twitch.tv/login"}}},
			{"method": "Page.navigatedWithinDocument", "params": map[string]any{"frameId": "iframe", "url": "https://www.twitch.tv/#access_token=wrong&token_type=bearer&state=valid"}},
			{"method": "Page.navigatedWithinDocument", "params": map[string]any{"frameId": "main", "url": "https://www.twitch.tv/#access_token=secret&token_type=bearer&state=valid"}},
		} {
			if err := c.WriteJSON(event); err != nil {
				t.Error(err)
				return
			}
		}
		if err := c.ReadJSON(&command); err != nil {
			t.Error(err)
			return
		}
		if command["method"] != "Browser.close" {
			t.Error("browser close missing")
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	token, err := capture(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "https://www.twitch.tv/login", "valid")
	if err != nil || token != "secret" {
		t.Fatal("same document capture failed", err)
	}
}
