package domain

import (
	"math"
	"slices"
	"testing"
	"time"
)

func TestCampaignAggregatesAndPreconditionsChain(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-agg",
		Name:     "agg",
		Game:     Game{ID: 20, Name: "Game"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(2 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:                 "drop-a",
				Name:               "drop-a",
				StartsAt:           now.Add(-time.Hour),
				EndsAt:             now.Add(2 * time.Hour),
				RequiredMinutes:    30,
				RealCurrentMinutes: 15,
				Benefits: []Benefit{
					{ID: "benefit-a", Name: "A", Type: BenefitTypeDirectEntitlement},
				},
			},
			{
				ID:                  "drop-b",
				Name:                "drop-b",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(2 * time.Hour),
				RequiredMinutes:     45,
				PreconditionDropIDs: []string{"drop-a"},
				Benefits: []Benefit{
					{ID: "benefit-b", Name: "B", Type: BenefitTypeDirectEntitlement},
				},
			},
			{
				ID:              "drop-c",
				Name:            "drop-c",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(2 * time.Hour),
				RequiredMinutes: 10,
				IsClaimed:       true,
				Benefits: []Benefit{
					{ID: "benefit-c", Name: "C", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	if claimed := campaign.ClaimedDrops(); claimed != 1 {
		t.Fatalf("已领取掉宝数不匹配: %d", claimed)
	}
	if remaining := campaign.RemainingDrops(); remaining != 2 {
		t.Fatalf("剩余掉宝数不匹配: %d", remaining)
	}
	if required := campaign.RequiredMinutes(); required != 75 {
		t.Fatalf("活动总所需分钟数不匹配: %d", required)
	}
	if remaining := campaign.RemainingMinutes(); remaining != 60 {
		t.Fatalf("活动总剩余分钟数不匹配: %d", remaining)
	}
	if progress := campaign.Progress(); math.Abs(progress-0.5) > 1e-9 {
		t.Fatalf("活动进度不匹配: %v", progress)
	}
	if availability := campaign.Availability(now); math.Abs(availability-2) > 1e-9 {
		t.Fatalf("活动 availability 不匹配: %v", availability)
	}

	chain := campaign.PreconditionsChain()
	if !slices.Equal(chain, []string{"drop-a"}) {
		t.Fatalf("前置链不匹配: %#v", chain)
	}
}

func TestCampaignTimeTriggersAndFirstEarnableDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := Game{ID: 40, Name: "Game"}
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-triggers",
		Name:     "triggers",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-90 * time.Minute),
		EndsAt:   now.Add(3 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:                 "drop-slower",
				Name:               "drop-slower",
				StartsAt:           now.Add(-90 * time.Minute),
				EndsAt:             now.Add(2 * time.Hour),
				RequiredMinutes:    60,
				RealCurrentMinutes: 10,
				Benefits: []Benefit{
					{ID: "benefit-slower", Name: "Slower", Type: BenefitTypeDirectEntitlement},
				},
			},
			{
				ID:                 "drop-faster",
				Name:               "drop-faster",
				StartsAt:           now.Add(-30 * time.Minute),
				EndsAt:             now.Add(3 * time.Hour),
				RequiredMinutes:    30,
				RealCurrentMinutes: 20,
				Benefits: []Benefit{
					{ID: "benefit-faster", Name: "Faster", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	channel := &Channel{
		ID:    401,
		Login: "channel",
		Stream: &Stream{
			BroadcastID:  501,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	firstDrop := campaign.FirstEarnableDrop(now, channel, false, false)
	if firstDrop == nil || firstDrop.ID != "drop-faster" {
		t.Fatalf("FirstEarnableDrop 选择错误: %#v", firstDrop)
	}

	expectedTriggers := []time.Time{
		now.Add(-90 * time.Minute),
		now.Add(-30 * time.Minute),
		now.Add(2 * time.Hour),
		now.Add(3 * time.Hour),
	}
	if triggers := campaign.TimeTriggers(); !slices.Equal(triggers, expectedTriggers) {
		t.Fatalf("time triggers 不匹配: %#v", triggers)
	}
}
