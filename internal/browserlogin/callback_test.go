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
		defer c.Close()
		var command map[string]any
		if c.ReadJSON(&command) != nil {
			return
		}
		c.WriteJSON(map[string]any{"id": 1, "result": map[string]any{}})
		if c.ReadJSON(&command) != nil {
			return
		}
		c.WriteJSON(map[string]any{"method": "Page.frameNavigated", "params": map[string]any{"frame": map[string]any{"id": "main", "url": "https://www.twitch.tv/login"}}})
		c.WriteJSON(map[string]any{"method": "Page.navigatedWithinDocument", "params": map[string]any{"frameId": "iframe", "url": "https://www.twitch.tv/#access_token=wrong&token_type=bearer&state=valid"}})
		c.WriteJSON(map[string]any{"method": "Page.navigatedWithinDocument", "params": map[string]any{"frameId": "main", "url": "https://www.twitch.tv/#access_token=secret&token_type=bearer&state=valid"}})
		c.ReadJSON(&command)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	token, err := capture(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "https://www.twitch.tv/login", "valid")
	if err != nil || token != "secret" {
		t.Fatal("same document capture failed", err)
	}
}
