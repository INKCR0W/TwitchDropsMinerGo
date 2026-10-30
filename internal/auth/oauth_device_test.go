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

func TestStateValidatePerformsDeviceCodeLoginAndPersistsSession(t *testing.T) {
	t.Parallel()

	fixture := newValidatedDeviceCodeState(t)

	snapshot := fixture.state.Snapshot()
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

	if fixture.announcedCode.UserCode != "ABCD-EFGH" || fixture.announcedCode.VerificationURI == "" {
		t.Fatalf("device code 回调信息不完整: %#v", fixture.announcedCode)
	}
	if fixture.homeHits != 1 || fixture.deviceHits != 1 || fixture.tokenHits != 2 || fixture.validateHits != 1 {
		t.Fatalf("调用次数不匹配: home=%d device=%d token=%d validate=%d", fixture.homeHits, fixture.deviceHits, fixture.tokenHits, fixture.validateHits)
	}
	if len(fixture.sleeps) != 2 {
		t.Fatalf("轮询次数不匹配: %#v", fixture.sleeps)
	}

	reloadedJar, err := httpclient.NewPersistentJar(fixture.cookiesPath, nil)
	if err != nil {
		t.Fatalf("重新创建 Cookie Jar 失败: %v", err)
	}

	cookies := cookieMap(reloadedJar.Cookies(mustParseURL(t, fixture.serverURL)))
	if cookies["auth-token"] != "token-1" {
		t.Fatalf("auth-token 未持久化: %#v", cookies)
	}
	if cookies["persistent"] != "42" {
		t.Fatalf("persistent 未持久化: %#v", cookies)
	}
}

func TestExplicitTVLoginMigratesAndroidTokenAndRestoresTVSession(t *testing.T) {
	t.Parallel()
	var deviceHits, tokenHits, validateHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device", "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.Form.Get("client_id") != httpclient.TVAppClient.ClientID || r.Header.Get("Client-Id") != httpclient.TVAppClient.ClientID {
				t.Error("设备授权表单和请求头必须使用 TV Client-ID")
			}
			if r.Header.Get("User-Agent") != httpclient.TVAppClient.UserAgent {
				t.Error("设备授权必须使用 TV User-Agent")
			}
			if r.URL.Path == "/device" {
				deviceHits.Add(1)
				writeJSON(t, w, http.StatusOK, map[string]any{
					"device_code": "tv-device", "user_code": "TV-CODE", "interval": 1,
					"verification_uri": "https://www.twitch.tv/activate", "expires_in": 1800,
				})
				return
			}
			tokenHits.Add(1)
			if r.Form.Get("device_code") != "tv-device" || r.Form.Get("grant_type") != deviceGrantType {
				t.Error("token 请求未关联设备码授权")
			}
			writeJSON(t, w, http.StatusOK, map[string]any{"access_token": "tv-token"})
		case "/validate":
			validateHits.Add(1)
			clientID := httpclient.TVAppClient.ClientID
			switch r.Header.Get("Authorization") {
			case "OAuth android-token":
				clientID = httpclient.AndroidAppClient.ClientID
			case "OAuth tv-token":
			default:
				t.Error("收到未知认证令牌")
			}
			writeJSON(t, w, http.StatusOK, map[string]any{"client_id": clientID, "user_id": "42"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, cookiesPath := newTestAuthHTTPClient(t, httpclient.ClientInfo{})
	client.CookieJar().SetCookies(mustParseURL(t, httpclient.TVAppClient.ClientURL), []*http.Cookie{
		{Name: "auth-token", Value: "android-token", Path: "/"},
	})
	announced := 0
	options := Options{
		HTTPClient: client, DeviceEndpoint: server.URL + "/device",
		ClientInfo:    httpclient.TVAppClient,
		TokenEndpoint: server.URL + "/token", ValidateEndpoint: server.URL + "/validate",
		Sleep:             func(context.Context, time.Duration) error { return nil },
		DeviceCodeHandler: func(context.Context, DeviceCode) error { announced++; return nil },
	}
	for attempt := range 2 {
		state, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		// 本测试只替换设备标识获取，认证和持久化走完整流程
		state.deviceID = "tv-device-id"
		headers, err := state.HeadersProvider(HeadersOptions{GQL: true})(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if headers.Get("Client-Id") != httpclient.TVAppClient.ClientID || headers.Get("Authorization") != "OAuth tv-token" {
			t.Fatal("GQL 身份与 TV 令牌不一致")
		}
		if state.Snapshot().UserID != 42 {
			t.Fatal("未保存校验后的用户身份")
		}
		if attempt == 0 {
			jar, err := httpclient.NewPersistentJar(cookiesPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			if cookieMap(jar.Cookies(mustParseURL(t, httpclient.TVAppClient.ClientURL)))["auth-token"] != "tv-token" {
				t.Fatal("TV token 未持久化")
			}
		}
	}
	if deviceHits.Load() != 1 || tokenHits.Load() != 1 || validateHits.Load() != 3 || announced != 1 {
		t.Fatalf("迁移后恢复不应重新授权: device=%d token=%d validate=%d notice=%d", deviceHits.Load(), tokenHits.Load(), validateHits.Load(), announced)
	}
}

func TestParseTokenPollingResponse(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		body      string
		interval  time.Duration
		reissue   bool
		wantError bool
	}{
		{"pending", `{"error":"authorization_pending"}`, 2 * time.Second, false, false},
		{"pending message", `{"message":"authorization_pending"}`, 2 * time.Second, false, false},
		{"slow down", `{"error":"slow_down"}`, 7 * time.Second, false, false},
		{"expired", `{"error":"expired_token"}`, 2 * time.Second, true, false},
		{"denied", `{"error":"access_denied"}`, 2 * time.Second, false, true},
		{"invalid client", `{"message":"invalid client"}`, 2 * time.Second, false, true},
		{"malformed", `<html>bad request</html>`, 2 * time.Second, false, true},
		{"missing reason", `{}`, 2 * time.Second, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			interval, reissue, err := parseTokenPollingResponse([]byte(test.body), 2*time.Second)
			if interval != test.interval || reissue != test.reissue || (err != nil) != test.wantError {
				t.Fatalf("interval=%v reissue=%v err=%v", interval, reissue, err)
			}
		})
	}
}

func TestValidateDeviceFlowInvokesAuthenticatedHandler(t *testing.T) {
	t.Parallel()

	var calls int
	newValidatedDeviceCodeState(t, func(options *Options) {
		options.AuthenticatedHandler = func() { calls++ }
	})

	if calls != 1 {
		t.Fatalf("AuthenticatedHandler 调用次数不匹配: %d", calls)
	}
}
