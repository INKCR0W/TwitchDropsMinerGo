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
	scheduler.applyDropProgress(now, &channel, "drop-a", 30)

	output := logs.String()
	if got := strings.Count(output, "开始挂新掉落"); got != 2 {
		t.Fatalf("跨过一个档位应各记一条概览, 实际 %d:\n%s", got, output)
	}
	if !strings.Contains(output, "drop=Facemask") || !strings.Contains(output, "drop=Door") {
		t.Fatalf("应先后宣布 Facemask 与 Door:\n%s", output)
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
