package scheduler

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
)

func TestCloneInventorySnapshotCreatesIndependentCampaignGraph(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-clone", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	original := snapshotFromCampaigns(campaign)

	cloned, err := cloneInventorySnapshot(original)
	if err != nil {
		t.Fatalf("cloneInventorySnapshot 返回错误: %v", err)
	}

	originalDrop := original.Drops["campaign-clone-drop"]
	clonedDrop := cloned.Drops["campaign-clone-drop"]
	if originalDrop == nil || clonedDrop == nil {
		t.Fatalf("clone 后 drop 丢失: original=%#v cloned=%#v", originalDrop, clonedDrop)
	}
	if originalDrop == clonedDrop {
		t.Fatal("clone 后的 drop 不应与原对象共享指针")
	}

	originalDrop.ExtraCurrentMinutes = 5
	if clonedDrop.ExtraCurrentMinutes != 0 {
		t.Fatalf("clone 后的 drop 不应跟随原对象变化: %d", clonedDrop.ExtraCurrentMinutes)
	}
}

func TestHandleInventoryFetchAddsUserTopics(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Campaign"}
	refresher := &fakeRefresher{
		refreshFunc: func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error) {
			return snapshotFromCampaigns(
				mustCampaign(t, campaignSpec(now, "campaign-user-topics", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
			), nil
		},
	}
	pubsubManager := &fakePubSub{}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher: refresher,
		pubsub:    pubsubManager,
		authState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 42}},
	})

	if err := scheduler.handleInventoryFetch(context.Background()); err != nil {
		t.Fatalf("handleInventoryFetch 返回错误: %v", err)
	}

	added := pubsubManager.addedKeys()
	for _, expected := range []string{"onsite-notifications.42", "user-drop-events.42"} {
		if !slices.Contains(added, expected) {
			t.Fatalf("缺少用户 topic %q, got=%#v", expected, added)
		}
	}
}

func TestHandleGamesUpdateContinuesAfterClaimSweepTimeout(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Timeout Game"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-timeout", game, now.Add(-time.Hour), now.Add(2*time.Hour), nil))
	drop := campaign.Drop("campaign-timeout-drop")
	if drop == nil {
		t.Fatal("期望测试 campaign 包含 drop")
	}
	drop.UpdateClaim(drop.GenerateClaimID(42))

	scheduler := newTestScheduler(t, testSchedulerOptions{
		authState:         &fakeAuthState{snapshot: auth.Snapshot{UserID: 42}},
		claimSweepTimeout: 5 * time.Millisecond,
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				if operation.OperationName != "DropsPage_ClaimDropRewards" {
					return gql.Response{}, nil
				}
				<-ctx.Done()
				return gql.Response{}, ctx.Err()
			},
		},
	})
	scheduler.state = StateGamesUpdate
	scheduler.snapshot = snapshotFromCampaigns(campaign)

	if err := scheduler.handleGamesUpdate(context.Background()); err != nil {
		t.Fatalf("handleGamesUpdate 返回错误: %v", err)
	}
	if scheduler.State() != StateChannelsCleanup {
		t.Fatalf("认领超时后仍应进入 CHANNELS_CLEANUP: %s", scheduler.State())
	}
}

func TestMaintenanceLoopRequestsCleanupThenReload(t *testing.T) {
	t.Parallel()

	current := testTime()
	sleepCalls := 0
	var scheduler *Scheduler
	scheduler = newTestScheduler(t, testSchedulerOptions{
		now: func() time.Time {
			return current
		},
		sleep: func(ctx context.Context, delay time.Duration) error {
			sleepCalls++
			current = current.Add(delay)
			if sleepCalls == 2 && scheduler.State() != StateChannelsCleanup {
				return errors.New("cleanup 未按预期触发")
			}
			return nil
		},
		maintenanceReload: time.Hour,
	})

	scheduler.wg.Add(1)
	scheduler.maintenanceLoop(context.Background(), []time.Time{current.Add(10 * time.Minute)})

	if sleepCalls != 2 {
		t.Fatalf("maintenance sleep 次数不匹配: %d", sleepCalls)
	}
	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("maintenance 结束后应请求 inventory reload: %s", scheduler.State())
	}
}

func TestStatusSnapshotIncludesSchedulerAndPubSubState(t *testing.T) {
	t.Parallel()

	game := domain.Game{ID: 1, Name: "Watched"}
	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.selectedChannelID = 10
	scheduler.watchingChannelID = 20
	scheduler.userTopicUserID = 42
	scheduler.lastProgressAt = testTime()
	scheduler.channels = map[int64]domain.Channel{
		20: {
			ID:    20,
			Login: "watching",
			Stream: &domain.Stream{
				BroadcastID:  200,
				Game:         &game,
				DropsEnabled: true,
			},
		},
	}

	status := scheduler.StatusSnapshot()
	if status.State != StateChannelSwitch {
		t.Fatalf("State 不匹配: %s", status.State)
	}
	if status.AuthenticatedUserID != 1 || status.UserTopicUserID != 42 {
		t.Fatalf("认证状态快照不匹配: %#v", status)
	}
	if status.PubSub.TopicCount != 0 {
		t.Fatalf("PubSub 状态不匹配: %#v", status.PubSub)
	}
	if len(status.Channels) != 1 || status.Channels[0].ID != 20 {
		t.Fatalf("频道快照不匹配: %#v", status.Channels)
	}
}

func TestUpdateSettingsReconfiguresTrackerAndRequestsReload(t *testing.T) {
	t.Parallel()

	tracker := newFakeTracker()
	scheduler := newTestScheduler(t, testSchedulerOptions{
		tracker: tracker,
		settings: config.Settings{
			Priority: []string{"A"},
		},
	})
	scheduler.state = StateIdle

	updated := config.DefaultSettings()
	updated.AvailableDropsCheck = true
	updated.Priority = []string{"B"}

	if err := scheduler.UpdateSettings(updated); err != nil {
		t.Fatalf("UpdateSettings 返回错误: %v", err)
	}

	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("UpdateSettings 应触发 inventory reload: %s", scheduler.State())
	}
	if !tracker.configuredSettings.AvailableDropsCheck {
		t.Fatalf("Tracker 未收到最新配置: %#v", tracker.configuredSettings)
	}
	if tracker.configuredSettings.Priority[0] != "B" {
		t.Fatalf("Tracker Priority 配置不匹配: %#v", tracker.configuredSettings)
	}
}
