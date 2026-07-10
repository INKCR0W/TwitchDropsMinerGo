package scheduler

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
)

func TestComputeWantedGamesHonorsPriorityExcludeAndWindow(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameA := domain.Game{ID: 1, Name: "Apex Legends"}
	gameB := domain.Game{ID: 2, Name: "Rust"}
	gameIgnored := domain.Game{ID: 3, Name: "Ignored"}
	gameLater := domain.Game{ID: 4, Name: "Later"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.EndingSoonest,
			Priority:     []string{gameA.Name},
			Exclude:      []string{gameIgnored.Name},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-a", gameA, now.Add(-time.Hour), now.Add(4*time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-b", gameB, now.Add(-time.Hour), now.Add(2*time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-ignored", gameIgnored, now.Add(-time.Hour), now.Add(2*time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-later", gameLater, now.Add(2*time.Hour), now.Add(4*time.Hour), nil)),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameA, gameB}
	if !slices.Equal(got, want) {
		t.Fatalf("wanted games 不匹配:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesPriorityOnlyStillHonorsPriorityList(t *testing.T) {
	t.Parallel()

	now := testTime()
	gamePriority := domain.Game{ID: 51, Name: "Priority"}
	gameOther := domain.Game{ID: 52, Name: "Other"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.PriorityOnly,
			Priority:     []string{gamePriority.Name},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-priority-only", gamePriority, now.Add(-time.Hour), now.Add(2*time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-other-priority-only", gameOther, now.Add(-time.Hour), now.Add(2*time.Hour), nil)),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gamePriority}
	if !slices.Equal(got, want) {
		t.Fatalf("priority_only 行为不应变化:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestLogWantedGamesUpdateReportsCurrentAndRemovedGames(t *testing.T) {
	t.Parallel()

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))

	scheduler := newTestScheduler(t, testSchedulerOptions{logger: logger})
	scheduler.logWantedGamesUpdate(
		[]domain.Game{{ID: 1, Name: "Rust"}, {ID: 2, Name: "Apex Legends"}},
		[]domain.Game{{ID: 2, Name: "Apex Legends"}},
	)

	output := logs.String()
	if !strings.Contains(output, "规划挂游戏列表已更新") {
		t.Fatalf("缺少规划游戏列表日志: %q", output)
	}
	if !strings.Contains(output, "wanted_games=\"Apex Legends\"") {
		t.Fatalf("规划游戏列表日志不匹配: %q", output)
	}
	if !strings.Contains(output, "游戏已移出规划列表") || !strings.Contains(output, "game=Rust") {
		t.Fatalf("缺少移出规划列表日志: %q", output)
	}
}
