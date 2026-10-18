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

func TestShouldResolveProgressToleratesProgressAnywhereInWatchCycle(t *testing.T) {
	t.Parallel()

	now := testTime()
	scheduler := newTestScheduler(t, testSchedulerOptions{})

	scheduler.lastProgressAt = now.Add(-20 * time.Second)
	if scheduler.shouldResolveProgress() {
		t.Fatal("观看周期结束但未满一分钟时不应查询进度")
	}

	// 观看请求每 20 秒发送一次，本地补分钟不能随之加速
	scheduler.lastProgressAt = now.Add(-time.Minute)
	if !scheduler.shouldResolveProgress() {
		t.Fatal("距上次进度满一分钟时应走 GQL 兜底")
	}

	scheduler.lastProgressAt = time.Time{}
	if !scheduler.shouldResolveProgress() {
		t.Fatal("从未收到过进度时应走 GQL 兜底")
	}
}

func TestProgressFallbackPreservesMinuteRemainder(t *testing.T) {
	t.Parallel()
	for _, reward := range []bool{false, true} {
		name := "timed drop"
		if reward {
			name = "reward campaign"
		}
		t.Run(name, func(t *testing.T) {
			start := testTime()
			now := start.Add(15200 * time.Millisecond)
			game := domain.Game{ID: 1, Name: "Watched"}
			spec := campaignSpec(start, "campaign-jitter", game, start.Add(-time.Hour), start.Add(time.Hour), nil)
			spec.IsRewardCampaign = reward
			campaign := mustCampaign(t, spec)
			scheduler := newTestScheduler(t, testSchedulerOptions{now: func() time.Time { return now }})
			scheduler.snapshot = snapshotFromCampaigns(campaign)
			scheduler.wantedGames = []domain.Game{game}
			scheduler.lastProgressAt = now
			channel := domain.Channel{ID: 1, Login: "channel", Stream: &domain.Stream{Game: &game, DropsEnabled: true}}

			for _, step := range []struct {
				elapsed time.Duration
				minutes int
			}{
				{34900 * time.Millisecond, 0},
				{54900 * time.Millisecond, 0},
				{74900 * time.Millisecond, 0},
				{94900 * time.Millisecond, 1},
				{114800 * time.Millisecond, 1},
				{134800 * time.Millisecond, 1},
				{154800 * time.Millisecond, 2},
				{174700 * time.Millisecond, 2},
				{194700 * time.Millisecond, 2},
				{214700 * time.Millisecond, 3},
				// 长时间没有成功上报时，不追补中断期间的整分钟
				{625200 * time.Millisecond, 4},
				{645200 * time.Millisecond, 4},
			} {
				now = start.Add(step.elapsed)
				if scheduler.shouldResolveProgress() {
					scheduler.resolveProgress(context.Background(), channel, true)
				}
				if got := campaign.Drop("campaign-jitter-drop").ExtraCurrentMinutes; got != step.minutes {
					t.Fatalf("经过 %s 后分钟数不匹配: got=%d want=%d", step.elapsed, got, step.minutes)
				}
			}
		})
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
