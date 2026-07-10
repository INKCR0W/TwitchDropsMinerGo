package domain

import (
	"math"
	"testing"
	"time"
)

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

	if !campaign.ObserveMinutes(drop, 7) {
		t.Fatal("写入服务器进度应返回 true")
	}
	if drop.RealCurrentMinutes != 7 || drop.ExtraCurrentMinutes != 0 {
		t.Fatalf("写入进度后状态不匹配: real=%d extra=%d", drop.RealCurrentMinutes, drop.ExtraCurrentMinutes)
	}
	if !campaign.ObserveMinutes(drop, 9) {
		t.Fatal("提高服务器进度应返回 true")
	}
	if drop.RealCurrentMinutes != 9 {
		t.Fatalf("提高进度未生效: %d", drop.RealCurrentMinutes)
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
