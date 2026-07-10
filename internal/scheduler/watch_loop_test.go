package scheduler

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
)

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
