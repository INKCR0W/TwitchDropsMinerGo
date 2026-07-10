package scheduler

import (
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
)

func TestHandleChannelsCleanupRemovesOfflineAndUnwantedNonACLChannels(t *testing.T) {
	t.Parallel()

	now := testTime()
	wanted := domain.Game{ID: 1, Name: "Wanted"}
	other := domain.Game{ID: 2, Name: "Other"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelsCleanup
	scheduler.wantedGames = []domain.Game{wanted}
	scheduler.channels = map[int64]domain.Channel{
		1: {ID: 1, Login: "offline"},
		2: {
			ID:    2,
			Login: "other",
			Stream: &domain.Stream{
				BroadcastID:  22,
				Game:         &other,
				DropsEnabled: true,
			},
		},
		3: {
			ID:       3,
			Login:    "acl",
			ACLBased: true,
			Stream: &domain.Stream{
				BroadcastID:  33,
				Game:         &other,
				DropsEnabled: true,
			},
		},
		4: {
			ID:    4,
			Login: "wanted",
			Stream: &domain.Stream{
				BroadcastID:  44,
				Game:         &wanted,
				DropsEnabled: true,
			},
		},
	}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-wanted", wanted, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)

	scheduler.handleChannelsCleanup()

	if _, ok := scheduler.channels[1]; ok {
		t.Fatal("离线非 ACL 频道应被清理")
	}
	if _, ok := scheduler.channels[2]; ok {
		t.Fatal("不再想看的非 ACL 频道应被清理")
	}
	if _, ok := scheduler.channels[3]; !ok {
		t.Fatal("ACL 频道不应在增量 cleanup 中被移除")
	}
	if _, ok := scheduler.channels[4]; !ok {
		t.Fatal("仍然可看的频道不应被移除")
	}
	if scheduler.State() != StateChannelsFetch {
		t.Fatalf("cleanup 后应进入 CHANNELS_FETCH: %s", scheduler.State())
	}
}
