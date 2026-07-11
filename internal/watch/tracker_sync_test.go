package watch

import (
	"context"
	"slices"
	"strings"
	"testing"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestSyncChannelUpdatesDisplayNameAndOfferedCampaigns(t *testing.T) {
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

	tracker := newTestTracker(t, testTrackerOptions{gqlClient: fakeGQL})

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
	if !slices.Equal(channel.Stream.OfferedCampaignIDs, []string{"campaign-1"}) {
		t.Fatalf("应记录 Twitch 报告的可推进活动: %#v", channel.Stream.OfferedCampaignIDs)
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

	tracker := newTestTracker(t, testTrackerOptions{gqlClient: fakeGQL})

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
	if !ok || first.Stream == nil {
		t.Fatalf("频道 1 应在线: %#v", first)
	}
	if !slices.Equal(first.Stream.OfferedCampaignIDs, []string{"campaign-1"}) {
		t.Fatalf("频道 1 应记录可推进活动: %#v", first.Stream.OfferedCampaignIDs)
	}

	second, ok := tracker.Channel(2)
	if !ok {
		t.Fatal("频道 2 应存在")
	}
	if second.Stream != nil {
		t.Fatalf("离线频道不应保留 stream: %#v", second.Stream)
	}
}

func TestSyncChannelsRejectsExtraBatchResponses(t *testing.T) {
	t.Parallel()

	fakeGQL := &fakeGQLClient{
		doBatchFunc: func(ctx context.Context, operations []gql.Operation) ([]gql.Response, error) {
			return []gql.Response{
				{Data: map[string]any{"user": nil}},
				{Data: map[string]any{"user": nil}},
			}, nil
		},
	}

	tracker := newTestTracker(t, testTrackerOptions{gqlClient: fakeGQL})
	tracker.AddChannel(domain.Channel{ID: 1, Login: "one"})

	err := tracker.SyncChannels(context.Background(), 1)
	if err == nil {
		t.Fatal("额外 batch response 应返回错误")
	}
	if !strings.Contains(err.Error(), "响应数量不匹配") {
		t.Fatalf("错误信息不匹配: %v", err)
	}
}
