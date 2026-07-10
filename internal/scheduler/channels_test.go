package scheduler

import (
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
)

func TestCanWatchStillRequiresDropsEnabledForNormalCampaign(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}
	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-normal", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)

	channel := domain.Channel{
		ID:    10,
		Login: "normal-channel",
		Stream: &domain.Stream{
			BroadcastID:  100,
			Game:         &game,
			DropsEnabled: false,
		},
	}
	if scheduler.canWatch(channel) {
		t.Fatal("普通 campaign 仍应要求频道具备 DROPS_ENABLED")
	}
}

func TestCanWatchAllowsSpecialEventsWithoutDropsEnabled(t *testing.T) {
	t.Parallel()

	now := testTime()
	otherGame := domain.Game{ID: 999, Name: "Other"}
	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.wantedGames = []domain.Game{{ID: domain.SpecialEventsGameID, Name: "Special Events"}}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, domain.CampaignSpec{
			ID:       "campaign-special",
			Name:     "Special",
			Game:     domain.Game{ID: domain.SpecialEventsGameID, Name: "Special Events"},
			Linked:   true,
			Status:   "ACTIVE",
			StartsAt: now.Add(-time.Hour),
			EndsAt:   now.Add(time.Hour),
			Drops: []domain.TimedDropSpec{
				{
					ID:              "drop-special",
					Name:            "Special Drop",
					StartsAt:        now.Add(-time.Hour),
					EndsAt:          now.Add(time.Hour),
					RequiredMinutes: 10,
					Benefits: []domain.Benefit{
						{ID: "benefit-special", Name: "Special Reward", Type: domain.BenefitTypeDirectEntitlement},
					},
				},
			},
		}),
	)

	channel := domain.Channel{
		ID:    10,
		Login: "special-channel",
		Stream: &domain.Stream{
			BroadcastID:  100,
			Game:         &otherGame,
			DropsEnabled: false,
		},
	}
	if !scheduler.canWatch(channel) {
		t.Fatal("Special Events campaign 应继续允许任意游戏且不依赖 DROPS_ENABLED")
	}
}
