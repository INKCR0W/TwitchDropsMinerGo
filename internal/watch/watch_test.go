package watch

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
)

func decodeWatchBody(t *testing.T, body []byte) string {
	t.Helper()

	form := string(body)
	encoded, ok := strings.CutPrefix(form, "data=")
	if !ok {
		t.Fatalf("watch body 缺少 data= 前缀: %q", form)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 解码失败: %v", err)
	}
	return string(raw)
}

func TestBuildWatchBodyEncodesMinuteWatchedPayload(t *testing.T) {
	t.Parallel()

	channel := &domain.Channel{
		ID:    42,
		Login: "streamer",
		Stream: &domain.Stream{
			BroadcastID: 99,
			Game:        &domain.Game{ID: 7, Name: "Apex Legends"},
		},
	}

	body, err := BuildWatchBody(channel, 777)
	if err != nil {
		t.Fatalf("BuildWatchBody 返回错误: %v", err)
	}

	got := decodeWatchBody(t, body)
	expected := `[{"event":"minute-watched","properties":{"broadcast_id":"99","channel_id":"42","channel":"streamer","hidden":false,"live":true,"location":"channel","logged_in":true,"muted":false,"player":"site","user_id":777}}]`
	if got != expected {
		t.Fatalf("watch payload 不匹配:\n got=%s\nwant=%s", got, expected)
	}
}

func TestBuildWatchBodyRejectsMissingBroadcast(t *testing.T) {
	t.Parallel()

	channel := &domain.Channel{ID: 42, Login: "streamer", Stream: &domain.Stream{}}
	if _, err := BuildWatchBody(channel, 777); err == nil {
		t.Fatal("缺少 broadcast_id 时应返回错误")
	}
}

func TestSendWatchReturnsTrueOnStatusCode204(t *testing.T) {
	t.Parallel()

	spade := &fakeSpadeClient{status: 204}
	tracker := newTestTracker(t, testTrackerOptions{
		spadeClient: spade,
		authState:   &fakeAuthState{snapshot: auth.Snapshot{UserID: 999, SessionID: "session", DeviceID: "device"}},
	})
	tracker.AddChannel(domain.Channel{
		ID:    88,
		Login: "watcher",
		Stream: &domain.Stream{
			BroadcastID: 1234,
			Game:        &domain.Game{ID: 5, Name: "Rust"},
		},
	})

	ok, err := tracker.SendWatch(context.Background(), 88)
	if err != nil {
		t.Fatalf("SendWatch 返回错误: %v", err)
	}
	if !ok {
		t.Fatal("statusCode 204 应判定为 watch 成功")
	}

	req := spade.lastRequest()
	if req.Method != http.MethodPost || req.URL != spadeTrackURL {
		t.Fatalf("watch 应 POST 到 spade: %s %s", req.Method, req.URL)
	}
	if ct := req.Headers.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		t.Fatalf("Content-Type 不匹配: %q", ct)
	}
	got := decodeWatchBody(t, req.Body)
	for _, want := range []string{`"broadcast_id":"1234"`, `"channel_id":"88"`, `"user_id":999`, `"location":"channel"`, `"player":"site"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("payload 缺少 %s: %s", want, got)
		}
	}
}

func TestSendWatchReturnsFalseOnNonSuccessStatusCode(t *testing.T) {
	t.Parallel()

	tracker := newTestTracker(t, testTrackerOptions{
		spadeClient: &fakeSpadeClient{status: 500},
	})
	tracker.AddChannel(domain.Channel{
		ID:     88,
		Login:  "watcher",
		Stream: &domain.Stream{BroadcastID: 1234},
	})

	ok, err := tracker.SendWatch(context.Background(), 88)
	if err != nil {
		t.Fatalf("SendWatch 返回错误: %v", err)
	}
	if ok {
		t.Fatal("非 204 状态码应判定为 watch 失败")
	}
}

func TestSendWatchSetsContentTypeOnClonedHeaders(t *testing.T) {
	t.Parallel()

	shared := http.Header{}
	spade := &fakeSpadeClient{status: 204}
	tracker := newTestTracker(t, testTrackerOptions{spadeClient: spade})
	tracker.watchHeaders = func(context.Context) (http.Header, error) { return shared, nil }
	tracker.AddChannel(domain.Channel{ID: 88, Login: "watcher", Stream: &domain.Stream{BroadcastID: 1234}})

	if _, err := tracker.SendWatch(context.Background(), 88); err != nil {
		t.Fatalf("SendWatch 返回错误: %v", err)
	}
	if shared.Get("Content-Type") != "" {
		t.Fatal("不应污染共享的 headers, 应在副本上设置 Content-Type")
	}
}
