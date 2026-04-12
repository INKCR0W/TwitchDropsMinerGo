package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/httpclient"
)

type validatedDeviceCodeState struct {
	state         *State
	serverURL     string
	cookiesPath   string
	announcedCode DeviceCode
	sleeps        []time.Duration
	homeHits      int32
	deviceHits    int32
	tokenHits     int32
	validateHits  int32
}

func newValidatedDeviceCodeState(t *testing.T) validatedDeviceCodeState {
	t.Helper()

	var homeHits atomic.Int32
	var deviceHits atomic.Int32
	var tokenHits atomic.Int32
	var validateHits atomic.Int32
	var announcedCode DeviceCode
	var sleeps []time.Duration

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			homeHits.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "unique_id", Value: "device-1", Path: "/"})
			_, _ = w.Write([]byte("ok"))
		case "/oauth2/device":
			deviceHits.Add(1)
			if got := r.Header.Get("X-Device-Id"); got != "device-1" {
				t.Fatalf("X-Device-Id 不匹配: %q", got)
			}
			if got := r.Header.Get("Client-Id"); got != "client-1" {
				t.Fatalf("Client-Id 不匹配: %q", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"device_code":      "device-code-1",
				"user_code":        "ABCD-EFGH",
				"interval":         2,
				"verification_uri": server.URL + "/activate?device-code=ABCD-EFGH",
				"expires_in":       1800,
			})
		case "/oauth2/token":
			current := tokenHits.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Fatalf("解析表单失败: %v", err)
			}
			if current == 1 {
				writeJSON(t, w, http.StatusBadRequest, map[string]any{
					"error": "authorization_pending",
				})
				return
			}
			if got := r.Form.Get("grant_type"); got != deviceGrantType {
				t.Fatalf("grant_type 不匹配: %q", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"access_token": "token-1",
			})
		case "/oauth2/validate":
			validateHits.Add(1)
			if got := r.Header.Get("Authorization"); got != "OAuth token-1" {
				t.Fatalf("Authorization 不匹配: %q", got)
			}
			writeJSON(t, w, http.StatusOK, map[string]any{
				"client_id": "client-1",
				"user_id":   "42",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	clientInfo := httpclient.ClientInfo{
		ClientURL: server.URL,
		ClientID:  "client-1",
		UserAgent: "unit-test-agent",
	}
	client, cookiesPath := newTestAuthHTTPClient(t, clientInfo)

	state, err := New(Options{
		HTTPClient:       client,
		ClientInfo:       clientInfo,
		DeviceEndpoint:   server.URL + "/oauth2/device",
		TokenEndpoint:    server.URL + "/oauth2/token",
		ValidateEndpoint: server.URL + "/oauth2/validate",
		SessionIDGenerator: func() (string, error) {
			return "0123456789abcdef", nil
		},
		Sleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		},
		DeviceCodeHandler: func(_ context.Context, code DeviceCode) error {
			announcedCode = code
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if err := state.Validate(context.Background()); err != nil {
		t.Fatalf("Validate 返回错误: %v", err)
	}

	return validatedDeviceCodeState{
		state:         state,
		serverURL:     server.URL,
		cookiesPath:   cookiesPath,
		announcedCode: announcedCode,
		sleeps:        append([]time.Duration(nil), sleeps...),
		homeHits:      homeHits.Load(),
		deviceHits:    deviceHits.Load(),
		tokenHits:     tokenHits.Load(),
		validateHits:  validateHits.Load(),
	}
}

func newTestAuthHTTPClient(t *testing.T, clientInfo httpclient.ClientInfo) (*httpclient.Client, string) {
	t.Helper()

	cookiesPath := filepath.Join(t.TempDir(), "cookies.json")
	client, err := httpclient.New(httpclient.Options{
		Settings:    config.Settings{ConnectionQuality: 1},
		CookiesPath: cookiesPath,
		ClientInfo:  clientInfo,
	})
	if err != nil {
		t.Fatalf("创建 HTTP 客户端失败: %v", err)
	}

	return client, cookiesPath
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析 URL 失败: %v", err)
	}

	return parsedURL
}

func cookieMap(cookies []*http.Cookie) map[string]string {
	result := make(map[string]string, len(cookies))
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		result[cookie.Name] = cookie.Value
	}

	return result
}

func writeJSON(t *testing.T, w http.ResponseWriter, statusCode int, payload any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("写入 JSON 响应失败: %v", err)
	}
}
