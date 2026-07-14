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
	"twitchdropsminergo/internal/rewards"
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
	} else {
		drop.UpdateClaim(drop.GenerateClaimID(42))
	}

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
	scheduler.lastRuntimeError = errors.New("模拟调度错误")
	scheduler.runtimeErrorSince = time.Date(2026, 7, 14, 11, 0, 0, 0, time.UTC)

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
	if status.LastError != "模拟调度错误" {
		t.Fatalf("LastError 不匹配: %q", status.LastError)
	}
	if !status.ErrorSince.Equal(time.Date(2026, 7, 14, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("ErrorSince 不匹配: %v", status.ErrorSince)
	}
}

func TestUpdateSettingsAppliesSettingsAndRequestsReload(t *testing.T) {
	t.Parallel()

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			Priority: []string{"A"},
		},
	})
	scheduler.state = StateIdle

	updated := config.DefaultSettings()
	updated.Priority = []string{"B"}

	if err := scheduler.UpdateSettings(updated); err != nil {
		t.Fatalf("UpdateSettings 返回错误: %v", err)
	}

	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("UpdateSettings 应触发 inventory reload: %s", scheduler.State())
	}
	if applied := scheduler.settingsCopy(); applied.Priority[0] != "B" {
		t.Fatalf("新配置未生效: %#v", applied)
	}
}

func TestSyncRewardProgressPrunesExpiredRecordsBeforeRefreshing(t *testing.T) {
	t.Parallel()

	now := testTime()
	expiredCampaignID := "reward:expired"
	freshCampaignID := "reward:fresh"
	progressStore := &fakeRewardProgressStore{
		progress: map[string]rewards.Progress{
			expiredCampaignID: {
				CampaignID:     expiredCampaignID,
				DropID:         "reward:expired-drop",
				MinutesWatched: 5,
				CompletedAt:    now.Add(-10 * 24 * time.Hour),
				ExpiresAt:      now.Add(-8 * 24 * time.Hour),
				UpdatedAt:      now.Add(-10 * 24 * time.Hour),
			},
			freshCampaignID: {
				CampaignID:     freshCampaignID,
				DropID:         "reward:fresh-drop",
				MinutesWatched: 5,
				CompletedAt:    now.Add(-time.Hour),
				ExpiresAt:      now.Add(-6 * 24 * time.Hour),
				UpdatedAt:      now.Add(-time.Hour),
			},
		},
	}
	refresher := &fakeRefresher{}
	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher:        refresher,
		rewardProgress:   progressStore,
		now:              func() time.Time { return now },
		rewardPruneGrace: 7 * 24 * time.Hour,
	})

	scheduler.syncRewardProgressToRefresher()

	if _, ok := progressStore.progress[expiredCampaignID]; ok {
		t.Fatalf("超过宽限期的 reward 完成记录应被清理: %#v", progressStore.progress)
	}
	if _, ok := progressStore.progress[freshCampaignID]; !ok {
		t.Fatalf("宽限期内 reward 完成记录不应被清理: %#v", progressStore.progress)
	}
	if _, ok := refresher.rewardProgress[expiredCampaignID]; ok {
		t.Fatalf("refresher 不应收到已清理记录: %#v", refresher.rewardProgress)
	}
	if _, ok := refresher.rewardProgress[freshCampaignID]; !ok {
		t.Fatalf("refresher 应收到保留记录: %#v", refresher.rewardProgress)
	}
}
