package scheduler

import (
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
)

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

func TestHandleChannelSwitchLogsNoWatchableAndGoesIdle(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "ACL Game"}

	var logBuf logBuffer
	logger := logBuf.logger()

	scheduler := newTestScheduler(t, testSchedulerOptions{
		logger: logger,
	})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-acl", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)
	// All channels are offline — no Stream set.
	scheduler.channels = map[int64]domain.Channel{
		10: {ID: 10, Login: "offline-a", ACLBased: true},
		20: {ID: 20, Login: "offline-b", ACLBased: true},
	}

	scheduler.handleChannelSwitch()

	if scheduler.State() != StateIdle {
		t.Fatalf("无可观看频道时应进入 IDLE: %s", scheduler.State())
	}
	if got := scheduler.WatchingChannelID(); got != 0 {
		t.Fatalf("无可观看频道时不应有 watching channel: %d", got)
	}
	if !logBuf.contains("当前没有可观看的频道") {
		t.Fatal("应输出无可观看频道的诊断日志")
	}
}

func TestHandleChannelSwitchLeavesInvalidatedWatchingChannel(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)
	scheduler.channels = map[int64]domain.Channel{
		10: {
			ID:    10,
			Login: "invalidated",
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &game,
				DropsEnabled: false,
			},
		},
		20: {
			ID:    20,
			Login: "healthy",
			Stream: &domain.Stream{
				BroadcastID:  200,
				Game:         &game,
				DropsEnabled: true,
			},
		},
	}
	scheduler.watchingChannelID = 10

	if scheduler.canWatch(scheduler.channels[10]) {
		t.Fatal("前置条件不成立: 频道 10 应已不可观看")
	}
	if !scheduler.canWatch(scheduler.channels[20]) {
		t.Fatal("前置条件不成立: 频道 20 应可观看")
	}

	scheduler.handleChannelSwitch()

	if got := scheduler.WatchingChannelID(); got != 20 {
		t.Fatalf("当前频道失效且存在同优先级健康频道时应切换过去, got=%d", got)
	}
	if scheduler.State() == StateIdle {
		t.Fatal("存在可观看频道时不应进入 IDLE")
	}
}

func TestHandleChannelSwitchIdlesWhenInvalidatedChannelHasNoReplacement(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)
	scheduler.channels = map[int64]domain.Channel{
		10: {
			ID:    10,
			Login: "invalidated",
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &game,
				DropsEnabled: false,
			},
		},
	}
	scheduler.watchingChannelID = 10

	scheduler.handleChannelSwitch()

	if scheduler.State() != StateIdle {
		t.Fatalf("当前频道失效且无可替代频道时应进入 IDLE: %s", scheduler.State())
	}
}
