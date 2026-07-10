package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

var errAssert = errors.New("assert an error")

func TestWatchLogsChannelAndGameOnSwitch(t *testing.T) {
	t.Parallel()

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	game := domain.Game{ID: 1, Name: "Rust"}

	scheduler := newTestScheduler(t, testSchedulerOptions{logger: logger})
	scheduler.channels = map[int64]domain.Channel{
		7: {
			ID:          7,
			Login:       "rustlive",
			DisplayName: "Rust Live",
			ACLBased:    true,
			Stream: &domain.Stream{
				Game:         &game,
				Viewers:      321,
				DropsEnabled: true,
			},
		},
	}

	scheduler.watch(7)

	output := logs.String()
	if !strings.Contains(output, "切换观看频道") {
		t.Fatalf("缺少切台日志: %q", output)
	}
	if !strings.Contains(output, "channel_login=rustlive") || !strings.Contains(output, "game=Rust") {
		t.Fatalf("切台日志缺少频道或游戏信息: %q", output)
	}
}

func TestWatchLoopSendsWatchAndBumpsMinutesOnFallback(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-watch", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	drop := campaign.Drop("campaign-watch-drop")

	tracker := newFakeTracker()
	tracker.applyChannel(domain.Channel{
		ID:    99,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  999,
			Game:         &game,
			DropsEnabled: true,
		},
	})

	scheduler := newTestScheduler(t, testSchedulerOptions{
		tracker:       tracker,
		watchInterval: 2 * time.Millisecond,
		progressDelay: time.Millisecond,
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	scheduler.channels = tracker.snapshot()
	scheduler.watchingChannelID = 99

	ctx, cancel := context.WithCancel(context.Background())
	scheduler.wg.Add(1)
	go scheduler.watchLoop(ctx)

	time.Sleep(25 * time.Millisecond)
	cancel()
	scheduler.wg.Wait()

	if tracker.sendWatchCalls() == 0 {
		t.Fatal("watch loop 应调用 SendWatch")
	}
	if drop.ExtraCurrentMinutes == 0 {
		t.Fatal("CurrentDrop 缺失时应回退到本地补分钟")
	}
}

func TestWatchLoopSkipsFallbackWhenProgressUpdatedAfterSend(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-watch-no-fallback", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	drop := campaign.Drop("campaign-watch-no-fallback-drop")

	tracker := newFakeTracker()
	tracker.applyChannel(domain.Channel{
		ID:    99,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  999,
			Game:         &game,
			DropsEnabled: true,
		},
	})

	scheduler := newTestScheduler(t, testSchedulerOptions{
		tracker:       tracker,
		watchInterval: 2 * time.Millisecond,
		progressDelay: time.Millisecond,
	})
	tracker.sendWatchFunc = func(context.Context, int64) (bool, error) {
		scheduler.recordProgress(scheduler.nowUTC())
		return true, nil
	}

	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	scheduler.channels = tracker.snapshot()
	scheduler.watchingChannelID = 99

	ctx, cancel := context.WithCancel(context.Background())
	scheduler.wg.Add(1)
	go scheduler.watchLoop(ctx)

	time.Sleep(25 * time.Millisecond)
	cancel()
	scheduler.wg.Wait()

	if tracker.sendWatchCalls() == 0 {
		t.Fatal("watch loop 应调用 SendWatch")
	}
	if drop.ExtraCurrentMinutes != 0 {
		t.Fatalf("当前轮已收到进度更新时不应补本地分钟: %d", drop.ExtraCurrentMinutes)
	}
}

func TestResolveProgressRequestsSwitchWhenExtraMinutesHitLimit(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-limit", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	drop := campaign.Drop("campaign-limit-drop")
	drop.ExtraCurrentMinutes = domain.MaxExtraMinutes - 1

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() != StateChannelSwitch {
		t.Fatalf("达到 extra minutes 上限后应请求切台: %s", scheduler.State())
	}
}

func TestResolveProgressRefreshesInventoryWhenChannelHasNothingLeftToEarn(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpecWithDrop(
		"campaign-full",
		game,
		now.Add(-time.Hour),
		now.Add(time.Hour),
		nil,
		domain.TimedDropSpec{RequiredMinutes: 30, RealCurrentMinutes: 30},
	))

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("观看时长已满但尚未认领时应刷新 inventory 而不是空转: %s", scheduler.State())
	}
}

func TestResolveProgressKeepsWatchingWhileCampaignStillEarnable(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpecWithDrop(
		"campaign-partial",
		game,
		now.Add(-time.Hour),
		now.Add(time.Hour),
		nil,
		domain.TimedDropSpec{RequiredMinutes: 30, RealCurrentMinutes: 10},
	))

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() != StateIdle {
		t.Fatalf("活动仍可推进时不应触发状态切换: %s", scheduler.State())
	}
}

func TestResolveProgressCompletesRewardCampaignWithoutCurrentDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 27471, Name: "Minecraft"}
	campaign := mustCampaign(t, domain.CampaignSpec{
		ID:               "reward:builder-cape",
		Name:             "Builder Cape",
		Game:             game,
		Linked:           true,
		Status:           "ACTIVE",
		IsRewardCampaign: true,
		StartsAt:         now.Add(-time.Hour),
		EndsAt:           now.Add(time.Hour),
		LinkURL:          "https://www.minecraft.net/redeem",
		Drops: []domain.TimedDropSpec{
			{
				ID:                  "reward:builder-cape-drop",
				Name:                "Builder Cape",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(time.Hour),
				RequiredMinutes:     5,
				ExtraCurrentMinutes: 4,
				Benefits: []domain.Benefit{
					{ID: "builder-cape-benefit", Name: "Builder Cape", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})
	progressStore := &fakeRewardProgressStore{}
	refresher := &fakeRefresher{}
	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher:      refresher,
		rewardProgress: progressStore,
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"dropCurrentSession": nil,
						},
					},
				}, nil
			},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "minecraft-channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: false,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("reward campaign 完成后应刷新 inventory: %s", scheduler.State())
	}
	record, ok := progressStore.lastRecord()
	if !ok {
		t.Fatal("reward campaign 完成后应写入本地完成状态")
	}
	if record.CampaignID != "reward:builder-cape" || record.DropID != "reward:builder-cape-drop" || record.MinutesWatched != 5 || record.CompletedAt.IsZero() {
		t.Fatalf("reward 完成记录不匹配: %#v", record)
	}
	if !record.ExpiresAt.Equal(campaign.EndsAt) {
		t.Fatalf("reward 完成记录应包含活动过期时间: %#v", record)
	}
	if drop := campaign.Drop("reward:builder-cape-drop"); drop == nil || !drop.IsClaimed {
		t.Fatalf("完成后的 reward drop 应在内存快照中标记为 claimed: %#v", drop)
	}
	if refresher.updateCallCount != 1 {
		t.Fatalf("reward 完成后应同步进度给 refresher: %d", refresher.updateCallCount)
	}
	if _, ok := refresher.rewardProgress["reward:builder-cape"]; !ok {
		t.Fatalf("refresher 未收到 reward 完成快照: %#v", refresher.rewardProgress)
	}
}

func TestResolveProgressRetriesRewardCompletionWhenPersistFails(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 27471, Name: "Minecraft"}
	campaign := mustCampaign(t, domain.CampaignSpec{
		ID:               "reward:builder-cape",
		Name:             "Builder Cape",
		Game:             game,
		Linked:           true,
		Status:           "ACTIVE",
		IsRewardCampaign: true,
		StartsAt:         now.Add(-time.Hour),
		EndsAt:           now.Add(time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:                  "reward:builder-cape-drop",
				Name:                "Builder Cape",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(time.Hour),
				RequiredMinutes:     domain.MaxExtraMinutes,
				ExtraCurrentMinutes: domain.MaxExtraMinutes - 1,
				Benefits: []domain.Benefit{
					{ID: "builder-cape-benefit", Name: "Builder Cape", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})
	progressStore := &fakeRewardProgressStore{err: errAssert}
	scheduler := newTestScheduler(t, testSchedulerOptions{
		rewardProgress: progressStore,
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"dropCurrentSession": nil,
						},
					},
				}, nil
			},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "minecraft-channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: false,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() == StateInventoryFetch {
		t.Fatal("reward 完成状态保存失败时不应刷新 inventory 丢失本地进度")
	}
	if drop := campaign.Drop("reward:builder-cape-drop"); drop == nil || drop.IsClaimed {
		t.Fatalf("保存失败时不应把 reward drop 标记为 claimed: %#v", drop)
	}
	if _, ok := progressStore.lastRecord(); ok {
		t.Fatal("保存失败时不应记录成功完成态")
	}

	progressStore.err = nil
	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("保存恢复后应刷新 inventory: %s", scheduler.State())
	}
	if record, ok := progressStore.lastRecord(); !ok || record.CampaignID != "reward:builder-cape" || record.MinutesWatched != domain.MaxExtraMinutes {
		t.Fatalf("保存恢复后应写入完成态: %#v ok=%v", record, ok)
	}
}

func currentDropGQLClient(dropID string, currentMinutes int) *fakeGQLClient {
	return &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName != "DropCurrentSessionContext" {
				return gql.Response{}, nil
			}
			return gql.Response{
				Data: map[string]any{
					"currentUser": map[string]any{
						"dropCurrentSession": map[string]any{
							"dropID":                dropID,
							"currentMinutesWatched": float64(currentMinutes),
						},
					},
				},
			}, nil
		},
	}
}

func TestResolveProgressPrefersAuthoritativeGQLProgress(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-gql", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	drop := campaign.Drop("campaign-gql-drop")
	drop.ExtraCurrentMinutes = 5

	scheduler := newTestScheduler(t, testSchedulerOptions{
		gqlClient: currentDropGQLClient("campaign-gql-drop", 12),
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)

	if drop.RealCurrentMinutes != 12 {
		t.Fatalf("应采用 Twitch 返回的权威分钟数, got=%d", drop.RealCurrentMinutes)
	}
	if drop.ExtraCurrentMinutes != 0 {
		t.Fatalf("同步权威进度后应清零本地估算, got=%d", drop.ExtraCurrentMinutes)
	}
}

func TestResolveProgressRecomputesGamesAfterFullSpecialEventRewardGroup(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{Name: "Special Events"}
	campaign := mustCampaign(t, domain.CampaignSpec{
		ID:       "campaign-ewc",
		Name:     "EWC 2026",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(24 * time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:              "bronze",
				Name:            "EWC Bronze",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 60,
				Benefits: []domain.Benefit{
					{ID: "bronze-benefit", Name: "Bronze", Type: domain.BenefitTypeBadge},
				},
			},
			{
				ID:              "diamond",
				Name:            "EWC 2026 (Diamond) Reward Group",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 720,
				Benefits: []domain.Benefit{
					{ID: "diamond-benefit", Name: "Diamond", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	scheduler := newTestScheduler(t, testSchedulerOptions{
		gqlClient: currentDropGQLClient("diamond", 720),
	})
	scheduler.settings.EnableBadgesEmotes = true
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "special-events-channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)

	if scheduler.State() != StateGamesUpdate {
		t.Fatalf("Special Events 满进度本地收口后应重算游戏列表而不是刷新 inventory: %s", scheduler.State())
	}
	for _, dropID := range []string{"bronze", "diamond"} {
		if drop := campaign.Drop(dropID); drop == nil || !drop.IsClaimed {
			t.Fatalf("满进度 reward group 应收口同窗口 Special Events 里程碑: %s %#v", dropID, drop)
		}
	}
}

func TestResolveProgressSkipsLocalEstimateWhenWatchNotReported(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-no-estimate", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	drop := campaign.Drop("campaign-no-estimate-drop")

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, false)

	if drop.ExtraCurrentMinutes != 0 {
		t.Fatalf("watch 未成功送达时不应本地补分钟, got=%d", drop.ExtraCurrentMinutes)
	}
}
