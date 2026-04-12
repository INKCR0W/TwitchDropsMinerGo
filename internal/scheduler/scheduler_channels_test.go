package scheduler

import (
	"context"
	"slices"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestHandleChannelsCleanupRemovesOfflineAndUnwantedNonACLChannels(t *testing.T) {
	t.Parallel()

	now := testTime()
	wanted := domain.Game{ID: 1, Name: "Wanted"}
	other := domain.Game{ID: 2, Name: "Other"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelsCleanup
	scheduler.wantedGames = []domain.Game{wanted}
	scheduler.channels = map[int64]domain.Channel{
		1: {ID: 1, Login: "offline"},
		2: {
			ID:    2,
			Login: "other",
			Stream: &domain.Stream{
				BroadcastID:  22,
				Game:         &other,
				DropsEnabled: true,
			},
		},
		3: {
			ID:       3,
			Login:    "acl",
			ACLBased: true,
			Stream: &domain.Stream{
				BroadcastID:  33,
				Game:         &other,
				DropsEnabled: true,
			},
		},
		4: {
			ID:    4,
			Login: "wanted",
			Stream: &domain.Stream{
				BroadcastID:  44,
				Game:         &wanted,
				DropsEnabled: true,
			},
		},
	}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-wanted", wanted, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)

	scheduler.handleChannelsCleanup()

	if _, ok := scheduler.channels[1]; ok {
		t.Fatal("离线非 ACL 频道应被清理")
	}
	if _, ok := scheduler.channels[2]; ok {
		t.Fatal("不再想看的非 ACL 频道应被清理")
	}
	if _, ok := scheduler.channels[3]; !ok {
		t.Fatal("ACL 频道不应在增量 cleanup 中被移除")
	}
	if _, ok := scheduler.channels[4]; !ok {
		t.Fatal("仍然可看的频道不应被移除")
	}
	if scheduler.State() != StateChannelsFetch {
		t.Fatalf("cleanup 后应进入 CHANNELS_FETCH: %s", scheduler.State())
	}
}

func TestHandleChannelsFetchAddsACLDirectoryAndTopics(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameACL := domain.Game{ID: 10, Name: "ACL Game"}
	gameDir := domain.Game{ID: 20, Name: "Directory Game", SlugText: "directory-game"}

	tracker := newFakeTracker()
	tracker.syncChannelsFunc = func(ctx context.Context, channelIDs []int64) error {
		tracker.applyChannel(domain.Channel{
			ID:          10,
			Login:       "acl-channel",
			DisplayName: "ACL Channel",
			ACLBased:    true,
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &gameACL,
				Viewers:      50,
				DropsEnabled: true,
			},
		})
		return nil
	}

	gqlClient := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName != "DirectoryPage_Game" {
				return gql.Response{}, nil
			}
			return gql.Response{
				Data: map[string]any{
					"game": map[string]any{
						"streams": map[string]any{
							"edges": []any{
								map[string]any{
									"node": map[string]any{
										"id":           "200",
										"viewersCount": 77,
										"title":        "Live",
										"game": map[string]any{
											"id":          "20",
											"displayName": gameDir.Name,
											"slug":        gameDir.Slug(),
										},
										"broadcaster": map[string]any{
											"id":          "20",
											"login":       "directory-channel",
											"displayName": "Directory Channel",
										},
									},
								},
							},
						},
					},
				},
			}, nil
		},
	}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		tracker:   tracker,
		gqlClient: gqlClient,
	})
	scheduler.state = StateChannelsFetch
	scheduler.wantedGames = []domain.Game{gameACL, gameDir}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-acl", gameACL, now.Add(-time.Hour), now.Add(time.Hour), []domain.Channel{
			{ID: 10, Login: "acl-channel", DisplayName: "ACL Channel", ACLBased: true},
		})),
		mustCampaign(t, campaignSpec(now, "campaign-dir", gameDir, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)

	if err := scheduler.handleChannelsFetch(context.Background()); err != nil {
		t.Fatalf("handleChannelsFetch 返回错误: %v", err)
	}

	if _, ok := scheduler.channels[10]; !ok {
		t.Fatal("ACL 频道应被加入调度列表")
	}
	if channel, ok := scheduler.channels[20]; !ok || channel.Stream == nil || channel.Stream.Viewers != 77 {
		t.Fatalf("目录频道应被加入调度列表: %#v", channel)
	}
	if scheduler.State() != StateChannelSwitch {
		t.Fatalf("fetch 后应进入 CHANNEL_SWITCH: %s", scheduler.State())
	}

	added := trackerPubSubKeys(scheduler)
	expected := []string{
		"broadcast-settings-update.10",
		"broadcast-settings-update.20",
		"video-playback-by-id.10",
		"video-playback-by-id.20",
	}
	for _, key := range expected {
		if !slices.Contains(added, key) {
			t.Fatalf("缺少订阅 topic: %s, got=%#v", key, added)
		}
	}
}

func TestHandleChannelSwitchHonorsSelectionAndPriority(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameA := domain.Game{ID: 1, Name: "A Game"}
	gameB := domain.Game{ID: 2, Name: "B Game"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{gameA, gameB}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-a", gameA, now.Add(-time.Hour), now.Add(time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-b", gameB, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)
	scheduler.channels = map[int64]domain.Channel{
		10: {
			ID:    10,
			Login: "b",
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &gameB,
				DropsEnabled: true,
			},
		},
		20: {
			ID:    20,
			Login: "a",
			Stream: &domain.Stream{
				BroadcastID:  200,
				Game:         &gameA,
				DropsEnabled: true,
			},
		},
	}

	scheduler.SelectChannel(10)
	if selected := scheduler.selectedChannel(); selected != 10 {
		t.Fatalf("selected channel 未写入: %d", selected)
	}
	if !scheduler.canWatch(scheduler.channels[10]) {
		t.Fatalf("手选频道应可观看: %#v", scheduler.channels[10])
	}
	scheduler.handleChannelSwitch()
	if got := scheduler.WatchingChannelID(); got != 10 {
		t.Fatalf("手选频道应优先被切入: %d", got)
	}

	scheduler.ClearSelectedChannel()
	scheduler.handleChannelSwitch()
	if got := scheduler.WatchingChannelID(); got != 20 {
		t.Fatalf("高优先级游戏频道应接管观看: %d", got)
	}
}
