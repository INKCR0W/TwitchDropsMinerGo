package domain

import (
	"testing"
	"time"
)

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

func cumulativeCounterCampaign(t *testing.T, now time.Time) *DropsCampaign {
	t.Helper()

	return mustCampaign(t, CampaignSpec{
		ID:       "campaign-ewc",
		Name:     "EWC 2026",
		Game:     Game{ID: SpecialEventsGameID, Name: "Special Events"},
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(24 * time.Hour),
		Drops: []TimedDropSpec{
			{ID: "ultraviolet", Name: "EWC UltraViolet", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), RequiredMinutes: 0,
				Benefits: []Benefit{{ID: "uv", Name: "UltraViolet", Type: BenefitTypeBadge}}},
			{ID: "bronze", Name: "EWC Bronze", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), RequiredMinutes: 60,
				Benefits: []Benefit{{ID: "bronze", Name: "Bronze", Type: BenefitTypeBadge}}},
			{ID: "silver", Name: "EWC Silver", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), RequiredMinutes: 120,
				Benefits: []Benefit{{ID: "silver", Name: "Silver", Type: BenefitTypeBadge}}},
			{ID: "diamond", Name: "EWC 2026 (Diamond) Reward Group", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), RequiredMinutes: 720,
				Benefits: []Benefit{{ID: "diamond", Name: "Diamond", Type: BenefitTypeBadge}}},
		},
	})
}

func TestObserveMinutesSpreadsAcrossSharedCounterAndStopsCampaign(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := cumulativeCounterCampaign(t, now)
	channel := &Channel{ID: 1, Login: "ch", Stream: &Stream{BroadcastID: 1, Game: &campaign.Game, DropsEnabled: true}}

	if !campaign.ObserveMinutes(campaign.Drop("diamond"), 1006) {
		t.Fatal("首次写入服务器进度应返回 true")
	}

	for dropID, want := range map[string]int{"bronze": 60, "silver": 120, "diamond": 720} {
		drop := campaign.Drop(dropID)
		if drop.RealCurrentMinutes != want {
			t.Fatalf("%s 应被 clamp 到自己的阈值: real=%d want=%d", dropID, drop.RealCurrentMinutes, want)
		}
		if drop.IsClaimed {
			t.Fatalf("%s 不应被伪造成已认领", dropID)
		}
		if drop.CanEarn(now, channel, true, false) {
			t.Fatalf("%s 已满进度, 不应继续可推进", dropID)
		}
	}
	if campaign.Drop("ultraviolet").RealCurrentMinutes != 0 {
		t.Fatal("无观看阈值的 drop 不参与共享计数")
	}
	if campaign.CanEarn(now, channel, true, false) || campaign.CanEarnWithin(now, now.Add(time.Hour), true) {
		t.Fatal("所有档位满进度后活动不应继续可推进")
	}
}

func TestObserveMinutesKeepsLowerTiersMineableUntilReached(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := cumulativeCounterCampaign(t, now)
	channel := &Channel{ID: 1, Login: "ch", Stream: &Stream{BroadcastID: 1, Game: &campaign.Game, DropsEnabled: true}}

	campaign.ObserveMinutes(campaign.Drop("diamond"), 90)

	if campaign.Drop("bronze").CanEarn(now, channel, true, false) {
		t.Fatal("bronze 已满 60 分钟, 不应继续可推进")
	}
	if !campaign.Drop("silver").CanEarn(now, channel, true, false) {
		t.Fatal("silver 还差 30 分钟, 应继续可推进")
	}
	if first := campaign.FirstEarnableDrop(now, channel, true, false); first == nil || first.ID != "silver" {
		t.Fatalf("下一个目标应是 silver: %#v", first)
	}
}

func TestObserveMinutesNeverRegresses(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := cumulativeCounterCampaign(t, now)
	diamond := campaign.Drop("diamond")

	campaign.ObserveMinutes(diamond, 300)
	if campaign.ObserveMinutes(diamond, 0) {
		t.Fatal("回退的分钟数不应被接受")
	}
	if diamond.RealCurrentMinutes != 300 {
		t.Fatalf("回退不应改写已有进度: %d", diamond.RealCurrentMinutes)
	}
	if campaign.Drop("bronze").RealCurrentMinutes != 60 {
		t.Fatal("回退不应改写同组其它 drop 的进度")
	}
}

func TestObserveMinutesSkipsOtherWindowsPreconditionsAndClaimed(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := mustCampaign(t, CampaignSpec{
		ID: "campaign-windows", Name: "windows", Game: Game{ID: 1, Name: "Game"},
		Linked: true, Status: "ACTIVE", StartsAt: now.Add(-48 * time.Hour), EndsAt: now.Add(48 * time.Hour),
		Drops: []TimedDropSpec{
			{ID: "week1", Name: "week1", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), RequiredMinutes: 60,
				Benefits: []Benefit{{ID: "w1", Name: "w1", Type: BenefitTypeBadge}}},
			{ID: "week2", Name: "week2", StartsAt: now.Add(24 * time.Hour), EndsAt: now.Add(48 * time.Hour), RequiredMinutes: 60,
				Benefits: []Benefit{{ID: "w2", Name: "w2", Type: BenefitTypeBadge}}},
			{ID: "tier1", Name: "tier1", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), RequiredMinutes: 30, IsClaimed: true,
				Benefits: []Benefit{{ID: "t1", Name: "t1", Type: BenefitTypeBadge}}},
			{ID: "tier2", Name: "tier2", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), RequiredMinutes: 30,
				PreconditionDropIDs: []string{"tier1"},
				Benefits:            []Benefit{{ID: "t2", Name: "t2", Type: BenefitTypeBadge}}},
		},
	})

	campaign.ObserveMinutes(campaign.Drop("week1"), 999)

	if got := campaign.Drop("week2").RealCurrentMinutes; got != 0 {
		t.Fatalf("另一时间窗有独立计数, 不应被写入: %d", got)
	}
	if got := campaign.Drop("tier2").RealCurrentMinutes; got != 0 {
		t.Fatalf("带前置链的 drop 有独立计数, 不应被写入: %d", got)
	}
	if got := campaign.Drop("week1").RealCurrentMinutes; got != 60 {
		t.Fatalf("同窗口 drop 应 clamp 到自己的阈值: %d", got)
	}
}
