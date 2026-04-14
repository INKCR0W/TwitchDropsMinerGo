package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/pubsub"
)

type testSchedulerOptions struct {
	logger            *slog.Logger
	settings          config.Settings
	refresher         InventoryRefresher
	tracker           *fakeTracker
	pubsub            *fakePubSub
	gqlClient         GQLClient
	authState         AuthState
	now               func() time.Time
	sleep             func(context.Context, time.Duration) error
	watchInterval     time.Duration
	progressDelay     time.Duration
	maintenanceReload time.Duration
	claimSweepTimeout time.Duration
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
		Clock:             now,
		Sleep:             options.sleep,
		WatchInterval:     options.watchInterval,
		ProgressDelay:     options.progressDelay,
		MaintenanceReload: options.maintenanceReload,
		ClaimSweepTimeout: options.claimSweepTimeout,
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

type fakeRefresher struct {
	refreshFunc func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error)
}

func (f *fakeRefresher) Refresh(ctx context.Context, options inventory.RefreshOptions) (inventory.Snapshot, error) {
	if f.refreshFunc == nil {
		return inventory.Snapshot{}, nil
	}
	return f.refreshFunc(ctx, options)
}

type fakeTracker struct {
	mu                 sync.Mutex
	channels           map[int64]domain.Channel
	onChange           func(before, after domain.Channel)
	syncChannelsFunc   func(context.Context, []int64) error
	sendWatchFunc      func(context.Context, int64) (bool, error)
	sendCount          int
	configuredSettings config.Settings
	configuredSnapshot inventory.Snapshot
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{
		channels: make(map[int64]domain.Channel),
	}
}

func (f *fakeTracker) Configure(settings config.Settings, snapshot inventory.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configuredSettings = settings.Clone()
	f.configuredSnapshot = snapshot
}

func (f *fakeTracker) SetChannelChangeHandler(handler func(before, after domain.Channel)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onChange = handler
}

func (f *fakeTracker) AddChannel(channel domain.Channel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[channel.ID] = cloneChannel(channel)
}

func (f *fakeTracker) RemoveChannel(channelID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.channels, channelID)
}

func (f *fakeTracker) Channel(channelID int64) (domain.Channel, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	channel, ok := f.channels[channelID]
	return cloneChannel(channel), ok
}

func (f *fakeTracker) SyncChannels(ctx context.Context, channelIDs ...int64) error {
	if f.syncChannelsFunc == nil {
		return nil
	}
	return f.syncChannelsFunc(ctx, channelIDs)
}

func (f *fakeTracker) SendWatch(ctx context.Context, channelID int64) (bool, error) {
	f.mu.Lock()
	f.sendCount++
	sendWatchFunc := f.sendWatchFunc
	f.mu.Unlock()

	if sendWatchFunc == nil {
		return true, nil
	}
	return sendWatchFunc(ctx, channelID)
}

func (f *fakeTracker) ProcessStreamState(context.Context, int64, json.RawMessage) error {
	return nil
}

func (f *fakeTracker) ProcessStreamUpdate(context.Context, int64, json.RawMessage) error {
	return nil
}

func (f *fakeTracker) Close(context.Context) error {
	return nil
}

func (f *fakeTracker) applyChannel(channel domain.Channel) {
	f.mu.Lock()
	before := cloneChannel(f.channels[channel.ID])
	f.channels[channel.ID] = cloneChannel(channel)
	handler := f.onChange
	after := cloneChannel(channel)
	f.mu.Unlock()

	if handler != nil {
		handler(before, after)
	}
}

func (f *fakeTracker) snapshot() map[int64]domain.Channel {
	f.mu.Lock()
	defer f.mu.Unlock()

	cloned := make(map[int64]domain.Channel, len(f.channels))
	for channelID, channel := range f.channels {
		cloned[channelID] = cloneChannel(channel)
	}
	return cloned
}

func (f *fakeTracker) sendWatchCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendCount
}

type fakePubSub struct {
	mu      sync.Mutex
	started int
	added   []string
	removed []string
}

func (f *fakePubSub) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	return nil
}

func (f *fakePubSub) AddTopics(topics ...pubsub.Topic) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, topic := range topics {
		f.added = append(f.added, topic.Key())
	}
	return nil
}

func (f *fakePubSub) RemoveTopics(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, keys...)
}

func (f *fakePubSub) Stop(context.Context, bool) error {
	return nil
}

func (f *fakePubSub) addedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.added...)
}

type fakeGQLClient struct {
	doFunc func(context.Context, gql.Operation) (gql.Response, error)
}

func (f *fakeGQLClient) Do(ctx context.Context, operation gql.Operation) (gql.Response, error) {
	if f.doFunc == nil {
		return gql.Response{}, nil
	}
	return f.doFunc(ctx, operation)
}

type fakeAuthState struct {
	snapshot auth.Snapshot
}

func (f *fakeAuthState) Snapshot() auth.Snapshot {
	return f.snapshot
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

type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&logSyncWriter{b: b}, nil))
}

func (b *logBuffer) contains(substr string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Contains(b.buf.String(), substr)
}

type logSyncWriter struct {
	b *logBuffer
}

func (w *logSyncWriter) Write(p []byte) (int, error) {
	w.b.mu.Lock()
	defer w.b.mu.Unlock()
	return w.b.buf.Write(p)
}
