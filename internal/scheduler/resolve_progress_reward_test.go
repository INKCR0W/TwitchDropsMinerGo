package scheduler

import (
	"context"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestResolveProgressCompletesRewardCampaignWithoutCurrentDrop(t *testing.T) {
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
		LinkURL:          "https://www.minecraft.net/redeem",
		Drops: []domain.TimedDropSpec{
			{
				ID:                  "reward:builder-cape-drop",
				Name:                "Builder Cape",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(time.Hour),
				RequiredMinutes:     5,
				ExtraCurrentMinutes: 4,
				Benefits: []domain.Benefit{
					{ID: "builder-cape-benefit", Name: "Builder Cape", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})
	progressStore := &fakeRewardProgressStore{}
	refresher := &fakeRefresher{}
	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher:      refresher,
		rewardProgress: progressStore,
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"dropCurrentSession": nil,
						},
					},
				}, nil
			},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "minecraft-channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: false,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("reward campaign 完成后应刷新 inventory: %s", scheduler.State())
	}
	record, ok := progressStore.lastRecord()
	if !ok {
		t.Fatal("reward campaign 完成后应写入本地完成状态")
	}
	if record.CampaignID != "reward:builder-cape" || record.DropID != "reward:builder-cape-drop" || record.MinutesWatched != 5 || record.CompletedAt.IsZero() {
		t.Fatalf("reward 完成记录不匹配: %#v", record)
	}
	if !record.ExpiresAt.Equal(campaign.EndsAt) {
		t.Fatalf("reward 完成记录应包含活动过期时间: %#v", record)
	}
	if drop := campaign.Drop("reward:builder-cape-drop"); drop == nil || !drop.IsClaimed {
		t.Fatalf("完成后的 reward drop 应在内存快照中标记为 claimed: %#v", drop)
	}
	if refresher.updateCallCount != 1 {
		t.Fatalf("reward 完成后应同步进度给 refresher: %d", refresher.updateCallCount)
	}
	if _, ok := refresher.rewardProgress["reward:builder-cape"]; !ok {
		t.Fatalf("refresher 未收到 reward 完成快照: %#v", refresher.rewardProgress)
	}
}

func TestResolveProgressRetriesRewardCompletionWhenPersistFails(t *testing.T) {
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
				ID:                  "reward:builder-cape-drop",
				Name:                "Builder Cape",
				StartsAt:            now.Add(-time.Hour),
				EndsAt:              now.Add(time.Hour),
				RequiredMinutes:     domain.MaxExtraMinutes,
				ExtraCurrentMinutes: domain.MaxExtraMinutes - 1,
				Benefits: []domain.Benefit{
					{ID: "builder-cape-benefit", Name: "Builder Cape", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})
	progressStore := &fakeRewardProgressStore{err: errAssert}
	scheduler := newTestScheduler(t, testSchedulerOptions{
		rewardProgress: progressStore,
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"dropCurrentSession": nil,
						},
					},
				}, nil
			},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "minecraft-channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: false,
		},
	}

	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() == StateInventoryFetch {
		t.Fatal("reward 完成状态保存失败时不应刷新 inventory 丢失本地进度")
	}
	if drop := campaign.Drop("reward:builder-cape-drop"); drop == nil || drop.IsClaimed {
		t.Fatalf("保存失败时不应把 reward drop 标记为 claimed: %#v", drop)
	}
	if _, ok := progressStore.lastRecord(); ok {
		t.Fatal("保存失败时不应记录成功完成态")
	}

	progressStore.err = nil
	scheduler.resolveProgress(context.Background(), channel, true)
	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("保存恢复后应刷新 inventory: %s", scheduler.State())
	}
	if record, ok := progressStore.lastRecord(); !ok || record.CampaignID != "reward:builder-cape" || record.MinutesWatched != domain.MaxExtraMinutes {
		t.Fatalf("保存恢复后应写入完成态: %#v ok=%v", record, ok)
	}
}
