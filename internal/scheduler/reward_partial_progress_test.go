package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/rewards"
)

func TestRewardPartialMinutesSurviveInventoryRefreshAndRestart(t *testing.T) {
	t.Parallel()
	now := testTime()
	path := filepath.Join(t.TempDir(), "rewards.json")
	store, err := rewards.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	refresher := &fakeRefresher{}
	refresher.refreshFunc = func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error) {
		campaign := partialRewardCampaign(t, now)
		campaign.Drop("reward-drop").RealCurrentMinutes = refresher.rewardProgress[campaign.ID].MinutesWatched
		return snapshotFromCampaigns(campaign), nil
	}
	s := newTestScheduler(t, testSchedulerOptions{refresher: refresher, rewardProgress: store, settings: config.Settings{PriorityMode: config.EndingSoonest}})
	t.Cleanup(func() { s.cancelMaintenance(); s.wg.Wait() })
	campaign := partialRewardCampaign(t, now)
	s.snapshot = snapshotFromCampaigns(campaign)
	s.wantedGames = []domain.Game{campaign.Game}
	channel := &domain.Channel{ID: 1, Stream: &domain.Stream{Game: &campaign.Game, BroadcastID: 1}}
	for minute := 1; minute <= 4; minute++ {
		s.bumpActiveCampaign(now.Add(time.Duration(minute)*time.Minute), channel)
	}
	if err := s.handleInventoryFetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s.snapshot.Drops["reward-drop"].CurrentMinutes(); got != 4 {
		t.Fatalf("refresh lost partial minutes: got=%d want=4", got)
	}
	reopened, err := rewards.NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	progress := reopened.Snapshot()[campaign.ID]
	if progress.MinutesWatched != 4 || !progress.CompletedAt.IsZero() || !progress.ExpiresAt.Equal(campaign.EndsAt) {
		t.Fatalf("partial progress did not persist with campaign expiry: %#v", progress)
	}
	completed, _, _ := s.bumpActiveCampaign(now.Add(5*time.Minute), channel)
	if !completed {
		t.Fatal("reward did not complete after refresh")
	}
}

func TestRewardRefreshPreservesUnsavedAndConcurrentMinutes(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"save-failure", "in-flight-progress", "refresh-failure", "changed-window", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			now := testTime()
			store := &fakeRewardProgressStore{}
			if scenario == "save-failure" {
				store.err = errAssert
			}
			refresher := &fakeRefresher{}
			s := newTestScheduler(t, testSchedulerOptions{refresher: refresher, rewardProgress: store, settings: config.Settings{PriorityMode: config.EndingSoonest}, now: func() time.Time { return now }})
			t.Cleanup(func() { s.cancelMaintenance(); s.wg.Wait() })
			campaign := partialRewardCampaign(t, now)
			s.snapshot = snapshotFromCampaigns(campaign)
			s.wantedGames = []domain.Game{campaign.Game}
			channel := &domain.Channel{ID: 1, Stream: &domain.Stream{Game: &campaign.Game, BroadcastID: 1}}
			for range 3 {
				s.bumpActiveCampaign(now, channel)
			}
			refresher.refreshFunc = func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error) {
				if scenario == "refresh-failure" {
					return inventory.Snapshot{}, errAssert
				}
				fresh := partialRewardCampaign(t, testTime())
				if scenario == "in-flight-progress" {
					s.bumpActiveCampaign(now, channel)
				}
				if scenario == "changed-window" {
					fresh.StartsAt = fresh.StartsAt.Add(time.Minute)
				}
				return snapshotFromCampaigns(fresh), nil
			}
			if scenario == "expired" {
				now = campaign.EndsAt
			}
			err := s.handleInventoryFetch(context.Background())
			want := 3
			switch scenario {
			case "refresh-failure":
				if err == nil {
					t.Fatal("expected refresh failure")
				}
			case "in-flight-progress":
				want = 4
			case "changed-window", "expired":
				want = 0
			}
			if scenario != "refresh-failure" && err != nil {
				t.Fatal(err)
			}
			if got := s.snapshot.Drops["reward-drop"].CurrentMinutes(); got != want {
				t.Fatalf("minutes after %s=%d want=%d", scenario, got, want)
			}
		})
	}
}

func partialRewardCampaign(t *testing.T, now time.Time) *domain.DropsCampaign {
	t.Helper()
	spec := campaignSpecWithDrop("reward-campaign", domain.Game{ID: 1, Name: "Game"}, now.Add(-time.Hour), now.Add(time.Hour), nil, domain.TimedDropSpec{ID: "reward-drop", RequiredMinutes: 5})
	spec.IsRewardCampaign = true
	return mustCampaign(t, spec)
}
