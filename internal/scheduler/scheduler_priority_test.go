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

func TestComputeWantedGamesSmartBalancePrefersUrgentCampaignOverManualPriority(t *testing.T) {
	t.Parallel()

	now := testTime()
	gamePriority := domain.Game{ID: 11, Name: "Priority Game"}
	gameUrgent := domain.Game{ID: 12, Name: "Urgent Game"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.SmartBalance,
			Priority:     []string{gamePriority.Name},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-priority", gamePriority, now.Add(-time.Hour), now.Add(12*time.Hour), nil)),
		mustCampaign(t, campaignSpecWithDrop("campaign-urgent", gameUrgent, now.Add(-time.Hour), now.Add(90*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 60,
		})),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameUrgent, gamePriority}
	if !slices.Equal(got, want) {
		t.Fatalf("smart_balance 应优先选择紧急活动:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalanceFiltersCertainlyUnfinishableCampaigns(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameImpossible := domain.Game{ID: 21, Name: "Impossible"}
	gamePossible := domain.Game{ID: 22, Name: "Possible"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.SmartBalance,
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpecWithDrop("campaign-impossible", gameImpossible, now.Add(-time.Hour), now.Add(20*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 30,
		})),
		mustCampaign(t, campaignSpecWithDrop("campaign-possible", gamePossible, now.Add(-time.Hour), now.Add(2*time.Hour), nil, domain.TimedDropSpec{
			RequiredMinutes: 30,
		})),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gamePossible}
	if !slices.Equal(got, want) {
		t.Fatalf("smart_balance 应排除确认来不及完成的活动:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalancePrefersActiveCampaignOverUpcoming(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameActive := domain.Game{ID: 31, Name: "Active"}
	gameUpcoming := domain.Game{ID: 32, Name: "Upcoming"}

	// Neither game is in the priority list, so both sit in the same tier and the
	// within-tier ordering (active before upcoming) decides. Cross-tier priority
	// precedence is covered by the priority-first tests above.
	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.SmartBalance,
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-active", gameActive, now.Add(-time.Hour), now.Add(4*time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-upcoming", gameUpcoming, now.Add(30*time.Minute), now.Add(4*time.Hour), nil)),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameActive, gameUpcoming}
	if !slices.Equal(got, want) {
		t.Fatalf("smart_balance 同层内应优先当前可刷的活动:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalanceUsesBestCampaignPerGame(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameMulti := domain.Game{ID: 41, Name: "Multi Campaign"}
	gameOther := domain.Game{ID: 42, Name: "Other Campaign"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.SmartBalance,
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpecWithDrop("campaign-multi-relaxed", gameMulti, now.Add(-time.Hour), now.Add(10*time.Hour), nil, domain.TimedDropSpec{
			RequiredMinutes: 120,
		})),
		mustCampaign(t, campaignSpecWithDrop("campaign-other", gameOther, now.Add(-time.Hour), now.Add(3*time.Hour), nil, domain.TimedDropSpec{
			RequiredMinutes: 40,
		})),
		mustCampaign(t, campaignSpecWithDrop("campaign-multi-urgent", gameMulti, now.Add(-time.Hour), now.Add(25*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes:    30,
			RealCurrentMinutes: 20,
		})),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameMulti, gameOther}
	if !slices.Equal(got, want) {
		t.Fatalf("smart_balance 应按同游戏最佳活动排序:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalanceKeepsGamesWithAFinishableDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameSalvageable := domain.Game{ID: 61, Name: "Salvageable"}

	spec := domain.CampaignSpec{
		ID:       "campaign-salvageable",
		Name:     "campaign-salvageable",
		Game:     gameSalvageable,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(3 * time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:              "drop-unfinishable",
				Name:            "drop-unfinishable",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(40 * time.Minute),
				RequiredMinutes: 300,
				Benefits: []domain.Benefit{
					{ID: "benefit-long", Name: "long-reward", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
			{
				ID:              "drop-finishable",
				Name:            "drop-finishable",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(3 * time.Hour),
				RequiredMinutes: 20,
				Benefits: []domain.Benefit{
					{ID: "benefit-short", Name: "short-reward", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{PriorityMode: config.SmartBalance},
	})
	scheduler.snapshot = snapshotFromCampaigns(mustCampaign(t, spec))

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameSalvageable}
	if !slices.Equal(got, want) {
		t.Fatalf("smart_balance 应保留仍有可完成 drop 的游戏:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalancePrioritizesPriorityGamesWhenAllRelaxed(t *testing.T) {
	t.Parallel()

	now := testTime()
	gamePriority := domain.Game{ID: 71, Name: "Priority Relaxed"}
	gameRareNonPriority := domain.Game{ID: 72, Name: "Rare NonPriority"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.SmartBalance,
			Priority:     []string{gamePriority.Name},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		// priority: availability = 1200/30 ≈ 40 (risk 2, relaxed)
		mustCampaign(t, campaignSpec(now, "campaign-priority-relaxed", gamePriority, now.Add(-time.Hour), now.Add(20*time.Hour), nil)),
		// non-priority but rarer: availability = 300/30 = 10 (risk 2, still relaxed, lower availability than priority)
		mustCampaign(t, campaignSpec(now, "campaign-rare-nonpriority", gameRareNonPriority, now.Add(-time.Hour), now.Add(5*time.Hour), nil)),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gamePriority, gameRareNonPriority}
	if !slices.Equal(got, want) {
		t.Fatalf("smart_balance 应让 priority 游戏排在非 priority 之前:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalanceProtectsPriorityWhenPriorityAlsoAtRisk(t *testing.T) {
	t.Parallel()

	now := testTime()
	gamePriorityAtRisk := domain.Game{ID: 81, Name: "Priority AtRisk"}
	gameNonPriorityMoreUrgent := domain.Game{ID: 82, Name: "NonPriority MoreUrgent"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.SmartBalance,
			Priority:     []string{gamePriorityAtRisk.Name},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		// priority also at risk: availability = 90/60 = 1.5 (risk 0)
		mustCampaign(t, campaignSpecWithDrop("campaign-priority-atrisk", gamePriorityAtRisk, now.Add(-time.Hour), now.Add(90*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 60,
		})),
		// non-priority even more urgent: availability = 72/60 = 1.2 (risk 0)
		mustCampaign(t, campaignSpecWithDrop("campaign-nonpriority-urgent", gameNonPriorityMoreUrgent, now.Add(-time.Hour), now.Add(72*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 60,
		})),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gamePriorityAtRisk, gameNonPriorityMoreUrgent}
	if !slices.Equal(got, want) {
		t.Fatalf("某 priority 游戏也危险时不应让非 priority 插队:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalanceKeepsCampaignWithLateFinalDropBehindPrecondition(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameChained := domain.Game{ID: 91, Name: "Chained"}

	spec := domain.CampaignSpec{
		ID:       "campaign-chained",
		Name:     "campaign-chained",
		Game:     gameChained,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(10 * time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				// Benefit-less precondition, earnable across the whole campaign window.
				ID:              "drop-precondition",
				Name:            "drop-precondition",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(10 * time.Hour),
				RequiredMinutes: 60,
			},
			{
				// Final reward drop with a late, narrow window; its own 30 minutes fit
				// the window even though the precondition minutes do not.
				ID:                  "drop-final",
				Name:                "drop-final",
				StartsAt:            now.Add(4 * time.Hour),
				EndsAt:              now.Add(5 * time.Hour),
				RequiredMinutes:     30,
				PreconditionDropIDs: []string{"drop-precondition"},
				Benefits: []domain.Benefit{
					{ID: "benefit-final", Name: "final-reward", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{PriorityMode: config.SmartBalance},
	})
	scheduler.snapshot = snapshotFromCampaigns(mustCampaign(t, spec))

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameChained}
	if !slices.Equal(got, want) {
		t.Fatalf("smart_balance 不应丢弃前置+迟到 final drop 但仍可完成的活动:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalanceDoesNotPromoteUpcomingAtRiskOverActive(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameActiveRelaxed := domain.Game{ID: 93, Name: "Active Relaxed"}
	gameUpcomingAtRisk := domain.Game{ID: 94, Name: "Upcoming AtRisk"}

	// No priority list: tiering must not lift a not-yet-watchable upcoming game
	// above a currently watchable active one.
	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{PriorityMode: config.SmartBalance},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-active-relaxed", gameActiveRelaxed, now.Add(-time.Hour), now.Add(4*time.Hour), nil)),
		// upcoming (starts in 10m) and at risk (availability = 36/20 = 1.8), but finishable
		mustCampaign(t, campaignSpecWithDrop("campaign-upcoming-atrisk", gameUpcomingAtRisk, now.Add(10*time.Minute), now.Add(36*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 20,
		})),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameActiveRelaxed, gameUpcomingAtRisk}
	if !slices.Equal(got, want) {
		t.Fatalf("smart_balance 不应把尚不可刷的 upcoming at-risk 活动排到当前可刷活动之前:\n got=%#v\nwant=%#v", got, want)
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
