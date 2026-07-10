package scheduler

import (
	"strings"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
)

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

// 同一活动的 drop 共享计数, dropCurrentSession 报哪个 drop 都应只宣布最近的未达档位
func TestApplyDropProgressAnnouncesNearestTierRegardlessOfReportedDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := multiDropCampaign(t, now, game)

	logs := &logBuffer{}
	scheduler := newProgressLogScheduler(t, logs, campaign, game)
	channel := progressLogChannel(9, game)

	scheduler.applyDropProgress(now, &channel, "drop-b", 1)
	scheduler.applyDropProgress(now, &channel, "drop-a", 2)
	scheduler.applyDropProgress(now, &channel, "drop-b", 3)

	output := logs.String()
	if got := strings.Count(output, "开始挂新掉落"); got != 1 {
		t.Fatalf("同一档位只应宣布一次, 实际 %d:\n%s", got, output)
	}
	if !strings.Contains(output, "drop=Facemask") {
		t.Fatalf("应宣布最近的未达档位 Facemask:\n%s", output)
	}
	if got := campaign.Drop("drop-b").RealCurrentMinutes; got != 3 {
		t.Fatalf("共享计数应同步到同窗口的其它 drop: %d", got)
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
