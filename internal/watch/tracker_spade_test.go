package watch

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/httpclient"
)

func TestBuildWatchPayloadEncodesMinuteWatchedEvent(t *testing.T) {
	t.Parallel()

	channel := &domain.Channel{
		ID:    42,
		Login: "streamer",
		Stream: &domain.Stream{
			BroadcastID: 99,
		},
	}

	payload, err := BuildWatchPayload(channel, 777)
	if err != nil {
		t.Fatalf("BuildWatchPayload 返回错误: %v", err)
	}

	encoded := payload.Get("data")
	if encoded == "" {
		t.Fatal("watch payload 缺少 data 字段")
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("解码 payload 失败: %v", err)
	}

	expected := `[{"event":"minute-watched","properties":{"broadcast_id":"99","channel_id":"42","channel":"streamer","hidden":false,"live":true,"location":"channel","logged_in":true,"muted":false,"player":"site","user_id":777}}]`
	if string(decoded) != expected {
		t.Fatalf("watch payload 不匹配:\n got=%s\nwant=%s", decoded, expected)
	}
}

func TestSendWatchFallsBackToSettingsJSAndPostsFormPayload(t *testing.T) {
	t.Parallel()

	var calls []httpclient.Request
	fakeHTTP := &fakeHTTPClient{
		doFunc: func(ctx context.Context, request httpclient.Request) (httpclient.Response, error) {
			calls = append(calls, request)

			switch len(calls) {
			case 1:
				return httpclient.Response{
					StatusCode: http.StatusOK,
					Body:       []byte(`<html><script src="https://static.twitchcdn.net/config/settings.0123456789abcdef0123456789abcdef.js"></script></html>`),
				}, nil
			case 2:
				return httpclient.Response{
					StatusCode: http.StatusOK,
					Body:       []byte(`window.__settings={"spade_url":"https:\/\/spade.example.com\/track"}`),
				}, nil
			case 3:
				return httpclient.Response{StatusCode: http.StatusNoContent}, nil
			default:
				t.Fatalf("收到多余 HTTP 请求: %d", len(calls))
				return httpclient.Response{}, nil
			}
		},
	}

	tracker := newTestTracker(t, testTrackerOptions{
		httpClient: fakeHTTP,
		authState: &fakeAuthState{
			snapshot: auth.Snapshot{
				UserID:    999,
				SessionID: "session",
				DeviceID:  "device",
			},
		},
	})

	tracker.AddChannel(domain.Channel{
		ID:    88,
		Login: "watcher",
		Stream: &domain.Stream{
			BroadcastID: 1234,
		},
	})

	ok, err := tracker.SendWatch(context.Background(), 88)
	if err != nil {
		t.Fatalf("SendWatch 返回错误: %v", err)
	}
	if !ok {
		t.Fatal("204 响应应判定为 watch 成功")
	}
	if len(calls) != 3 {
		t.Fatalf("HTTP 请求次数不匹配: %d", len(calls))
	}
	if calls[2].Method != http.MethodPost {
		t.Fatalf("最后一个请求应为 POST: %#v", calls[2])
	}
	if got := calls[2].Headers.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type 不匹配: %q", got)
	}

	values, err := url.ParseQuery(string(calls[2].Body))
	if err != nil {
		t.Fatalf("解析 POST body 失败: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(values.Get("data"))
	if err != nil {
		t.Fatalf("解码 POST payload 失败: %v", err)
	}
	if !strings.Contains(string(decoded), `"broadcast_id":"1234"`) {
		t.Fatalf("payload 缺少 broadcast_id: %s", decoded)
	}
	if calls[2].URL != "https://spade.example.com/track" {
		t.Fatalf("spade_url 解析不匹配: %q", calls[2].URL)
	}
}
