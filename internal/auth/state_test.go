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

func TestStateValidatePerformsDeviceCodeLoginAndPersistsSession(t *testing.T) {
	t.Parallel()

	var homeHits atomic.Int32
	var deviceHits atomic.Int32
	var tokenHits atomic.Int32
	var validateHits atomic.Int32
	var announcedCode DeviceCode

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
	defer server.Close()

	clientInfo := httpclient.ClientInfo{
		ClientURL: server.URL,
		ClientID:  "client-1",
		UserAgent: "unit-test-agent",
	}
	client, cookiesPath := newTestAuthHTTPClient(t, clientInfo)

	var sleeps []time.Duration
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

	snapshot := state.Snapshot()
	if snapshot.DeviceID != "device-1" {
		t.Fatalf("device_id 不匹配: %q", snapshot.DeviceID)
	}
	if snapshot.SessionID != "0123456789abcdef" {
		t.Fatalf("session_id 不匹配: %q", snapshot.SessionID)
	}
	if snapshot.AccessToken != "token-1" {
		t.Fatalf("access_token 不匹配: %q", snapshot.AccessToken)
	}
	if snapshot.UserID != 42 {
		t.Fatalf("user_id 不匹配: %d", snapshot.UserID)
	}

	if announcedCode.UserCode != "ABCD-EFGH" || announcedCode.VerificationURI == "" {
		t.Fatalf("device code 回调信息不完整: %#v", announcedCode)
	}
	if homeHits.Load() != 1 || deviceHits.Load() != 1 || tokenHits.Load() != 2 || validateHits.Load() != 1 {
		t.Fatalf("调用次数不匹配: home=%d device=%d token=%d validate=%d", homeHits.Load(), deviceHits.Load(), tokenHits.Load(), validateHits.Load())
	}
	if len(sleeps) != 2 {
		t.Fatalf("轮询次数不匹配: %#v", sleeps)
	}

	headers, err := state.HeadersProvider(HeadersOptions{GQL: true})(context.Background())
	if err != nil {
		t.Fatalf("HeadersProvider 返回错误: %v", err)
	}
	if got := headers.Get("Authorization"); got != "OAuth token-1" {
		t.Fatalf("Authorization 请求头不匹配: %q", got)
	}
	if got := headers.Get("X-Device-Id"); got != "device-1" {
		t.Fatalf("X-Device-Id 请求头不匹配: %q", got)
	}
	if got := headers.Get("Client-Session-Id"); got != "0123456789abcdef" {
		t.Fatalf("Client-Session-Id 请求头不匹配: %q", got)
	}

	reloadedJar, err := httpclient.NewPersistentJar(cookiesPath)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 失败: %v", err)
	}

	cookies := cookieMap(reloadedJar.Cookies(mustParseURL(t, server.URL)))
	if cookies["auth-token"] != "token-1" {
		t.Fatalf("auth-token 未持久化: %#v", cookies)
	}
	if cookies["persistent"] != "42" {
		t.Fatalf("persistent 未持久化: %#v", cookies)
	}
}

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

func TestStateValidateRetriesAfterInvalidCookieToken(t *testing.T) {
	t.Parallel()

	var deviceHits atomic.Int32
	var tokenHits atomic.Int32
	var validateHits atomic.Int32

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "unique_id", Value: "device-2", Path: "/"})
			_, _ = w.Write([]byte("ok"))
		case "/oauth2/device":
			deviceHits.Add(1)
			writeJSON(t, w, http.StatusOK, map[string]any{
				"device_code":      "device-code-2",
				"user_code":        "IJKL-MNOP",
				"interval":         1,
				"verification_uri": server.URL + "/activate?device-code=IJKL-MNOP",
				"expires_in":       1800,
			})
		case "/oauth2/token":
			tokenHits.Add(1)
			writeJSON(t, w, http.StatusOK, map[string]any{
				"access_token": "fresh-token",
			})
		case "/oauth2/validate":
			current := validateHits.Add(1)
			switch r.Header.Get("Authorization") {
			case "OAuth stale-token":
				if current != 1 {
					t.Fatalf("失效 token 的 validate 次数不符合预期: %d", current)
				}
				w.WriteHeader(http.StatusUnauthorized)
			case "OAuth fresh-token":
				writeJSON(t, w, http.StatusOK, map[string]any{
					"client_id": "client-retry",
					"user_id":   "88",
				})
			default:
				t.Fatalf("收到意外 Authorization: %q", r.Header.Get("Authorization"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	clientInfo := httpclient.ClientInfo{
		ClientURL: server.URL,
		ClientID:  "client-retry",
		UserAgent: "unit-test-agent",
	}
	client, cookiesPath := newTestAuthHTTPClient(t, clientInfo)
	client.CookieJar().SetCookies(mustParseURL(t, server.URL), []*http.Cookie{
		{Name: "auth-token", Value: "stale-token", Path: "/"},
		{Name: "unique_id", Value: "device-2", Path: "/"},
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
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
		DeviceCodeHandler: func(context.Context, DeviceCode) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if err := state.Validate(context.Background()); err != nil {
		t.Fatalf("Validate 返回错误: %v", err)
	}

	if snapshot := state.Snapshot(); snapshot.AccessToken != "fresh-token" || snapshot.UserID != 88 {
		t.Fatalf("重试后的认证状态不匹配: %#v", snapshot)
	}
	if deviceHits.Load() != 1 || tokenHits.Load() != 1 || validateHits.Load() != 2 {
		t.Fatalf("调用次数不匹配: device=%d token=%d validate=%d", deviceHits.Load(), tokenHits.Load(), validateHits.Load())
	}

	reloadedJar, err := httpclient.NewPersistentJar(cookiesPath)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 失败: %v", err)
	}

	cookies := cookieMap(reloadedJar.Cookies(mustParseURL(t, server.URL)))
	if cookies["auth-token"] != "fresh-token" {
		t.Fatalf("auth-token 未更新为新 token: %#v", cookies)
	}
}

func TestStateValidateClearsCookieJarOnClientMismatch(t *testing.T) {
	t.Parallel()

	var deviceHits atomic.Int32
	var tokenHits atomic.Int32
	var validateHits atomic.Int32

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "unique_id", Value: "device-3", Path: "/"})
			_, _ = w.Write([]byte("ok"))
		case "/oauth2/device":
			deviceHits.Add(1)
			writeJSON(t, w, http.StatusOK, map[string]any{
				"device_code":      "device-code-3",
				"user_code":        "QRST-UVWX",
				"interval":         1,
				"verification_uri": server.URL + "/activate?device-code=QRST-UVWX",
				"expires_in":       1800,
			})
		case "/oauth2/token":
			tokenHits.Add(1)
			writeJSON(t, w, http.StatusOK, map[string]any{
				"access_token": "matched-token",
			})
		case "/oauth2/validate":
			validateHits.Add(1)
			switch r.Header.Get("Authorization") {
			case "OAuth mismatched-token":
				writeJSON(t, w, http.StatusOK, map[string]any{
					"client_id": "other-client",
					"user_id":   "55",
				})
			case "OAuth matched-token":
				writeJSON(t, w, http.StatusOK, map[string]any{
					"client_id": "client-match",
					"user_id":   "66",
				})
			default:
				t.Fatalf("收到意外 Authorization: %q", r.Header.Get("Authorization"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	clientInfo := httpclient.ClientInfo{
		ClientURL: server.URL,
		ClientID:  "client-match",
		UserAgent: "unit-test-agent",
	}
	client, cookiesPath := newTestAuthHTTPClient(t, clientInfo)
	client.CookieJar().SetCookies(mustParseURL(t, server.URL), []*http.Cookie{
		{Name: "auth-token", Value: "mismatched-token", Path: "/"},
		{Name: "legacy", Value: "remove-me", Path: "/"},
		{Name: "unique_id", Value: "device-3", Path: "/"},
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
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
		DeviceCodeHandler: func(context.Context, DeviceCode) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if err := state.Validate(context.Background()); err != nil {
		t.Fatalf("Validate 返回错误: %v", err)
	}

	if snapshot := state.Snapshot(); snapshot.AccessToken != "matched-token" || snapshot.UserID != 66 {
		t.Fatalf("client_id 不匹配后重建的认证状态不正确: %#v", snapshot)
	}
	if deviceHits.Load() != 1 || tokenHits.Load() != 1 || validateHits.Load() != 2 {
		t.Fatalf("调用次数不匹配: device=%d token=%d validate=%d", deviceHits.Load(), tokenHits.Load(), validateHits.Load())
	}

	reloadedJar, err := httpclient.NewPersistentJar(cookiesPath)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 失败: %v", err)
	}

	cookies := cookieMap(reloadedJar.Cookies(mustParseURL(t, server.URL)))
	if cookies["legacy"] != "" {
		t.Fatalf("client_id 不匹配后应清空旧 Cookie: %#v", cookies)
	}
	if cookies["auth-token"] != "matched-token" || cookies["persistent"] != "66" {
		t.Fatalf("重建后的 Cookie 不匹配: %#v", cookies)
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
