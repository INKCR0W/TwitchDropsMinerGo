package domain

import (
	"testing"
	"time"
)

func TestCampaignCanEarnWithinAndSpecialEvents(t *testing.T) {
	t.Parallel()

	now := testTime()
	normalCampaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-future",
		Name:     "future",
		Game:     Game{ID: 30, Name: "Game"},
		Linked:   true,
		Status:   "UPCOMING",
		StartsAt: now.Add(30 * time.Minute),
		EndsAt:   now.Add(4 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "drop-future",
				Name:            "drop-future",
				StartsAt:        now.Add(30 * time.Minute),
				EndsAt:          now.Add(4 * time.Hour),
				RequiredMinutes: 20,
				Benefits: []Benefit{
					{ID: "benefit-future", Name: "Future", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	if !normalCampaign.CanEarnWithin(now, now.Add(time.Hour), false) {
		t.Fatal("未来一小时内开始的活动应被视为可在窗口内推进")
	}
	if normalCampaign.CanEarnWithin(now, now.Add(20*time.Minute), false) {
		t.Fatal("窗口早于活动开始时间时不应视为可推进")
	}

	specialCampaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-special",
		Name:     "special",
		Game:     Game{ID: SpecialEventsGameID, Name: "Special Events"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "drop-special",
				Name:            "drop-special",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(time.Hour),
				RequiredMinutes: 10,
				Benefits: []Benefit{
					{ID: "benefit-special", Name: "Special", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	channel := &Channel{
		ID:    301,
		Login: "other-game",
		Stream: &Stream{
			BroadcastID:  401,
			Game:         &Game{ID: 999, Name: "Something Else"},
			DropsEnabled: true,
		},
	}

	if !specialCampaign.CanEarn(now, channel, false, false) {
		t.Fatal("Special Events 活动应允许任意游戏频道推进")
	}
}

func TestCampaignCanEarnOnAnyChannelForIRLGame(t *testing.T) {
	t.Parallel()

	now := testTime()
	irlCampaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-irl",
		Name:     "irl",
		Game:     Game{ID: IRLGameID, Name: "IRL"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "drop-irl",
				Name:            "drop-irl",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(time.Hour),
				RequiredMinutes: 10,
				Benefits: []Benefit{
					{ID: "benefit-irl", Name: "IRL", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	channel := &Channel{
		ID:    302,
		Login: "other-game",
		Stream: &Stream{
			BroadcastID:  402,
			Game:         &Game{ID: 999, Name: "Something Else"},
			DropsEnabled: true,
		},
	}

	if !irlCampaign.CanEarn(now, channel, false, false) {
		t.Fatal("IRL 活动应允许任意游戏频道推进")
	}
}

func TestRewardCampaignBumpMinutesStopsAtRequiredMinutes(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := Game{ID: 60, Name: "Reward Game"}
	campaign := mustCampaign(t, CampaignSpec{
		ID:               "reward:campaign",
		Name:             "reward",
		Game:             game,
		Linked:           true,
		Status:           "ACTIVE",
		IsRewardCampaign: true,
		StartsAt:         now.Add(-time.Hour),
		EndsAt:           now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "reward:drop",
				Name:            "reward-drop",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(time.Hour),
				RequiredMinutes: 5,
				Benefits: []Benefit{
					{ID: "reward-benefit", Name: "Reward", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})
	drop := campaign.Drop("reward:drop")
	channel := &Channel{
		ID:    600,
		Login: "reward-channel",
		Stream: &Stream{
			BroadcastID:  700,
			Game:         &game,
			DropsEnabled: false,
		},
	}

	for i := 0; i < 4; i++ {
		if completed := campaign.BumpRewardMinutes(now, channel, false, false); completed {
			t.Fatalf("第 %d 次补分钟不应完成", i+1)
		}
	}
	if completed := campaign.BumpRewardMinutes(now, channel, false, false); !completed {
		t.Fatal("第 5 次补分钟应达到 reward 所需分钟")
	}
	if drop.CurrentMinutes() != 5 {
		t.Fatalf("reward 本地分钟不匹配: current=%d", drop.CurrentMinutes())
	}
	drop.MarkClaimed()
	if completed := campaign.BumpRewardMinutes(now, channel, false, false); completed {
		t.Fatal("本地已完成并标记 claimed 后不应继续被视为可赚取")
	}
	if drop.CurrentMinutes() != 5 {
		t.Fatalf("reward 本地分钟不应超过所需分钟: current=%d", drop.CurrentMinutes())
	}
}
