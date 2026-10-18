package watch

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
)

func TestSendWatchRequestsAllChunksAndCachesPlaylistPerBroadcast(t *testing.T) {
	t.Parallel()
	var tokenCalls int
	var requests []string
	client := &fakeHTTPClient{callFunc: func(request httpclient.Request) (httpclient.Response, error) {
		requests = append(requests, request.Method+" "+strings.Split(request.URL, "?")[0])
		if request.Headers.Get("Authorization") != "" {
			t.Fatal("CDN 请求不应携带 OAuth")
		}
		switch {
		case strings.HasPrefix(request.URL, "https://usher.ttvnw.net/"):
			parsed, err := url.Parse(request.URL)
			if err != nil {
				return httpclient.Response{}, err
			}
			if parsed.Query().Get("token") != `{"token":"a+b&c"}` || parsed.Query().Get("sig") != "signature" {
				t.Fatalf("播放凭据未正确编码: %s", parsed.RawQuery)
			}
			return httpclient.Response{StatusCode: 200, Body: []byte("#EXTM3U\nhttps://cdn.example/high.m3u8\nhttps://cdn.example/low.m3u8\n")}, nil
		case request.URL == "https://cdn.example/low.m3u8":
			if request.Headers.Get("Connection") != "close" {
				t.Fatal("媒体播放列表应关闭连接")
			}
			return httpclient.Response{StatusCode: 200, Body: []byte("#EXTM3U\r\n#EXTINF:2,\r\none.ts\r\n#EXTINF:2,\r\nhttps://cdn.example/two.ts\r\n#EXT-X-ENDLIST\r\n")}, nil
		case strings.HasSuffix(request.URL, ".ts"):
			if request.Method != http.MethodHead {
				t.Fatal("视频分片只应发送 HEAD")
			}
			return httpclient.Response{StatusCode: 200}, nil
		default:
			return httpclient.Response{}, fmt.Errorf("意外请求: %s", request.URL)
		}
	}}
	tracker := newTestTracker(t, testTrackerOptions{
		httpClient: client,
		gqlClient: &fakeGQLClient{doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			tokenCalls++
			if operation.OperationName != "PlaybackAccessToken" || operation.Variables["login"] != "watcher" {
				t.Fatalf("播放凭据请求错误: %#v", operation)
			}
			return gql.Response{Data: map[string]any{"streamPlaybackAccessToken": map[string]any{
				"value": `{"token":"a+b&c"}`, "signature": "signature",
			}}}, nil
		}},
	})
	channel := domain.Channel{ID: 88, Login: "watcher", Stream: &domain.Stream{BroadcastID: 1234}}
	tracker.AddChannel(channel)
	for range 2 {
		if ok, err := tracker.SendWatch(context.Background(), 88); err != nil || !ok {
			t.Fatalf("SendWatch = %v, %v", ok, err)
		}
	}
	if tokenCalls != 1 {
		t.Fatalf("同一直播应复用播放地址，凭据请求次数: %d", tokenCalls)
	}
	want := []string{
		"GET https://usher.ttvnw.net/api/channel/hls/watcher.m3u8",
		"GET https://cdn.example/low.m3u8", "HEAD https://cdn.example/one.ts", "HEAD https://cdn.example/two.ts",
		"GET https://cdn.example/low.m3u8", "HEAD https://cdn.example/one.ts", "HEAD https://cdn.example/two.ts",
	}
	if !slices.Equal(requests, want) {
		t.Fatalf("观看请求顺序错误: %#v", requests)
	}
	channel.Stream.BroadcastID++
	tracker.AddChannel(channel)
	if ok, err := tracker.SendWatch(context.Background(), 88); err != nil || !ok {
		t.Fatalf("新直播 SendWatch = %v, %v", ok, err)
	}
	if tokenCalls != 2 {
		t.Fatalf("新直播应重新获取播放凭据，次数: %d", tokenCalls)
	}
}

func TestSendWatchRejectsInvalidPlaylistAndRenewsFailedPlayback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		body          string
		status        int
		headStatus    int
		getError      error
		headError     error
		cancelRequest bool
	}{
		{name: "expired", status: 403},
		{name: "offline", status: 404},
		{name: "json error", status: 200, body: `[{"error":"access denied"}]`},
		{name: "empty playlist", status: 200, body: "#EXTM3U\n#EXT-X-ENDLIST"},
		{name: "invalid scheme", status: 200, body: "#EXTM3U\nfile:///chunk.ts"},
		{name: "failed chunk", status: 200, body: "#EXTM3U\nchunk.ts", headStatus: 404},
		{name: "playlist retry exhausted", getError: fmt.Errorf("HTTP 请求达到最大重试次数 5: 状态码 503")},
		{name: "chunk retry exhausted", status: 200, body: "#EXTM3U\nchunk.ts", headError: fmt.Errorf("HTTP 请求达到最大重试次数 5: 状态码 503")},
		{name: "caller cancellation", cancelRequest: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failed := true
			tokenCalls := 0
			tracker := newTestTracker(t, testTrackerOptions{
				gqlClient: &fakeGQLClient{doFunc: func(context.Context, gql.Operation) (gql.Response, error) {
					tokenCalls++
					return gql.Response{Data: map[string]any{"streamPlaybackAccessToken": map[string]any{"value": "token", "signature": "sig"}}}, nil
				}},
				httpClient: &fakeHTTPClient{callFunc: func(request httpclient.Request) (httpclient.Response, error) {
					if strings.HasPrefix(request.URL, "https://usher.ttvnw.net/") {
						return httpclient.Response{StatusCode: 200, Body: []byte("#EXTM3U\nhttps://cdn.example/live.m3u8")}, nil
					}
					if failed && tc.cancelRequest {
						cancel()
						return httpclient.Response{}, ctx.Err()
					}
					if request.Method == http.MethodHead {
						if failed && tc.headError != nil {
							return httpclient.Response{}, tc.headError
						}
						status := 200
						if failed {
							status = tc.headStatus
						}
						return httpclient.Response{StatusCode: status}, nil
					}
					if failed {
						if tc.getError != nil {
							return httpclient.Response{}, tc.getError
						}
						return httpclient.Response{StatusCode: tc.status, Body: []byte(tc.body)}, nil
					}
					return httpclient.Response{StatusCode: 200, Body: []byte("#EXTM3U\nchunk.ts")}, nil
				}},
			})
			tracker.AddChannel(domain.Channel{ID: 88, Login: "watcher", Stream: &domain.Stream{BroadcastID: 1234}})
			if ok, err := tracker.SendWatch(ctx, 88); ok || err == nil {
				t.Fatalf("无效播放响应不应报告成功: %v, %v", ok, err)
			}
			failed = false
			if ok, err := tracker.SendWatch(context.Background(), 88); !ok || err != nil {
				t.Fatalf("重新获取播放地址后应恢复: %v, %v", ok, err)
			}
			expectedTokenCalls := 2
			if tc.cancelRequest {
				expectedTokenCalls = 1
			}
			if tokenCalls != expectedTokenCalls {
				t.Fatalf("播放凭据请求次数不匹配: got=%d want=%d", tokenCalls, expectedTokenCalls)
			}
		})
	}
}
