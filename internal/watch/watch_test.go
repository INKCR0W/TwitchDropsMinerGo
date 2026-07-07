package watch

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func decodeWatchData(t *testing.T, query gql.RawQuery) string {
	t.Helper()

	input, ok := query.Variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("variables.input 类型不匹配: %#v", query.Variables)
	}
	if input["repository"] != "twilight" || input["encoding"] != "GZIP_B64" {
		t.Fatalf("input 元数据不匹配: %#v", input)
	}
	data, ok := input["data"].(string)
	if !ok || data == "" {
		t.Fatalf("input.data 缺失: %#v", input)
	}

	compressed, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		t.Fatalf("base64 解码失败: %v", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip 解压失败: %v", err)
	}
	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("读取解压内容失败: %v", err)
	}
	return string(decompressed)
}

func TestBuildWatchQueryEncodesMinuteWatchedPayload(t *testing.T) {
	t.Parallel()

	channel := &domain.Channel{
		ID:    42,
		Login: "streamer",
		Stream: &domain.Stream{
			BroadcastID: 99,
			Game:        &domain.Game{ID: 7, Name: "Apex Legends"},
		},
	}
	now := time.Date(2026, 7, 7, 14, 44, 52, 835*int(time.Millisecond), time.UTC)

	query, err := BuildWatchQuery(channel, 777, now)
	if err != nil {
		t.Fatalf("BuildWatchQuery 返回错误: %v", err)
	}

	if !strings.Contains(query.Query, "sendSpadeEvents") {
		t.Fatalf("mutation 不含 sendSpadeEvents: %q", query.Query)
	}

	got := decodeWatchData(t, query)
	expected := `[{"event":"minute-watched","properties":{"broadcast_id":"99","channel_id":"42","channel":"streamer","client_time":"2026-07-07T14:44:52.835Z","game":"Apex Legends","game_id":"7","hidden":false,"is_live":true,"live":true,"logged_in":true,"minutes_logged":1,"muted":false,"user_id":777}}]`
	if got != expected {
		t.Fatalf("watch payload 不匹配:\n got=%s\nwant=%s", got, expected)
	}
}

func TestBuildWatchQueryOmitsGameWhenAbsent(t *testing.T) {
	t.Parallel()

	channel := &domain.Channel{
		ID:     42,
		Login:  "streamer",
		Stream: &domain.Stream{BroadcastID: 99},
	}
	now := time.Date(2026, 7, 7, 14, 44, 52, 0, time.UTC)

	query, err := BuildWatchQuery(channel, 777, now)
	if err != nil {
		t.Fatalf("BuildWatchQuery 返回错误: %v", err)
	}

	got := decodeWatchData(t, query)
	if !strings.Contains(got, `"game":"","game_id":""`) {
		t.Fatalf("无 game 时应留空: %s", got)
	}
}

func TestBuildWatchQueryDoesNotHTMLEscapeGameName(t *testing.T) {
	t.Parallel()

	channel := &domain.Channel{
		ID:    42,
		Login: "streamer",
		Stream: &domain.Stream{
			BroadcastID: 99,
			Game:        &domain.Game{ID: 7, Name: "Dead by Daylight & Friends"},
		},
	}
	now := time.Date(2026, 7, 7, 14, 44, 52, 0, time.UTC)

	query, err := BuildWatchQuery(channel, 777, now)
	if err != nil {
		t.Fatalf("BuildWatchQuery 返回错误: %v", err)
	}

	got := decodeWatchData(t, query)
	if !strings.Contains(got, `"game":"Dead by Daylight & Friends"`) {
		t.Fatalf("game 名中的 & 不应被 HTML 转义: %s", got)
	}
}

func TestSendWatchReturnsTrueOnStatusCode204(t *testing.T) {
	t.Parallel()

	var sent gql.RawQuery
	tracker := newTestTracker(t, testTrackerOptions{
		gqlClient: &fakeGQLClient{
			doRawFunc: func(_ context.Context, query gql.RawQuery) (gql.Response, error) {
				sent = query
				return gql.Response{Data: map[string]any{
					"sendSpadeEvents": map[string]any{"statusCode": float64(204)},
				}}, nil
			},
		},
		authState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 999, SessionID: "session", DeviceID: "device"}},
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

	got := decodeWatchData(t, sent)
	if !strings.Contains(got, `"broadcast_id":"1234"`) {
		t.Fatalf("payload 缺少 broadcast_id: %s", got)
	}
	if !strings.Contains(got, `"channel_id":"88"`) {
		t.Fatalf("payload 缺少 channel_id: %s", got)
	}
	if !strings.Contains(got, `"user_id":999`) {
		t.Fatalf("payload 缺少 user_id: %s", got)
	}
}

func TestSendWatchReturnsFalseOnNonSuccessStatusCode(t *testing.T) {
	t.Parallel()

	tracker := newTestTracker(t, testTrackerOptions{
		gqlClient: &fakeGQLClient{
			doRawFunc: func(context.Context, gql.RawQuery) (gql.Response, error) {
				return gql.Response{Data: map[string]any{
					"sendSpadeEvents": map[string]any{"statusCode": float64(500)},
				}}, nil
			},
		},
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
