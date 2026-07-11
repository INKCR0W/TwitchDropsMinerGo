package scheduler

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/pubsub"
	"twitchdropsminergo/internal/rewards"
)

type fakeRefresher struct {
	refreshFunc     func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error)
	rewardProgress  map[string]rewards.Progress
	updateCallCount int
}

func (f *fakeRefresher) Refresh(ctx context.Context, options inventory.RefreshOptions) (inventory.Snapshot, error) {
	if f.refreshFunc == nil {
		return inventory.Snapshot{}, nil
	}
	return f.refreshFunc(ctx, options)
}

func (f *fakeRefresher) UpdateRewardProgress(progress map[string]rewards.Progress) {
	f.rewardProgress = progress
	f.updateCallCount++
}

type fakeTracker struct {
	mu               sync.Mutex
	channels         map[int64]domain.Channel
	onChange         func(before, after domain.Channel)
	syncChannelsFunc func(context.Context, []int64) error
	sendWatchFunc    func(context.Context, int64) (bool, error)
	sendCount        int
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{
		channels: make(map[int64]domain.Channel),
	}
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

type fakeRewardProgressStore struct {
	mu       sync.Mutex
	progress map[string]rewards.Progress
	records  []rewards.Progress
	err      error
}

func (f *fakeRewardProgressStore) Snapshot() map[string]rewards.Progress {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.progress) == 0 {
		return nil
	}
	cloned := make(map[string]rewards.Progress, len(f.progress))
	for key, value := range f.progress {
		cloned[key] = value
	}
	return cloned
}

func (f *fakeRewardProgressStore) RecordProgress(campaignID string, dropID string, minutesWatched int, completed bool, now time.Time) (rewards.Progress, error) {
	return f.recordProgress(campaignID, dropID, minutesWatched, completed, now, time.Time{})
}

func (f *fakeRewardProgressStore) RecordCompletion(campaignID string, dropID string, minutesWatched int, now time.Time, expiresAt time.Time) (rewards.Progress, error) {
	return f.recordProgress(campaignID, dropID, minutesWatched, true, now, expiresAt)
}

func (f *fakeRewardProgressStore) recordProgress(campaignID string, dropID string, minutesWatched int, completed bool, now time.Time, expiresAt time.Time) (rewards.Progress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return rewards.Progress{}, f.err
	}
	if f.progress == nil {
		f.progress = make(map[string]rewards.Progress)
	}
	progress := f.progress[campaignID]
	progress.CampaignID = campaignID
	progress.DropID = dropID
	if minutesWatched > progress.MinutesWatched {
		progress.MinutesWatched = minutesWatched
	}
	if completed && progress.CompletedAt.IsZero() {
		progress.CompletedAt = now.UTC()
	}
	if completed && !expiresAt.IsZero() {
		progress.ExpiresAt = expiresAt.UTC()
	}
	progress.UpdatedAt = now.UTC()
	f.progress[campaignID] = progress
	f.records = append(f.records, progress)
	return progress, nil
}

func (f *fakeRewardProgressStore) PruneExpired(now time.Time, gracePeriod time.Duration) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return 0, f.err
	}
	if gracePeriod < 0 {
		gracePeriod = 0
	}
	now = now.UTC()
	removed := 0
	for campaignID, progress := range f.progress {
		if progress.ExpiresAt.IsZero() || now.Before(progress.ExpiresAt.Add(gracePeriod)) {
			continue
		}
		delete(f.progress, campaignID)
		removed++
	}
	return removed, nil
}

func (f *fakeRewardProgressStore) lastRecord() (rewards.Progress, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.records) == 0 {
		return rewards.Progress{}, false
	}
	return f.records[len(f.records)-1], true
}
