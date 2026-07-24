package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

var errAssert = errors.New("assert an error")

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

func TestResolveProgressLogsPolledDropProgress(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-gql", game, now.Add(-time.Hour), now.Add(time.Hour), nil))

	var logs logBuffer
	scheduler := newTestScheduler(t, testSchedulerOptions{
		logger:    logs.logger(),
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

	if !logs.contains("轮询到掉宝进度") {
		t.Fatalf("GQL 轮询取得权威进度后应输出进度日志, 实际: %s", logs.String())
	}
	if !logs.contains("current_minutes=12") {
		t.Fatalf("进度日志应含轮询到的分钟数 12, 实际: %s", logs.String())
	}
}

func TestResolveProgressStampsProgressBeforeGQLRoundTrip(t *testing.T) {
	t.Parallel()

	start := testTime()
	clock := start
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(start, "campaign-slow-gql", game, start.Add(-time.Hour), start.Add(time.Hour), nil))

	scheduler := newTestScheduler(t, testSchedulerOptions{
		now: func() time.Time { return clock },
		gqlClient: &fakeGQLClient{
			doFunc: func(context.Context, gql.Operation) (gql.Response, error) {
				clock = clock.Add(10 * time.Second)
				return gql.Response{}, errAssert
			},
		},
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

	if !scheduler.lastProgressAt.Equal(start) {
		t.Fatalf("进度戳应取本轮判定时刻, GQL 耗时不能计入: got=%s want=%s", scheduler.lastProgressAt, start)
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
