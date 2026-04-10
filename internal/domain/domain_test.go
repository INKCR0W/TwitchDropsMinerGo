package domain

import (
	"math"
	"slices"
	"testing"
	"time"
)

func TestGameSlugUsesExplicitValueOrGeneratedFallback(t *testing.T) {
	t.Parallel()

	withSlug := Game{ID: 1, Name: "Counter-Strike 2", SlugText: "counter-strike-2"}
	if slug := withSlug.Slug(); slug != "counter-strike-2" {
		t.Fatalf("显式 slug 不匹配: %q", slug)
	}

	withoutSlug := Game{ID: 2, Name: "Tom Clancy's Rainbow   Six: Siege!"}
	if slug := withoutSlug.Slug(); slug != "tom-clancys-rainbow-six-siege" {
		t.Fatalf("生成 slug 不匹配: %q", slug)
	}
}

func TestCampaignEligibleHonorsBadgeToggle(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-badge",
		Name:     "badge",
		Game:     Game{ID: 1, Name: "Game"},
		Linked:   false,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "drop-badge",
				Name:            "badge",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(time.Hour),
				RequiredMinutes: 15,
				Benefits: []Benefit{
					{ID: "benefit-badge", Name: "Badge", Type: BenefitTypeBadge},
				},
			},
		},
	})

	if campaign.Eligible(false) {
		t.Fatal("badge/emote 活动在设置关闭时不应可领取")
	}
	if !campaign.Eligible(true) {
		t.Fatal("badge/emote 活动在设置开启时应可领取")
	}
}

func TestNewCampaignNormalizesClaimedDropAndRejectsDuplicateID(t *testing.T) {
	t.Parallel()

	now := testTime()

	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-constructor",
		Name:     "constructor",
		Game:     Game{ID: 9, Name: "Game"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:                  "drop-claimed",
				Name:                "drop-claimed",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(time.Hour),
				RequiredMinutes:     20,
				RealCurrentMinutes:  5,
				ExtraCurrentMinutes: 7,
				IsClaimed:           true,
				Benefits: []Benefit{
					{ID: "benefit-claimed", Name: "Claimed", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	drop := campaign.Drop("drop-claimed")
	if drop.RealCurrentMinutes != 20 {
		t.Fatalf("已领取掉宝应被归一化为满进度: %d", drop.RealCurrentMinutes)
	}
	if drop.ExtraCurrentMinutes != 0 {
		t.Fatalf("已领取掉宝不应保留额外分钟: %d", drop.ExtraCurrentMinutes)
	}

	_, err := NewCampaign(CampaignSpec{
		ID:       "campaign-duplicate",
		Name:     "duplicate",
		Game:     Game{ID: 10, Name: "Game"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{ID: "dup", Name: "dup-1", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), RequiredMinutes: 10},
			{ID: "dup", Name: "dup-2", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), RequiredMinutes: 20},
		},
	})
	if err == nil {
		t.Fatal("重复 drop id 应返回错误")
	}
}

func TestTimedDropCanEarnRespectsPreconditionsACLAndGame(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := Game{ID: 11, Name: "Apex Legends"}
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-main",
		Name:     "main",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(3 * time.Hour),
		AllowedChannels: []Channel{
			{ID: 101, Login: "allowed", ACLBased: true},
		},
		Drops: []TimedDropSpec{
			{
				ID:              "drop-1",
				Name:            "drop-1",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(3 * time.Hour),
				RequiredMinutes: 30,
				IsClaimed:       true,
				Benefits: []Benefit{
					{ID: "benefit-1", Name: "Reward 1", Type: BenefitTypeDirectEntitlement},
				},
			},
			{
				ID:                  "drop-2",
				Name:                "drop-2",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(3 * time.Hour),
				RequiredMinutes:     45,
				PreconditionDropIDs: []string{"drop-1"},
				Benefits: []Benefit{
					{ID: "benefit-2", Name: "Reward 2", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	drop := campaign.Drop("drop-2")
	allowedChannel := &Channel{
		ID:          101,
		Login:       "allowed",
		DisplayName: "Allowed",
		Stream: &Stream{
			BroadcastID:  201,
			Game:         &game,
			Viewers:      42,
			Title:        "Live",
			DropsEnabled: true,
		},
	}
	wrongChannel := &Channel{
		ID:    999,
		Login: "wrong",
		Stream: &Stream{
			BroadcastID:  202,
			Game:         &game,
			DropsEnabled: true,
		},
	}
	wrongGameChannel := &Channel{
		ID:    101,
		Login: "allowed",
		Stream: &Stream{
			BroadcastID:  203,
			Game:         &Game{ID: 12, Name: "Fortnite"},
			DropsEnabled: true,
		},
	}

	if !drop.PreconditionsMet() {
		t.Fatal("前置掉宝已领取时应判定为满足")
	}
	if !drop.CanEarn(now, allowedChannel, false, false) {
		t.Fatal("满足前置、ACL 和游戏时应可推进")
	}
	if drop.CanEarn(now, wrongChannel, false, false) {
		t.Fatal("不在 ACL 中的频道不应可推进")
	}
	if drop.CanEarn(now, wrongGameChannel, false, false) {
		t.Fatal("游戏不匹配的频道不应可推进")
	}
	if !drop.CanEarn(now, wrongGameChannel, false, true) {
		t.Fatal("忽略频道状态时应跳过游戏校验")
	}
}

func TestTimedDropRemainingAndAvailabilityIncludePreconditions(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-chain",
		Name:     "chain",
		Game:     Game{ID: 10, Name: "Game"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-2 * time.Hour),
		EndsAt:   now.Add(2 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:                 "drop-1",
				Name:               "drop-1",
				StartsAt:           now.Add(-2 * time.Hour),
				EndsAt:             now.Add(2 * time.Hour),
				RequiredMinutes:    30,
				RealCurrentMinutes: 10,
				Benefits: []Benefit{
					{ID: "benefit-1", Name: "Reward 1", Type: BenefitTypeDirectEntitlement},
				},
			},
			{
				ID:                  "drop-2",
				Name:                "drop-2",
				StartsAt:            now.Add(-2 * time.Hour),
				EndsAt:              now.Add(2 * time.Hour),
				RequiredMinutes:     15,
				RealCurrentMinutes:  3,
				PreconditionDropIDs: []string{"drop-1"},
				Benefits: []Benefit{
					{ID: "benefit-2", Name: "Reward 2", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	drop := campaign.Drop("drop-2")
	if totalRequired := drop.TotalRequiredMinutes(); totalRequired != 45 {
		t.Fatalf("总所需分钟数不匹配: %d", totalRequired)
	}
	if totalRemaining := drop.TotalRemainingMinutes(); totalRemaining != 32 {
		t.Fatalf("总剩余分钟数不匹配: %d", totalRemaining)
	}

	expectedAvailability := 120.0 / 32.0
	if availability := drop.Availability(now); math.Abs(availability-expectedAvailability) > 1e-9 {
		t.Fatalf("availability 不匹配: got=%v want=%v", availability, expectedAvailability)
	}
	if progress := drop.Progress(); math.Abs(progress-0.2) > 1e-9 {
		t.Fatalf("progress 不匹配: %v", progress)
	}
}

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

func TestChannelHelpersReflectStreamState(t *testing.T) {
	t.Parallel()

	channel := &Channel{ID: 1, Login: "streamer", DisplayName: "Streamer"}
	if !channel.Offline() {
		t.Fatal("无流且无 pending 状态时应判定为 offline")
	}
	if channel.PendingOnline() {
		t.Fatal("默认不应为 pending 状态")
	}

	channel.PendingStream = true
	if !channel.PendingOnline() {
		t.Fatal("pending 标记后应判定为 pending online")
	}

	game := &Game{ID: 1, Name: "Game"}
	channel.PendingStream = false
	channel.Stream = &Stream{BroadcastID: 1, Game: game, Viewers: 99, Title: "Live"}
	if !channel.Online() {
		t.Fatal("有流时应判定为 online")
	}
	if got := channel.CurrentGame(); got == nil || got.ID != game.ID {
		t.Fatalf("频道当前游戏不匹配: %#v", got)
	}
	if viewers := channel.ViewerCount(); viewers != 99 {
		t.Fatalf("viewer count 不匹配: %d", viewers)
	}
}

func mustCampaign(t *testing.T, spec CampaignSpec) *DropsCampaign {
	t.Helper()

	campaign, err := NewCampaign(spec)
	if err != nil {
		t.Fatalf("NewCampaign 返回错误: %v", err)
	}
	return campaign
}

func testTime() time.Time {
	return time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)
}
