package scheduler

import (
	"context"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/progress"
)

type fakeWatchProgress struct {
	entries []progress.Entry
	pruned  int
}

func (f *fakeWatchProgress) Snapshot() []progress.Entry {
	return f.entries
}

func (f *fakeWatchProgress) Record(campaignID string, dropID string, minutesWatched int, expiresAt time.Time, _ time.Time) error {
	f.entries = append(f.entries, progress.Entry{
		CampaignID:     campaignID,
		DropID:         dropID,
		MinutesWatched: minutesWatched,
		ExpiresAt:      expiresAt,
	})
	return nil
}

func (f *fakeWatchProgress) PruneExpired(time.Time) (int, error) {
	f.pruned++
	return 0, nil
}

func badgeMilestoneCampaign(t *testing.T, now time.Time) *domain.DropsCampaign {
	t.Helper()

	game := domain.Game{ID: domain.SpecialEventsGameID, Name: "Special Events"}
	return mustCampaign(t, domain.CampaignSpec{
		ID: "campaign-ewc", Name: "EWC 2026", Game: game, Linked: true, Status: "ACTIVE",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
		Drops: []domain.TimedDropSpec{
			{ID: "bronze", Name: "EWC Bronze", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), RequiredMinutes: 60,
				Benefits: []domain.Benefit{{ID: "bronze", Name: "Bronze", Type: domain.BenefitTypeBadge}}},
			{ID: "diamond", Name: "EWC 2026 (Diamond) Reward Group", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), RequiredMinutes: 720,
				Benefits: []domain.Benefit{{ID: "diamond", Name: "Diamond", Type: domain.BenefitTypeBadge}}},
		},
	})
}

func specialEventsChannel(id int64) domain.Channel {
	game := domain.Game{ID: domain.SpecialEventsGameID, Name: "Special Events"}
	return domain.Channel{ID: id, Login: "berbatow", Stream: &domain.Stream{BroadcastID: id * 10, Game: &game, DropsEnabled: true}}
}

func TestApplyDropProgressPersistsObservedMinutes(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := badgeMilestoneCampaign(t, now)
	store := &fakeWatchProgress{}
	scheduler := newTestScheduler(t, testSchedulerOptions{watchProgress: store})
	scheduler.settings.EnableBadgesEmotes = true
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	channel := specialEventsChannel(1)

	if !scheduler.applyDropProgress(now, &channel, "diamond", 1006) {
		t.Fatal("写入服务器进度应成功")
	}

	if len(store.entries) != 1 {
		t.Fatalf("服务器进度应落盘: %#v", store.entries)
	}
	entry := store.entries[0]
	if entry.CampaignID != "campaign-ewc" || entry.DropID != "diamond" || entry.MinutesWatched != 1006 {
		t.Fatalf("落盘内容不匹配: %#v", entry)
	}
	if entry.ExpiresAt.IsZero() {
		t.Fatal("落盘记录必须带到期时间, 否则永远清不掉")
	}
	if campaign.Drop("bronze").RealCurrentMinutes != 60 {
		t.Fatal("共享计数应同步到同窗口的其它 drop")
	}
}

func TestApplyDropProgressSkipsUnearnableDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := badgeMilestoneCampaign(t, now)
	store := &fakeWatchProgress{}
	scheduler := newTestScheduler(t, testSchedulerOptions{watchProgress: store})
	scheduler.settings.EnableBadgesEmotes = true
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	channel := specialEventsChannel(1)

	campaign.ObserveMinutes(campaign.Drop("diamond"), 1006)

	if scheduler.applyDropProgress(now, &channel, "diamond", 1010) {
		t.Fatal("已满进度的 drop 不应再接受进度写入")
	}
	if len(store.entries) != 0 {
		t.Fatalf("不可推进的 drop 不应落盘: %#v", store.entries)
	}
	if scheduler.applyDropProgress(now, &channel, "unknown-drop", 10) {
		t.Fatal("未知 drop 不应接受进度写入")
	}
}

// inventory 不回报 badge 活动的进度, 不回灌的话每次刷新都会重新去挂已完成的活动
func TestInventoryFetchSeedsObservedProgressAndKeepsFinishedCampaignOutOfPlanning(t *testing.T) {
	t.Parallel()

	now := testTime()
	campaign := badgeMilestoneCampaign(t, now)
	channel := specialEventsChannel(1)
	store := &fakeWatchProgress{
		entries: []progress.Entry{
			{CampaignID: "campaign-ewc", DropID: "diamond", MinutesWatched: 1006, ExpiresAt: now.Add(24 * time.Hour)},
		},
	}
	refresher := &fakeRefresher{
		refreshFunc: func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error) {
			return snapshotFromCampaigns(campaign), nil
		},
	}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher:     refresher,
		watchProgress: store,
		settings:      config.Settings{PriorityMode: config.EndingSoonest, EnableBadgesEmotes: true},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	if len(scheduler.computeWantedGames(now)) == 0 {
		t.Fatal("刚拉取的 inventory 里活动应是可推进的")
	}

	if err := scheduler.handleInventoryFetch(context.Background()); err != nil {
		t.Fatalf("handleInventoryFetch 返回错误: %v", err)
	}
	t.Cleanup(scheduler.cancelMaintenance)

	if store.pruned != 1 {
		t.Fatalf("每次刷新 inventory 都应清理过期的观看进度: %d", store.pruned)
	}
	if got := scheduler.computeWantedGames(now); len(got) != 0 {
		t.Fatalf("回灌后已完成的活动不应重新进入规划: %#v", got)
	}
	if scheduler.canWatch(channel) {
		t.Fatal("回灌后不应再去挂已完成活动的频道")
	}
	for _, dropID := range []string{"bronze", "diamond"} {
		if campaign.Drop(dropID).IsClaimed {
			t.Fatalf("回灌只写进度, 不应伪造 %s 的认领状态", dropID)
		}
	}
}

// 收口发生在还有其它可推进活动的频道上时, 不应把这个频道也丢掉
func TestResolveProgressKeepsChannelWhenAnotherCampaignStillEarnable(t *testing.T) {
	t.Parallel()

	now := testTime()
	badges := badgeMilestoneCampaign(t, now)
	siege := domain.Game{ID: 460630, Name: "Rainbow Six Siege"}
	channel := domain.Channel{
		ID: 1, Login: "esix_france", ACLBased: true,
		Stream: &domain.Stream{BroadcastID: 10, Game: &siege, DropsEnabled: true},
	}
	esports := mustCampaign(t, domain.CampaignSpec{
		ID: "campaign-r6s", Name: "R6S S1 2026 10", Game: siege, Linked: true, Status: "ACTIVE",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour),
		AllowedChannels: []domain.Channel{{ID: 1, Login: "esix_france", ACLBased: true}},
		Drops: []domain.TimedDropSpec{
			{ID: "pack", Name: "Esports Pack", StartsAt: now.Add(-time.Hour), EndsAt: now.Add(24 * time.Hour), RequiredMinutes: 720, RealCurrentMinutes: 300,
				Benefits: []domain.Benefit{{ID: "pack", Name: "Pack", Type: domain.BenefitTypeDirectEntitlement}}},
		},
	})

	scheduler := newTestScheduler(t, testSchedulerOptions{
		gqlClient:     currentDropGQLClient("diamond", 1006),
		watchProgress: &fakeWatchProgress{},
	})
	scheduler.settings.EnableBadgesEmotes = true
	scheduler.snapshot = snapshotFromCampaigns(badges, esports)
	scheduler.wantedGames = []domain.Game{siege, badges.Game}
	scheduler.channels = map[int64]domain.Channel{1: channel}

	scheduler.resolveProgress(context.Background(), channel, true)

	if badges.CanEarn(now, &channel, true, false) {
		t.Fatal("满进度的徽章活动应停止推进")
	}
	if scheduler.State() != StateIdle {
		t.Fatalf("频道上仍有可推进的活动, 不应切换状态: %s", scheduler.State())
	}
}

