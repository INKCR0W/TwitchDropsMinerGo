package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

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
