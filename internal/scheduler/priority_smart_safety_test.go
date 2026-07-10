package scheduler

import (
	"slices"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
)

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

func TestComputeWantedGamesSmartBalanceBlocksInsertionWhenPrioritySpareBelowSafety(t *testing.T) {
	t.Parallel()

	now := testTime()
	gamePriority := domain.Game{ID: 101, Name: "Priority Tight Spare"}
	gameUrgent := domain.Game{ID: 102, Name: "Urgent NonPriority"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.SmartBalance,
			Priority:     []string{gamePriority.Name},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-priority-tight", gamePriority, now.Add(-time.Hour), now.Add(100*time.Minute), nil)),
		mustCampaign(t, campaignSpecWithDrop("campaign-urgent", gameUrgent, now.Add(-time.Hour), now.Add(72*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 60,
		})),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gamePriority, gameUrgent}
	if !slices.Equal(got, want) {
		t.Fatalf("priority 富余时间不足 120 分钟时不应被非 priority 插队:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalanceRespectsConfiguredSafetyMargin(t *testing.T) {
	t.Parallel()

	now := testTime()
	gamePriority := domain.Game{ID: 111, Name: "Priority Tight Spare"}
	gameUrgent := domain.Game{ID: 112, Name: "Urgent NonPriority"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode:               config.SmartBalance,
			Priority:                   []string{gamePriority.Name},
			SmartPrioritySafetyMinutes: 15,
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-priority-tight", gamePriority, now.Add(-time.Hour), now.Add(100*time.Minute), nil)),
		mustCampaign(t, campaignSpecWithDrop("campaign-urgent", gameUrgent, now.Add(-time.Hour), now.Add(72*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 60,
		})),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameUrgent, gamePriority}
	if !slices.Equal(got, want) {
		t.Fatalf("安全余量调小后应允许更紧急的非 priority 插队:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestComputeWantedGamesSmartBalanceProtectsPriorityByTightestSiblingCampaign(t *testing.T) {
	t.Parallel()

	now := testTime()
	gamePriority := domain.Game{ID: 121, Name: "Priority MultiCampaign"}
	gameUrgent := domain.Game{ID: 122, Name: "Urgent NonPriority"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.SmartBalance,
			Priority:     []string{gamePriority.Name},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpecWithDrop("campaign-priority-tight", gamePriority, now.Add(-time.Hour), now.Add(25*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 10,
		})),
		mustCampaign(t, campaignSpecWithDrop("campaign-priority-relaxed", gamePriority, now.Add(-time.Hour), now.Add(10*time.Hour), nil, domain.TimedDropSpec{
			RequiredMinutes: 300,
		})),
		mustCampaign(t, campaignSpecWithDrop("campaign-urgent", gameUrgent, now.Add(-time.Hour), now.Add(72*time.Minute), nil, domain.TimedDropSpec{
			RequiredMinutes: 60,
		})),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gamePriority, gameUrgent}
	if !slices.Equal(got, want) {
		t.Fatalf("同一 priority 游戏存在更紧的活动时应触发保护,不应被非 priority 插队:\n got=%#v\nwant=%#v", got, want)
	}
}
