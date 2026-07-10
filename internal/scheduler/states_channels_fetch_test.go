package scheduler

import (
	"context"
	"slices"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

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
