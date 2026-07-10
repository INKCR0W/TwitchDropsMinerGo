package scheduler

import (
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
)

func stallTestChannel(game domain.Game) domain.Channel {
	return domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  2,
			Game:         &game,
			DropsEnabled: true,
		},
	}
}

func newStallScheduler(t *testing.T, now time.Time) (*Scheduler, domain.Channel) {
	t.Helper()

	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpecWithDrop(
		"campaign-stall",
		game,
		now.Add(-time.Hour),
		now.Add(time.Hour),
		nil,
		domain.TimedDropSpec{RequiredMinutes: 30, RealCurrentMinutes: 5},
	))

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{WatchStallMinutes: 10},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}

	channel := stallTestChannel(game)
	scheduler.channels = map[int64]domain.Channel{channel.ID: channel}
	scheduler.watchingChannelID = channel.ID
	return scheduler, channel
}

func TestCheckWatchStallAvoidsStalledChannelAndSwitches(t *testing.T) {
	t.Parallel()

	now := testTime()
	scheduler, channel := newStallScheduler(t, now)
	scheduler.lastAdvanceAt = now.Add(-11 * time.Minute)

	if !scheduler.canWatch(channel) {
		t.Fatal("前置条件: 卡住前频道应可观看")
	}

	scheduler.checkWatchStall(channel.ID, true)

	if scheduler.State() != StateChannelSwitch {
		t.Fatalf("长时间无进度后应请求切台: %s", scheduler.State())
	}
	if !scheduler.channelStalled(channel.ID, now) {
		t.Fatal("卡住频道应被标记为回避")
	}
	if scheduler.canWatch(channel) {
		t.Fatal("回避期内的频道不应再可观看")
	}
}

func TestCheckWatchStallLeavesAdvancingChannelAlone(t *testing.T) {
	t.Parallel()

	now := testTime()
	scheduler, channel := newStallScheduler(t, now)
	scheduler.lastAdvanceAt = now.Add(-3 * time.Minute)

	scheduler.checkWatchStall(channel.ID, true)

	if scheduler.State() != StateIdle {
		t.Fatalf("仍在推进的频道不应触发切台: %s", scheduler.State())
	}
	if scheduler.channelStalled(channel.ID, now) {
		t.Fatal("未卡住的频道不应被回避")
	}
}

func TestChannelStalledExpiresAfterCooldown(t *testing.T) {
	t.Parallel()

	now := testTime()
	scheduler, channel := newStallScheduler(t, now)
	scheduler.lastAdvanceAt = now.Add(-11 * time.Minute)
	scheduler.checkWatchStall(channel.ID, true)

	if !scheduler.channelStalled(channel.ID, now) {
		t.Fatal("刚判定卡住时应处于回避期")
	}
	if scheduler.channelStalled(channel.ID, now.Add(watchStallCooldown+time.Minute)) {
		t.Fatal("超过回避时长后应重新可观看")
	}
}

func TestBumpLocalTimedRewardResetsStallClock(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 27471, Name: "Minecraft"}
	campaign := mustCampaign(t, domain.CampaignSpec{
		ID:               "reward:builder-cape",
		Name:             "Builder Cape",
		Game:             game,
		Linked:           true,
		Status:           "ACTIVE",
		IsRewardCampaign: true,
		StartsAt:         now.Add(-time.Hour),
		EndsAt:           now.Add(time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:                 "reward:builder-cape-drop",
				Name:               "Builder Cape",
				StartsAt:           now.Add(-time.Hour),
				EndsAt:             now.Add(time.Hour),
				RequiredMinutes:    30,
				RealCurrentMinutes: 5,
				Benefits: []domain.Benefit{
					{ID: "benefit", Name: "Builder Cape", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{WatchStallMinutes: 10},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  2,
			Game:         &game,
			DropsEnabled: true,
		},
	}
	scheduler.channels = map[int64]domain.Channel{channel.ID: channel}
	scheduler.watchingChannelID = channel.ID
	scheduler.lastAdvanceAt = now.Add(-11 * time.Minute)

	if _, _, updated := scheduler.bumpActiveCampaign(now, &channel); !updated {
		t.Fatal("前置条件: 只靠本地计时的 reward 活动应发生本地补分")
	}

	scheduler.checkWatchStall(channel.ID, true)

	if scheduler.State() != StateIdle {
		t.Fatalf("本地计时的 reward 活动在本地推进后不应被判卡住: %s", scheduler.State())
	}
	if scheduler.channelStalled(channel.ID, now) {
		t.Fatal("本地推进的频道不应被回避")
	}
}

func TestNormalLocalBumpDoesNotDisarmStall(t *testing.T) {
	t.Parallel()

	now := testTime()
	scheduler, channel := newStallScheduler(t, now)
	scheduler.lastAdvanceAt = now.Add(-11 * time.Minute)

	if _, _, updated := scheduler.bumpActiveCampaign(now, &channel); !updated {
		t.Fatal("前置条件: 普通活动应发生本地补分")
	}

	scheduler.checkWatchStall(channel.ID, true)

	if scheduler.State() != StateChannelSwitch {
		t.Fatalf("普通活动的本地估算不是服务器进度, 不应阻止卡住判定: %s", scheduler.State())
	}
}

func TestCheckWatchStallResetsClockOnWatchSendFailure(t *testing.T) {
	t.Parallel()

	now := testTime()
	scheduler, channel := newStallScheduler(t, now)
	scheduler.lastAdvanceAt = now.Add(-11 * time.Minute)

	scheduler.checkWatchStall(channel.ID, false)

	if scheduler.State() != StateIdle {
		t.Fatalf("发送 watch 失败时不应把本地掉线判为频道卡住: %s", scheduler.State())
	}
	if scheduler.channelStalled(channel.ID, now) {
		t.Fatal("发送失败不应回避频道")
	}
	// 计时已被重置, 恢复后的第一轮成功发送不应立刻误判
	scheduler.checkWatchStall(channel.ID, true)
	if scheduler.State() != StateIdle {
		t.Fatalf("发送失败重置计时后, 恢复的第一轮不应立刻切台: %s", scheduler.State())
	}
}

func TestCheckWatchStallIgnoresNonWatchedChannel(t *testing.T) {
	t.Parallel()

	now := testTime()
	scheduler, channel := newStallScheduler(t, now)
	scheduler.lastAdvanceAt = now.Add(-time.Hour)

	scheduler.checkWatchStall(channel.ID+999, true)

	if scheduler.State() != StateIdle {
		t.Fatalf("非当前观看频道不应触发切台: %s", scheduler.State())
	}
	if scheduler.channelStalled(channel.ID, now) {
		t.Fatal("非当前观看频道不应被回避")
	}
}

func TestCheckWatchStallDisabledWhenThresholdZero(t *testing.T) {
	t.Parallel()

	now := testTime()
	scheduler, channel := newStallScheduler(t, now)
	scheduler.settings.WatchStallMinutes = 0
	scheduler.lastAdvanceAt = now.Add(-time.Hour)

	scheduler.checkWatchStall(channel.ID, true)

	if scheduler.State() != StateIdle {
		t.Fatalf("阈值为 0(禁用)时不应切台: %s", scheduler.State())
	}
	if scheduler.channelStalled(channel.ID, now) {
		t.Fatal("禁用时不应回避任何频道")
	}
}
