package scheduler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/inventory"
)

type testSchedulerOptions struct {
	logger            *slog.Logger
	settings          config.Settings
	refresher         InventoryRefresher
	tracker           *fakeTracker
	pubsub            *fakePubSub
	gqlClient         GQLClient
	authState         AuthState
	rewardProgress    RewardProgressStore
	watchProgress     WatchProgressStore
	now               func() time.Time
	sleep             func(context.Context, time.Duration) error
	watchInterval     time.Duration
	progressDelay     time.Duration
	maintenanceReload time.Duration
	errorRetryDelay   time.Duration
	claimSweepTimeout time.Duration
	rewardPruneGrace  time.Duration
}

func newTestScheduler(t *testing.T, options testSchedulerOptions) *Scheduler {
	t.Helper()

	refresher := options.refresher
	if refresher == nil {
		refresher = &fakeRefresher{}
	}

	tracker := options.tracker
	if tracker == nil {
		tracker = newFakeTracker()
	}

	pubsubManager := options.pubsub
	if pubsubManager == nil {
		pubsubManager = &fakePubSub{}
	}

	gqlClient := options.gqlClient
	if gqlClient == nil {
		gqlClient = &fakeGQLClient{}
	}

	authState := options.authState
	if authState == nil {
		authState = &fakeAuthState{snapshot: auth.Snapshot{UserID: 1}}
	}

	now := options.now
	if now == nil {
		now = testTime
	}

	scheduler, err := New(Options{
		Logger:            options.logger,
		Settings:          options.settings,
		Refresher:         refresher,
		Tracker:           tracker,
		PubSub:            pubsubManager,
		GQLClient:         gqlClient,
		AuthState:         authState,
		RewardProgress:    options.rewardProgress,
		WatchProgress:     options.watchProgress,
		Clock:             now,
		Sleep:             options.sleep,
		WatchInterval:     options.watchInterval,
		ProgressDelay:     options.progressDelay,
		MaintenanceReload: options.maintenanceReload,
		ErrorRetryDelay:   options.errorRetryDelay,
		ClaimSweepTimeout: options.claimSweepTimeout,
		RewardPruneGrace:  options.rewardPruneGrace,
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	return scheduler
}

func trackerPubSubKeys(s *Scheduler) []string {
	fake, ok := s.pubsub.(*fakePubSub)
	if !ok {
		return nil
	}
	return fake.addedKeys()
}

func mustCampaign(t *testing.T, spec domain.CampaignSpec) *domain.DropsCampaign {
	t.Helper()
	campaign, err := domain.NewCampaign(spec)
	if err != nil {
		t.Fatalf("NewCampaign 返回错误: %v", err)
	}
	return campaign
}

func snapshotFromCampaigns(campaigns ...*domain.DropsCampaign) inventory.Snapshot {
	snapshot := inventory.Snapshot{
		Inventory: make([]*domain.DropsCampaign, 0, len(campaigns)),
		Campaigns: make(map[string]*domain.DropsCampaign, len(campaigns)),
		Drops:     make(map[string]*domain.TimedDrop),
	}
	for _, campaign := range campaigns {
		if campaign == nil {
			continue
		}
		snapshot.Inventory = append(snapshot.Inventory, campaign)
		snapshot.Campaigns[campaign.ID] = campaign
		for _, drop := range campaign.Drops() {
			snapshot.Drops[drop.ID] = drop
		}
	}
	return snapshot
}

func campaignSpec(_ time.Time, id string, game domain.Game, startsAt time.Time, endsAt time.Time, allowed []domain.Channel) domain.CampaignSpec {
	return campaignSpecWithDrop(id, game, startsAt, endsAt, allowed, domain.TimedDropSpec{})
}

func campaignSpecWithDrop(id string, game domain.Game, startsAt time.Time, endsAt time.Time, allowed []domain.Channel, drop domain.TimedDropSpec) domain.CampaignSpec {
	if drop.ID == "" {
		drop.ID = id + "-drop"
	}
	if drop.Name == "" {
		drop.Name = id + "-drop"
	}
	if drop.StartsAt.IsZero() {
		drop.StartsAt = startsAt
	}
	if drop.EndsAt.IsZero() {
		drop.EndsAt = endsAt
	}
	if drop.RequiredMinutes == 0 {
		drop.RequiredMinutes = 30
	}
	if len(drop.Benefits) == 0 {
		drop.Benefits = []domain.Benefit{
			{ID: id + "-benefit", Name: id + "-reward", Type: domain.BenefitTypeDirectEntitlement},
		}
	}

	return domain.CampaignSpec{
		ID:              id,
		Name:            id,
		Game:            game,
		Linked:          true,
		Status:          "ACTIVE",
		StartsAt:        startsAt,
		EndsAt:          endsAt,
		AllowedChannels: allowed,
		Drops:           []domain.TimedDropSpec{drop},
	}
}

func testTime() time.Time {
	return time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)
}
