package scheduler

import (
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
)

func progressLogChannel(id int64, game domain.Game) domain.Channel {
	return domain.Channel{
		ID:    id,
		Login: "watching",
		Stream: &domain.Stream{
			BroadcastID:  id * 10,
			Game:         &game,
			DropsEnabled: true,
		},
	}
}

func newProgressLogScheduler(t *testing.T, logs *logBuffer, campaign *domain.DropsCampaign, game domain.Game) *Scheduler {
	t.Helper()

	scheduler := newTestScheduler(t, testSchedulerOptions{logger: logs.logger()})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	channel := progressLogChannel(9, game)
	scheduler.channels = map[int64]domain.Channel{9: channel}
	scheduler.watchingChannelID = 9
	return scheduler
}

func TestApplyDropProgressLogsOverviewWithoutPerMinuteLine(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := mustCampaign(t, campaignSpecWithDrop("campaign-progress", game, now.Add(-time.Hour), now.Add(3*time.Hour), nil, domain.TimedDropSpec{
		ID:              "drop-a",
		Name:            "Facemask",
		RequiredMinutes: 30,
	}))

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	channel := progressLogChannel(9, game)

	if !scheduler.applyDropProgress(now, &channel, "drop-a", 12) {
		t.Fatalf("applyDropProgress 应成功")
	}

	output := logs.String()
	if !strings.Contains(output, "开始挂新掉落") {
		t.Fatalf("缺少概览日志:\n%s", output)
	}
	if !strings.Contains(output, "Facemask:12/30") {
		t.Fatalf("概览缺 drops_detail:\n%s", output)
	}
	if strings.Contains(output, "挂机进度") {
		t.Fatalf("不应再有每分钟挂机进度日志:\n%s", output)
	}
}

func TestApplyDropProgressLogsOverviewOncePerDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := mustCampaign(t, campaignSpecWithDrop("campaign-progress", game, now.Add(-time.Hour), now.Add(3*time.Hour), nil, domain.TimedDropSpec{
		ID:              "drop-a",
		Name:            "Facemask",
		RequiredMinutes: 30,
	}))

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	channel := progressLogChannel(9, game)

	if !scheduler.applyDropProgress(now, &channel, "drop-a", 12) {
		t.Fatalf("首次 applyDropProgress 应成功")
	}
	if !scheduler.applyDropProgress(now, &channel, "drop-a", 13) {
		t.Fatalf("第二次 applyDropProgress 应成功")
	}

	if got := strings.Count(logs.String(), "开始挂新掉落"); got != 1 {
		t.Fatalf("同一 drop 概览应只记一次, 实际 %d:\n%s", got, logs.String())
	}
}

func TestApplyDropProgressLogsNewOverviewWhenDropChanges(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := mustCampaign(t, domain.CampaignSpec{
		ID:       "campaign-multi",
		Name:     "campaign-multi",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(5 * time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:              "drop-a",
				Name:            "Facemask",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(5 * time.Hour),
				RequiredMinutes: 30,
				Benefits: []domain.Benefit{
					{ID: "benefit-a", Name: "reward-a", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
			{
				ID:              "drop-b",
				Name:            "Door",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(5 * time.Hour),
				RequiredMinutes: 60,
				Benefits: []domain.Benefit{
					{ID: "benefit-b", Name: "reward-b", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	channel := progressLogChannel(9, game)

	scheduler.applyDropProgress(now, &channel, "drop-a", 12)
	scheduler.applyDropProgress(now, &channel, "drop-b", 5)

	if got := strings.Count(logs.String(), "开始挂新掉落"); got != 2 {
		t.Fatalf("切换 drop 应各记一条概览, 实际 %d:\n%s", got, logs.String())
	}
}

func multiDropCampaign(t *testing.T, now time.Time, game domain.Game) *domain.DropsCampaign {
	t.Helper()
	return mustCampaign(t, domain.CampaignSpec{
		ID:       "campaign-multi",
		Name:     "campaign-multi",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(5 * time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:              "drop-a",
				Name:            "Facemask",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(5 * time.Hour),
				RequiredMinutes: 30,
				Benefits:        []domain.Benefit{{ID: "benefit-a", Name: "reward-a", Type: domain.BenefitTypeDirectEntitlement}},
			},
			{
				ID:              "drop-b",
				Name:            "Door",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(5 * time.Hour),
				RequiredMinutes: 60,
				Benefits:        []domain.Benefit{{ID: "benefit-b", Name: "reward-b", Type: domain.BenefitTypeDirectEntitlement}},
			},
		},
	})
}

func TestApplyDropProgressAnnouncesEachDropOnceDespiteInterleaving(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := multiDropCampaign(t, now, game)

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	channel := progressLogChannel(9, game)

	scheduler.applyDropProgress(now, &channel, "drop-a", 1)
	scheduler.applyDropProgress(now, &channel, "drop-b", 1)
	scheduler.applyDropProgress(now, &channel, "drop-a", 2)
	scheduler.applyDropProgress(now, &channel, "drop-b", 2)
	scheduler.applyDropProgress(now, &channel, "drop-a", 3)

	if got := strings.Count(logs.String(), "开始挂新掉落"); got != 2 {
		t.Fatalf("交替累进时每个 drop 只应宣布一次, 实际 %d:\n%s", got, logs.String())
	}
}

func TestProgressAnnouncementsPersistAcrossInventoryReload(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := multiDropCampaign(t, now, game)

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	channel := progressLogChannel(9, game)

	scheduler.applyDropProgress(now, &channel, "drop-a", 1)
	scheduler.mu.Lock()
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.mu.Unlock()
	scheduler.applyDropProgress(now, &channel, "drop-a", 2)

	if got := strings.Count(logs.String(), "开始挂新掉落"); got != 1 {
		t.Fatalf("未切频道时 reload 不应重复宣布同一 drop, 实际 %d:\n%s", got, logs.String())
	}
}

func TestProgressAnnouncementsResetOnChannelSwitch(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := multiDropCampaign(t, now, game)

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	channel := progressLogChannel(9, game)
	scheduler.channels[10] = progressLogChannel(10, game)

	scheduler.applyDropProgress(now, &channel, "drop-a", 1)
	scheduler.watch(10)
	scheduler.applyDropProgress(now, &channel, "drop-a", 2)

	if got := strings.Count(logs.String(), "开始挂新掉落"); got != 2 {
		t.Fatalf("切换频道后同一 drop 应重新宣布一次, 实际 %d:\n%s", got, logs.String())
	}
}

func TestProcessDropProgressLogsEnrichedUpdate(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rainbow Six Siege"}
	campaign := mustCampaign(t, campaignSpecWithDrop("campaign-progress", game, now.Add(-time.Hour), now.Add(9*time.Hour), nil, domain.TimedDropSpec{
		ID:              "drop-a",
		Name:            "Esports Pack",
		RequiredMinutes: 60,
	}))

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)

	scheduler.processDropProgress(dropEventMessage{Data: dropEventData{
		DropID:              "drop-a",
		CurrentProgressMin:  3,
		RequiredProgressMin: 60,
	}})

	output := logs.String()
	if !strings.Contains(output, "收到掉宝进度更新") {
		t.Fatalf("缺少 收到掉宝进度更新:\n%s", output)
	}
	for _, want := range []string{
		"drop_id=drop-a",
		"current_minutes=3",
		"required_minutes=60",
		`drop="Esports Pack"`,
		`game="Rainbow Six Siege"`,
		"drop_remaining_minutes=57",
		"campaign_remaining_minutes=57",
		"campaign_required_minutes=60",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("收到掉宝进度更新 缺字段 %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "挂机进度") {
		t.Fatalf("不应有 挂机进度 日志:\n%s", output)
	}
}

func TestBumpActiveCampaignLogsOverviewOnly(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := mustCampaign(t, campaignSpecWithDrop("campaign-bump", game, now.Add(-time.Hour), now.Add(3*time.Hour), nil, domain.TimedDropSpec{
		ID:              "drop-a",
		Name:            "Facemask",
		RequiredMinutes: 30,
	}))

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	channel := progressLogChannel(9, game)

	_, _, updated := scheduler.bumpActiveCampaign(now, &channel)
	if !updated {
		t.Fatalf("bumpActiveCampaign 应更新进度")
	}

	output := logs.String()
	if !strings.Contains(output, "开始挂新掉落") {
		t.Fatalf("缺少概览日志:\n%s", output)
	}
	if strings.Contains(output, "挂机进度") {
		t.Fatalf("本地兜底不应再打每分钟挂机进度:\n%s", output)
	}
}

func TestApplyDropProgressNormalizesFullSpecialEventRewardGroup(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{Name: "Special Events"}
	campaign := mustCampaign(t, domain.CampaignSpec{
		ID:       "campaign-ewc",
		Name:     "EWC 2026",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(24 * time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:              "bronze",
				Name:            "EWC Bronze",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 60,
				Benefits: []domain.Benefit{
					{ID: "bronze-benefit", Name: "Bronze", Type: domain.BenefitTypeBadge},
				},
			},
			{
				ID:              "platinum",
				Name:            "EWC Platinum",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 360,
				Benefits: []domain.Benefit{
					{ID: "platinum-benefit", Name: "Platinum", Type: domain.BenefitTypeBadge},
				},
			},
			{
				ID:              "diamond",
				Name:            "EWC 2026 (Diamond) Reward Group",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 720,
				Benefits: []domain.Benefit{
					{ID: "diamond-benefit", Name: "Diamond", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	scheduler.settings.EnableBadgesEmotes = true
	channel := progressLogChannel(9, game)

	if !scheduler.applyDropProgress(now, &channel, "diamond", 720) {
		t.Fatal("CurrentDrop 返回满进度 reward group 时应接受权威进度")
	}

	for _, dropID := range []string{"bronze", "platinum", "diamond"} {
		if drop := campaign.Drop(dropID); drop == nil || !drop.IsClaimed {
			t.Fatalf("满进度 reward group 应收口同窗口 Special Events 里程碑: %s %#v", dropID, drop)
		}
	}
	if campaign.CanEarn(now, &channel, true, false) {
		t.Fatal("Special Events 里程碑收口后活动不应继续可推进")
	}
	if strings.Contains(logs.String(), "开始挂新掉落") {
		t.Fatalf("满进度 reward group 不应再宣布为新掉落:\n%s", logs.String())
	}
}
