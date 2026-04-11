package watch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/inventory"
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

func TestSyncChannelUpdatesDisplayNameAndDropsEnabled(t *testing.T) {
	t.Parallel()

	fakeGQL := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			switch operation.OperationName {
			case "VideoPlayerStreamInfoOverlayChannel":
				return gql.Response{
					Data: map[string]any{
						"user": map[string]any{
							"id":          "100",
							"displayName": "Streamer",
							"stream": map[string]any{
								"id":           "321",
								"viewersCount": 123,
							},
							"broadcastSettings": map[string]any{
								"title": "Live now",
								"game": map[string]any{
									"id":          "7",
									"displayName": "Game",
									"slug":        "game",
								},
							},
						},
					},
				}, nil
			case "DropsHighlightService_AvailableDrops":
				return gql.Response{
					Data: map[string]any{
						"channel": map[string]any{
							"viewerDropCampaigns": []any{
								map[string]any{"id": "campaign-1"},
							},
						},
					},
				}, nil
			default:
				t.Fatalf("收到意外 GQL 操作: %s", operation.OperationName)
				return gql.Response{}, nil
			}
		},
	}

	tracker := newTestTracker(t, testTrackerOptions{
		gqlClient: fakeGQL,
		settings: config.Settings{
			AvailableDropsCheck: true,
		},
		inventory: inventory.Snapshot{
			Campaigns: map[string]*domain.DropsCampaign{
				"campaign-1": mustCampaign(t, domain.CampaignSpec{
					ID:       "campaign-1",
					Name:     "Campaign",
					Game:     domain.Game{ID: 7, Name: "Game"},
					Linked:   true,
					Status:   "ACTIVE",
					StartsAt: testNow().Add(-time.Hour),
					EndsAt:   testNow().Add(time.Hour),
					Drops: []domain.TimedDropSpec{
						{
							ID:              "drop-1",
							Name:            "Drop",
							StartsAt:        testNow().Add(-time.Hour),
							EndsAt:          testNow().Add(time.Hour),
							RequiredMinutes: 15,
							Benefits: []domain.Benefit{
								{ID: "benefit-1", Name: "Reward", Type: domain.BenefitTypeDirectEntitlement},
							},
						},
					},
				}),
			},
		},
	})

	tracker.AddChannel(domain.Channel{ID: 100, Login: "streamer"})

	online, err := tracker.SyncChannel(context.Background(), 100)
	if err != nil {
		t.Fatalf("SyncChannel 返回错误: %v", err)
	}
	if !online {
		t.Fatal("在线频道应返回 online=true")
	}

	channel, ok := tracker.Channel(100)
	if !ok {
		t.Fatal("同步后应能读取频道状态")
	}
	if channel.DisplayName != "Streamer" {
		t.Fatalf("DisplayName 不匹配: %q", channel.DisplayName)
	}
	if channel.Stream == nil {
		t.Fatal("同步后应有 stream")
	}
	if !channel.Stream.DropsEnabled {
		t.Fatal("AvailableDrops 命中本地 campaign 时应判定为可掉宝")
	}
	if channel.Stream.Viewers != 123 {
		t.Fatalf("viewer 数不匹配: %d", channel.Stream.Viewers)
	}
}

func TestSyncChannelsUsesBatchAndHandlesOfflineChannels(t *testing.T) {
	t.Parallel()

	var batches [][]string
	fakeGQL := &fakeGQLClient{
		doBatchFunc: func(ctx context.Context, operations []gql.Operation) ([]gql.Response, error) {
			names := make([]string, 0, len(operations))
			for _, operation := range operations {
				names = append(names, operation.OperationName)
			}
			batches = append(batches, names)

			switch operations[0].OperationName {
			case "VideoPlayerStreamInfoOverlayChannel":
				return []gql.Response{
					{
						Data: map[string]any{
							"user": map[string]any{
								"id":          "1",
								"displayName": "One",
								"stream": map[string]any{
									"id":           "11",
									"viewersCount": 20,
								},
								"broadcastSettings": map[string]any{
									"title": "First",
									"game": map[string]any{
										"id":          "7",
										"displayName": "Game",
									},
								},
							},
						},
					},
					{
						Data: map[string]any{
							"user": map[string]any{
								"id":          "2",
								"displayName": "Two",
								"stream":      nil,
								"broadcastSettings": map[string]any{
									"title": "Offline",
									"game":  nil,
								},
							},
						},
					},
				}, nil
			case "DropsHighlightService_AvailableDrops":
				return []gql.Response{
					{
						Data: map[string]any{
							"channel": map[string]any{
								"viewerDropCampaigns": []any{
									map[string]any{"id": "campaign-1"},
								},
							},
						},
					},
				}, nil
			default:
				t.Fatalf("收到意外批量操作: %s", operations[0].OperationName)
				return nil, nil
			}
		},
	}

	tracker := newTestTracker(t, testTrackerOptions{
		gqlClient: fakeGQL,
		settings: config.Settings{
			AvailableDropsCheck: true,
		},
		inventory: inventory.Snapshot{
			Campaigns: map[string]*domain.DropsCampaign{
				"campaign-1": mustCampaign(t, domain.CampaignSpec{
					ID:       "campaign-1",
					Name:     "Campaign",
					Game:     domain.Game{ID: 7, Name: "Game"},
					Linked:   true,
					Status:   "ACTIVE",
					StartsAt: testNow().Add(-time.Hour),
					EndsAt:   testNow().Add(time.Hour),
					Drops: []domain.TimedDropSpec{
						{
							ID:              "drop-1",
							Name:            "Drop",
							StartsAt:        testNow().Add(-time.Hour),
							EndsAt:          testNow().Add(time.Hour),
							RequiredMinutes: 15,
							Benefits: []domain.Benefit{
								{ID: "benefit-1", Name: "Reward", Type: domain.BenefitTypeDirectEntitlement},
							},
						},
					},
				}),
			},
		},
	})

	tracker.AddChannel(domain.Channel{ID: 1, Login: "one"})
	tracker.AddChannel(domain.Channel{ID: 2, Login: "two"})

	if err := tracker.SyncChannels(context.Background(), 1, 2); err != nil {
		t.Fatalf("SyncChannels 返回错误: %v", err)
	}

	if len(batches) != 2 {
		t.Fatalf("批量调用次数不匹配: %#v", batches)
	}
	if !slices.Equal(batches[0], []string{"VideoPlayerStreamInfoOverlayChannel", "VideoPlayerStreamInfoOverlayChannel"}) {
		t.Fatalf("首批 GetStreamInfo 不匹配: %#v", batches[0])
	}
	if !slices.Equal(batches[1], []string{"DropsHighlightService_AvailableDrops"}) {
		t.Fatalf("次批 AvailableDrops 不匹配: %#v", batches[1])
	}

	first, ok := tracker.Channel(1)
	if !ok || first.Stream == nil || !first.Stream.DropsEnabled {
		t.Fatalf("频道 1 应在线且可掉宝: %#v", first)
	}

	second, ok := tracker.Channel(2)
	if !ok {
		t.Fatal("频道 2 应存在")
	}
	if second.Stream != nil {
		t.Fatalf("离线频道不应保留 stream: %#v", second.Stream)
	}
}

func TestProcessStreamEventsHonorOnlineDelayAndStatusTransitions(t *testing.T) {
	t.Parallel()

	slept := make(chan time.Duration, 1)
	synced := make(chan struct{}, 1)
	fakeGQL := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			select {
			case synced <- struct{}{}:
			default:
			}
			return gql.Response{
				Data: map[string]any{
					"user": map[string]any{
						"id":          "55",
						"displayName": "Delayed",
						"stream": map[string]any{
							"id":           "555",
							"viewersCount": 10,
						},
						"broadcastSettings": map[string]any{
							"title": "After delay",
							"game": map[string]any{
								"id":          "9",
								"displayName": "Game",
							},
						},
					},
				},
			}, nil
		},
	}

	tracker := newTestTracker(t, testTrackerOptions{
		gqlClient: fakeGQL,
		sleep: func(ctx context.Context, delay time.Duration) error {
			slept <- delay
			return nil
		},
		onlineDelay: 7 * time.Second,
	})

	tracker.AddChannel(domain.Channel{ID: 55, Login: "delayed"})

	if err := tracker.ProcessStreamState(context.Background(), 55, json.RawMessage(`{"type":"stream-up"}`)); err != nil {
		t.Fatalf("ProcessStreamState 返回错误: %v", err)
	}

	select {
	case delay := <-slept:
		if delay != 7*time.Second {
			t.Fatalf("ONLINE_DELAY 不匹配: %v", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("未触发延迟检查")
	}

	select {
	case <-synced:
	case <-time.After(time.Second):
		t.Fatal("延迟检查后未触发同步")
	}

	channel, ok := tracker.Channel(55)
	if !ok || channel.Stream == nil {
		t.Fatalf("延迟同步后频道应在线: %#v", channel)
	}
	if channel.PendingOnline() {
		t.Fatal("延迟同步完成后不应保留 pending 状态")
	}

	if err := tracker.ProcessStreamState(context.Background(), 55, json.RawMessage(`{"type":"viewcount","viewers":77}`)); err != nil {
		t.Fatalf("处理 viewcount 返回错误: %v", err)
	}
	channel, _ = tracker.Channel(55)
	if channel.Stream == nil || channel.Stream.Viewers != 77 {
		t.Fatalf("viewcount 应更新 viewer 数: %#v", channel.Stream)
	}

	if err := tracker.ProcessStreamUpdate(context.Background(), 55, json.RawMessage(`{"type":"broadcast_settings_update"}`)); err != nil {
		t.Fatalf("处理 stream update 返回错误: %v", err)
	}
	select {
	case <-synced:
	case <-time.After(time.Second):
		t.Fatal("stream update 应重新触发延迟同步")
	}

	if err := tracker.ProcessStreamState(context.Background(), 55, json.RawMessage(`{"type":"stream-down"}`)); err != nil {
		t.Fatalf("处理 stream-down 返回错误: %v", err)
	}
	channel, _ = tracker.Channel(55)
	if channel.Stream != nil || !channel.Offline() {
		t.Fatalf("stream-down 后频道应离线: %#v", channel)
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

type testTrackerOptions struct {
	gqlClient   GQLClient
	httpClient  HTTPClient
	authState   AuthState
	settings    config.Settings
	inventory   inventory.Snapshot
	sleep       func(context.Context, time.Duration) error
	onlineDelay time.Duration
}

func newTestTracker(t *testing.T, options testTrackerOptions) *Tracker {
	t.Helper()

	gqlClient := options.gqlClient
	if gqlClient == nil {
		gqlClient = &fakeGQLClient{
			doFunc: func(context.Context, gql.Operation) (gql.Response, error) {
				return gql.Response{}, nil
			},
			doBatchFunc: func(context.Context, []gql.Operation) ([]gql.Response, error) {
				return nil, nil
			},
		}
	}

	httpClient := options.httpClient
	if httpClient == nil {
		httpClient = &fakeHTTPClient{
			doFunc: func(context.Context, httpclient.Request) (httpclient.Response, error) {
				return httpclient.Response{}, nil
			},
		}
	}

	authState := options.authState
	if authState == nil {
		authState = &fakeAuthState{
			snapshot: auth.Snapshot{
				UserID:    1,
				SessionID: "session",
				DeviceID:  "device",
			},
		}
	}

	tracker, err := NewTracker(Options{
		GQLClient:   gqlClient,
		HTTPClient:  httpClient,
		AuthState:   authState,
		ClientInfo:  httpclient.AndroidAppClient,
		OnlineDelay: options.onlineDelay,
		Clock:       testNow,
		Sleep:       options.sleep,
	})
	if err != nil {
		t.Fatalf("NewTracker 返回错误: %v", err)
	}
	t.Cleanup(func() {
		if err := tracker.Close(context.Background()); err != nil {
			t.Fatalf("Close 返回错误: %v", err)
		}
	})

	tracker.Configure(options.settings, options.inventory)
	return tracker
}

type fakeGQLClient struct {
	doFunc      func(context.Context, gql.Operation) (gql.Response, error)
	doBatchFunc func(context.Context, []gql.Operation) ([]gql.Response, error)
}

func (f *fakeGQLClient) Do(ctx context.Context, operation gql.Operation) (gql.Response, error) {
	if f.doFunc == nil {
		return gql.Response{}, nil
	}
	return f.doFunc(ctx, operation)
}

func (f *fakeGQLClient) DoBatch(ctx context.Context, operations []gql.Operation) ([]gql.Response, error) {
	if f.doBatchFunc == nil {
		return nil, nil
	}
	return f.doBatchFunc(ctx, operations)
}

type fakeHTTPClient struct {
	doFunc func(context.Context, httpclient.Request) (httpclient.Response, error)
}

func (f *fakeHTTPClient) Do(ctx context.Context, request httpclient.Request) (httpclient.Response, error) {
	if f.doFunc == nil {
		return httpclient.Response{}, nil
	}
	return f.doFunc(ctx, request)
}

type fakeAuthState struct {
	mu       sync.Mutex
	snapshot auth.Snapshot
}

func (f *fakeAuthState) Validate(context.Context) error {
	return nil
}

func (f *fakeAuthState) Snapshot() auth.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeAuthState) Headers(options auth.HeadersOptions) http.Header {
	headers := make(http.Header)
	if options.UserAgent != "" {
		headers.Set("User-Agent", options.UserAgent)
	}
	headers.Set("Client-Id", httpclient.AndroidAppClient.ClientID)

	snapshot := f.Snapshot()
	if snapshot.SessionID != "" {
		headers.Set("Client-Session-Id", snapshot.SessionID)
	}
	if snapshot.DeviceID != "" {
		headers.Set("X-Device-Id", snapshot.DeviceID)
	}
	if options.GQL && snapshot.AccessToken != "" {
		headers.Set("Authorization", "OAuth "+snapshot.AccessToken)
	}
	return headers
}

func mustCampaign(t *testing.T, spec domain.CampaignSpec) *domain.DropsCampaign {
	t.Helper()

	campaign, err := domain.NewCampaign(spec)
	if err != nil {
		t.Fatalf("NewCampaign 返回错误: %v", err)
	}
	return campaign
}

func testNow() time.Time {
	return time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)
}
