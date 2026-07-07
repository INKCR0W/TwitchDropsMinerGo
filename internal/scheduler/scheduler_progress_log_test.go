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

func TestApplyDropProgressLogsOverviewAndProgress(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := mustCampaign(t, campaignSpecWithDrop("campaign-progress", game, now.Add(-time.Hour), now.Add(3*time.Hour), nil, domain.TimedDropSpec{
		ID:              "drop-a",
		Name:            "Facemask",
		RequiredMinutes: 30,
	}))

	logs := &logBuffer{}
	scheduler := newTestScheduler(t, testSchedulerOptions{logger: logs.logger()})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	channel := progressLogChannel(9, game)
	scheduler.channels = map[int64]domain.Channel{9: channel}
	scheduler.watchingChannelID = 9

	if !scheduler.applyDropProgress(now, &channel, "drop-a", 12) {
		t.Fatalf("applyDropProgress 应成功")
	}

	output := logs.String()
	if !strings.Contains(output, "开始挂新掉落") {
		t.Fatalf("缺少概览日志:\n%s", output)
	}
	for _, want := range []string{
		"campaign_required_minutes=30",
		"drops_total=1",
		"Facemask:12/30",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("概览日志缺字段 %q:\n%s", want, output)
		}
	}

	if !strings.Contains(output, "挂机进度") {
		t.Fatalf("缺少进度日志:\n%s", output)
	}
	for _, want := range []string{
		"drop=Facemask",
		"drop_watched_minutes=12",
		"drop_required_minutes=30",
		"drop_remaining_minutes=18",
		"campaign_remaining_minutes=18",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("进度日志缺字段 %q:\n%s", want, output)
		}
	}
}

func TestApplyDropProgressDoesNotRepeatOverviewForSameDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := mustCampaign(t, campaignSpecWithDrop("campaign-progress", game, now.Add(-time.Hour), now.Add(3*time.Hour), nil, domain.TimedDropSpec{
		ID:              "drop-a",
		Name:            "Facemask",
		RequiredMinutes: 30,
	}))

	logs := &logBuffer{}
	scheduler := newTestScheduler(t, testSchedulerOptions{logger: logs.logger()})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	channel := progressLogChannel(9, game)
	scheduler.channels = map[int64]domain.Channel{9: channel}
	scheduler.watchingChannelID = 9

	scheduler.applyDropProgress(now, &channel, "drop-a", 12)
	scheduler.applyDropProgress(now, &channel, "drop-a", 13)

	output := logs.String()
	if got := strings.Count(output, "开始挂新掉落"); got != 1 {
		t.Fatalf("概览应只记一次, 实际 %d:\n%s", got, output)
	}
	if got := strings.Count(output, "挂机进度"); got != 2 {
		t.Fatalf("两次进度应各记一行, 实际 %d:\n%s", got, output)
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
	scheduler := newTestScheduler(t, testSchedulerOptions{logger: logs.logger()})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	channel := progressLogChannel(9, game)
	scheduler.channels = map[int64]domain.Channel{9: channel}
	scheduler.watchingChannelID = 9

	scheduler.applyDropProgress(now, &channel, "drop-a", 12)
	scheduler.applyDropProgress(now, &channel, "drop-b", 5)

	output := logs.String()
	if got := strings.Count(output, "开始挂新掉落"); got != 2 {
		t.Fatalf("切换 drop 应各记一条概览, 实际 %d:\n%s", got, output)
	}
}

func TestBumpActiveCampaignLogsProgress(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Rust"}
	campaign := mustCampaign(t, campaignSpecWithDrop("campaign-bump", game, now.Add(-time.Hour), now.Add(3*time.Hour), nil, domain.TimedDropSpec{
		ID:              "drop-a",
		Name:            "Facemask",
		RequiredMinutes: 30,
	}))

	logs := &logBuffer{}
	scheduler := newTestScheduler(t, testSchedulerOptions{logger: logs.logger()})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	channel := progressLogChannel(9, game)
	scheduler.channels = map[int64]domain.Channel{9: channel}
	scheduler.watchingChannelID = 9

	_, _, updated := scheduler.bumpActiveCampaign(now, &channel)
	if !updated {
		t.Fatalf("bumpActiveCampaign 应更新进度")
	}

	output := logs.String()
	if !strings.Contains(output, "开始挂新掉落") {
		t.Fatalf("缺少概览日志:\n%s", output)
	}
	if !strings.Contains(output, "挂机进度") {
		t.Fatalf("缺少进度日志:\n%s", output)
	}
	if !strings.Contains(output, "drop_watched_minutes=1") {
		t.Fatalf("本地兜底 +1 分钟未体现:\n%s", output)
	}
}
