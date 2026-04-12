package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"twitchdropsminergo/internal/httpclient"
)

func TestStateValidateRestoresTokenFromCookie(t *testing.T) {
	t.Parallel()

	var homeHits atomic.Int32
	var deviceHits atomic.Int32
	var tokenHits atomic.Int32
	var validateHits atomic.Int32

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			homeHits.Add(1)
			_, _ = w.Write([]byte("ok"))
		case "/oauth2/device":
			deviceHits.Add(1)
			t.Fatal("恢复 cookie 会话时不应触发 device code 请求")
		case "/oauth2/token":
			tokenHits.Add(1)
			t.Fatal("恢复 cookie 会话时不应轮询 token")
		case "/oauth2/validate":
			validateHits.Add(1)
			if got := r.Header.Get("Authorization"); got != "OAuth restored-token" {
				t.Fatalf("Authorization 不匹配: %q", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"client_id": "client-restore",
				"user_id":   "77",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	clientInfo := httpclient.ClientInfo{
		ClientURL: server.URL,
		ClientID:  "client-restore",
		UserAgent: "unit-test-agent",
	}
	client, _ := newTestAuthHTTPClient(t, clientInfo)
	client.CookieJar().SetCookies(mustParseURL(t, server.URL), []*http.Cookie{
		{Name: "auth-token", Value: "restored-token", Path: "/"},
		{Name: "unique_id", Value: "restored-device", Path: "/"},
	})
	if err := client.CookieJar().Save(); err != nil {
		t.Fatalf("保存预置 Cookie 失败: %v", err)
	}

	state, err := New(Options{
		HTTPClient:       client,
		ClientInfo:       clientInfo,
		DeviceEndpoint:   server.URL + "/oauth2/device",
		TokenEndpoint:    server.URL + "/oauth2/token",
		ValidateEndpoint: server.URL + "/oauth2/validate",
		SessionIDGenerator: func() (string, error) {
			return "fedcba9876543210", nil
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if err := state.Validate(context.Background()); err != nil {
		t.Fatalf("Validate 返回错误: %v", err)
	}

	snapshot := state.Snapshot()
	if snapshot.DeviceID != "restored-device" || snapshot.AccessToken != "restored-token" || snapshot.UserID != 77 {
		t.Fatalf("恢复的认证状态不匹配: %#v", snapshot)
	}
	if homeHits.Load() != 1 || deviceHits.Load() != 0 || tokenHits.Load() != 0 || validateHits.Load() != 1 {
		t.Fatalf("调用次数不匹配: home=%d device=%d token=%d validate=%d", homeHits.Load(), deviceHits.Load(), tokenHits.Load(), validateHits.Load())
	}
}
