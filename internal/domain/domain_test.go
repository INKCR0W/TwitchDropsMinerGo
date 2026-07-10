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

func TestGameIsSpecialCoversEverySpecialCategory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		game Game
		want bool
	}{
		{name: "special events", game: Game{ID: SpecialEventsGameID, Name: "Special Events"}, want: true},
		{name: "special events by name", game: Game{Name: "Special Events"}, want: true},
		{name: "special events by slug", game: Game{SlugText: "special-events"}, want: true},
		{name: "irl", game: Game{ID: IRLGameID, Name: "IRL"}, want: true},
		{name: "regular game", game: Game{ID: 460630, Name: "Tom Clancy's Rainbow Six Siege"}, want: false},
	}
	for _, testCase := range cases {
		if got := testCase.game.IsSpecial(); got != testCase.want {
			t.Errorf("%s: IsSpecial() = %v, want %v", testCase.name, got, testCase.want)
		}
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

func TestNewCampaignInfersAutoClaimedSpecialEventMilestones(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-badge-milestone",
		Name:     "badge-milestone",
		Game:     Game{ID: SpecialEventsGameID, Name: "Special Events"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(24 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "bronze",
				Name:            "Bronze",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 60,
				Benefits: []Benefit{
					{ID: "bronze-benefit", Name: "Bronze", Type: BenefitTypeBadge},
				},
			},
			{
				ID:              "platinum",
				Name:            "Platinum",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 360,
				Benefits: []Benefit{
					{ID: "platinum-benefit", Name: "Platinum", Type: BenefitTypeBadge},
				},
			},
			{
				ID:                 "diamond",
				Name:               "Diamond Reward Group",
				StartsAt:           now.Add(-time.Hour),
				EndsAt:             now.Add(24 * time.Hour),
				RequiredMinutes:    720,
				RealCurrentMinutes: 720,
				Benefits: []Benefit{
					{ID: "diamond-benefit", Name: "Diamond", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	for _, dropID := range []string{"bronze", "platinum", "diamond"} {
		drop := campaign.Drop(dropID)
		if drop == nil || !drop.IsClaimed {
			t.Fatalf("已达到观看里程碑的 Special Events drop 应被视为 claimed: %s %#v", dropID, drop)
		}
		if drop.CurrentMinutes() != drop.RequiredMinutes {
			t.Fatalf("自动领取的 Special Events drop 应归一化为满进度: %s current=%d required=%d", dropID, drop.CurrentMinutes(), drop.RequiredMinutes)
		}
	}
}

func TestNewCampaignInfersSpecialEventMilestonesByName(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-special-events-name",
		Name:     "special-events-name",
		Game:     Game{Name: "Special Events"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(24 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "bronze",
				Name:            "Bronze",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 60,
				Benefits: []Benefit{
					{ID: "bronze-benefit", Name: "Bronze", Type: BenefitTypeBadge},
				},
			},
			{
				ID:                 "diamond",
				Name:               "Diamond Reward Group",
				StartsAt:           now.Add(-time.Hour),
				EndsAt:             now.Add(24 * time.Hour),
				RequiredMinutes:    720,
				RealCurrentMinutes: 720,
				Benefits: []Benefit{
					{ID: "diamond-benefit", Name: "Diamond", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	for _, dropID := range []string{"bronze", "diamond"} {
		if drop := campaign.Drop(dropID); drop == nil || !drop.IsClaimed {
			t.Fatalf("Special Events name fallback 应触发累计里程碑归一化: %s %#v", dropID, drop)
		}
	}
}

func TestNewCampaignDoesNotInferAutoClaimAcrossPreconditionChain(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-badge-chain",
		Name:     "badge-chain",
		Game:     Game{ID: SpecialEventsGameID, Name: "Special Events"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(24 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "tier-1",
				Name:            "Tier 1",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 60,
				IsClaimed:       true,
				Benefits: []Benefit{
					{ID: "tier-1-benefit", Name: "Tier 1", Type: BenefitTypeEmote},
				},
			},
			{
				ID:                  "tier-2",
				Name:                "Tier 2",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(24 * time.Hour),
				RequiredMinutes:     60,
				PreconditionDropIDs: []string{"tier-1"},
				Benefits: []Benefit{
					{ID: "tier-2-benefit", Name: "Tier 2", Type: BenefitTypeEmote},
				},
			},
		},
	})

	tier2 := campaign.Drop("tier-2")
	if tier2.IsClaimed {
		t.Fatal("前置链上 RequiredMinutes 是相对量，不应被前置 drop 的观看时长推断为 claimed")
	}
	if campaign.Finished() {
		t.Fatal("仍有未领取 drop 的活动不应被视为已完成")
	}
}

func TestNewCampaignDoesNotInferAutoClaimAcrossDropWindows(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-badge-windows",
		Name:     "badge-windows",
		Game:     Game{ID: SpecialEventsGameID, Name: "Special Events"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-48 * time.Hour),
		EndsAt:   now.Add(48 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "week-1",
				Name:            "Week 1",
				StartsAt:        now.Add(-48 * time.Hour),
				EndsAt:          now.Add(-24 * time.Hour),
				RequiredMinutes: 60,
				IsClaimed:       true,
				Benefits: []Benefit{
					{ID: "week-1-benefit", Name: "Week 1", Type: BenefitTypeBadge},
				},
			},
			{
				ID:              "week-2",
				Name:            "Week 2",
				StartsAt:        now.Add(-time.Minute),
				EndsAt:          now.Add(48 * time.Hour),
				RequiredMinutes: 60,
				Benefits: []Benefit{
					{ID: "week-2-benefit", Name: "Week 2", Type: BenefitTypeBadge},
				},
			},
		},
	})

	week2 := campaign.Drop("week-2")
	if week2.IsClaimed {
		t.Fatal("另一个时间窗的 drop 有独立计数，不应被上一窗口的观看时长推断为 claimed")
	}

	channel := &Channel{
		ID:    701,
		Login: "channel",
		Stream: &Stream{
			BroadcastID:  801,
			Game:         &Game{ID: SpecialEventsGameID, Name: "Special Events"},
			DropsEnabled: true,
		},
	}
	if !campaign.CanEarn(now, channel, true, false) {
		t.Fatal("当前窗口的 drop 仍需观看，活动应保持可推进")
	}
}

func TestNewCampaignIgnoresEstimatedMinutesWhenInferringAutoClaim(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-badge-estimated",
		Name:     "badge-estimated",
		Game:     Game{ID: SpecialEventsGameID, Name: "Special Events"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(24 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:                  "bronze",
				Name:                "Bronze",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(24 * time.Hour),
				RequiredMinutes:     60,
				RealCurrentMinutes:  50,
				ExtraCurrentMinutes: 10,
				Benefits: []Benefit{
					{ID: "bronze-benefit", Name: "Bronze", Type: BenefitTypeBadge},
				},
			},
		},
	})

	bronze := campaign.Drop("bronze")
	if bronze.IsClaimed {
		t.Fatal("本地估算分钟不是服务器事实，不足以推断 drop 已自动领取")
	}
	if bronze.RealCurrentMinutes != 50 {
		t.Fatalf("推断失败时不应改写真实观看时长: %d", bronze.RealCurrentMinutes)
	}
}

func TestNewCampaignSkipsAutoClaimInferenceForNonSpecialEventsGames(t *testing.T) {
	t.Parallel()

	now := testTime()
	cases := []struct {
		name string
		game Game
	}{
		{name: "normal game", game: Game{ID: 77, Name: "Normal Game"}},
		{name: "irl", game: Game{ID: IRLGameID, Name: "IRL"}},
	}
	for _, testCase := range cases {
		campaign := mustCampaign(t, CampaignSpec{
			ID:       "campaign-badge-" + testCase.name,
			Name:     "badge-" + testCase.name,
			Game:     testCase.game,
			Linked:   true,
			Status:   "ACTIVE",
			StartsAt: now.Add(-time.Hour),
			EndsAt:   now.Add(24 * time.Hour),
			Drops: []TimedDropSpec{
				{
					ID:              "low",
					Name:            "Low",
					StartsAt:        now.Add(-time.Hour),
					EndsAt:          now.Add(24 * time.Hour),
					RequiredMinutes: 60,
					Benefits: []Benefit{
						{ID: "low-benefit", Name: "Low", Type: BenefitTypeBadge},
					},
				},
				{
					ID:                 "high",
					Name:               "High",
					StartsAt:           now.Add(-time.Hour),
					EndsAt:             now.Add(24 * time.Hour),
					RequiredMinutes:    120,
					RealCurrentMinutes: 120,
					Benefits: []Benefit{
						{ID: "high-benefit", Name: "High", Type: BenefitTypeBadge},
					},
				},
			},
		})

		if campaign.Drop("low").IsClaimed {
			t.Fatalf("累计里程碑推断只适用于 Special Events，%s 活动不应改写 claim 状态", testCase.name)
		}
	}
}

func TestNewCampaignRejectsPreconditionCycles(t *testing.T) {
	t.Parallel()

	now := testTime()
	_, err := NewCampaign(CampaignSpec{
		ID:       "campaign-cycle",
		Name:     "cycle",
		Game:     Game{ID: 1, Name: "Game"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:                  "drop-a",
				Name:                "A",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(time.Hour),
				RequiredMinutes:     10,
				PreconditionDropIDs: []string{"drop-a"},
			},
		},
	})
	if err == nil {
		t.Fatal("自引用 precondition 应返回错误")
	}

	_, err = NewCampaign(CampaignSpec{
		ID:       "campaign-cycle-2",
		Name:     "cycle2",
		Game:     Game{ID: 1, Name: "Game"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{ID: "drop-a", Name: "A", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), RequiredMinutes: 10, PreconditionDropIDs: []string{"drop-b"}},
			{ID: "drop-b", Name: "B", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), RequiredMinutes: 10, PreconditionDropIDs: []string{"drop-a"}},
		},
	})
	if err == nil {
		t.Fatal("循环 precondition 应返回错误")
	}
}

func TestNewCampaignRejectsMissingPreconditionDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	_, err := NewCampaign(CampaignSpec{
		ID:       "campaign-missing-precondition",
		Name:     "missing",
		Game:     Game{ID: 1, Name: "Game"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:                  "drop-a",
				Name:                "A",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(time.Hour),
				RequiredMinutes:     10,
				PreconditionDropIDs: []string{"drop-missing"},
			},
		},
	})
	if err == nil {
		t.Fatal("缺失 precondition drop 应返回错误")
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

func TestTimedDropFullProgressIsNotEarnable(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := Game{ID: 41, Name: "Game"}
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-full-progress",
		Name:     "full-progress",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(2 * time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:                 "drop-full",
				Name:               "drop-full",
				StartsAt:           now.Add(-time.Hour),
				EndsAt:             now.Add(2 * time.Hour),
				RequiredMinutes:    30,
				RealCurrentMinutes: 30,
				Benefits: []Benefit{
					{ID: "benefit-full", Name: "Full", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})
	channel := &Channel{
		ID:    411,
		Login: "channel",
		Stream: &Stream{
			BroadcastID:  511,
			Game:         &game,
			DropsEnabled: true,
		},
	}
	drop := campaign.Drop("drop-full")

	if drop.CanEarn(now, channel, false, false) {
		t.Fatal("已满进度但未 claimed 的 drop 不应继续被视为可赚取")
	}
	if campaign.CanEarn(now, channel, false, false) {
		t.Fatal("只有满进度 drop 的活动不应继续被视为可推进")
	}
	if campaign.CanEarnWithin(now, now.Add(time.Hour), false) {
		t.Fatal("已满进度 drop 不应继续进入未来一小时规划")
	}
	if first := campaign.FirstEarnableDrop(now, channel, false, false); first != nil {
		t.Fatalf("已满进度 drop 不应被选为当前可挂目标: %#v", first)
	}
	if reachedLimit := campaign.BumpMinutes(now, channel, false, false); reachedLimit {
		t.Fatal("已满进度 drop 不应再补本地估算分钟")
	}
	if drop.ExtraCurrentMinutes != 0 {
		t.Fatalf("已满进度 drop 不应增加额外分钟: %d", drop.ExtraCurrentMinutes)
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

func TestTimedDropMutationHelpers(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := Game{ID: 50, Name: "Game"}
	campaign := mustCampaign(t, CampaignSpec{
		ID:       "campaign-mutate",
		Name:     "mutate",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []TimedDropSpec{
			{
				ID:              "drop-mutate",
				Name:            "drop-mutate",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(time.Hour),
				RequiredMinutes: 10,
				Benefits: []Benefit{
					{ID: "benefit-mutate", Name: "Reward", Type: BenefitTypeDirectEntitlement},
				},
			},
		},
	})
	drop := campaign.Drop("drop-mutate")
	channel := &Channel{
		ID:    500,
		Login: "channel",
		Stream: &Stream{
			BroadcastID:  600,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	if !drop.UpdateMinutes(7) {
		t.Fatal("更新分钟应返回 true")
	}
	if drop.RealCurrentMinutes != 7 || drop.ExtraCurrentMinutes != 0 {
		t.Fatalf("更新分钟后状态不匹配: real=%d extra=%d", drop.RealCurrentMinutes, drop.ExtraCurrentMinutes)
	}
	if !campaign.UpdateMinutes(now, channel, false, false, 9) {
		t.Fatal("活动更新分钟应返回 true")
	}
	if drop.RealCurrentMinutes != 9 {
		t.Fatalf("活动更新分钟未生效: %d", drop.RealCurrentMinutes)
	}
	if reached := campaign.BumpMinutes(now, channel, false, false); reached {
		t.Fatal("首次补分钟不应达到上限")
	}
	if drop.ExtraCurrentMinutes != 1 {
		t.Fatalf("补分钟未生效: %d", drop.ExtraCurrentMinutes)
	}

	drop.UpdateClaim(drop.GenerateClaimID(42))
	if drop.ClaimID != "42#campaign-mutate#drop-mutate" {
		t.Fatalf("claim id 生成不匹配: %q", drop.ClaimID)
	}
	if !drop.MarkClaimed() {
		t.Fatal("标记领取应返回 true")
	}
	if !drop.IsClaimed || drop.RealCurrentMinutes != drop.RequiredMinutes || drop.ExtraCurrentMinutes != 0 {
		t.Fatalf("标记领取后状态不匹配: %#v", drop)
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
