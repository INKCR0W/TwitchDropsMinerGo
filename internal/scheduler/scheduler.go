package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/progress"
	"twitchdropsminergo/internal/pubsub"
	"twitchdropsminergo/internal/rewards"
)

const (
	DefaultWatchInterval     = 59 * time.Second
	DefaultProgressDelay     = 20 * time.Second
	DefaultMaintenanceReload = 20 * time.Minute
	DefaultErrorRetryDelay   = time.Minute
	DefaultClaimSweepTimeout = 30 * time.Second
	DefaultRewardPruneGrace  = 7 * 24 * time.Hour
	defaultDirectoryLimit    = 20
)

type State string

const (
	StateIdle            State = "IDLE"
	StateInventoryFetch  State = "INVENTORY_FETCH"
	StateGamesUpdate     State = "GAMES_UPDATE"
	StateChannelsCleanup State = "CHANNELS_CLEANUP"
	StateChannelsFetch   State = "CHANNELS_FETCH"
	StateChannelSwitch   State = "CHANNEL_SWITCH"
	StateExit            State = "EXIT"
)

type InventoryRefresher interface {
	Refresh(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error)
}

type WatchTracker interface {
	Configure(config.Settings, inventory.Snapshot)
	AddChannel(domain.Channel)
	RemoveChannel(int64)
	Channel(int64) (domain.Channel, bool)
	SyncChannels(context.Context, ...int64) error
	SendWatch(context.Context, int64) (bool, error)
	ProcessStreamState(context.Context, int64, json.RawMessage) error
	ProcessStreamUpdate(context.Context, int64, json.RawMessage) error
	Close(context.Context) error
}

type PubSubManager interface {
	Start(context.Context) error
	AddTopics(...pubsub.Topic) error
	RemoveTopics(...string)
	Stop(context.Context, bool) error
}

type GQLClient interface {
	Do(context.Context, gql.Operation) (gql.Response, error)
}

type AuthState interface {
	Snapshot() auth.Snapshot
}

type RewardProgressStore interface {
	Snapshot() map[string]rewards.Progress
	RecordProgress(campaignID string, dropID string, minutesWatched int, completed bool, now time.Time) (rewards.Progress, error)
	RecordCompletion(campaignID string, dropID string, minutesWatched int, now time.Time, expiresAt time.Time) (rewards.Progress, error)
	PruneExpired(now time.Time, gracePeriod time.Duration) (int, error)
}

type WatchProgressStore interface {
	Snapshot() []progress.Entry
	Record(campaignID string, dropID string, minutesWatched int, expiresAt time.Time, now time.Time) error
	PruneExpired(now time.Time) (int, error)
}

type rewardProgressAwareRefresher interface {
	UpdateRewardProgress(map[string]rewards.Progress)
}

type channelChangeRegistrar interface {
	SetChannelChangeHandler(func(before, after domain.Channel))
}

type Options struct {
	Logger            *slog.Logger
	Settings          config.Settings
	Refresher         InventoryRefresher
	Tracker           WatchTracker
	PubSub            PubSubManager
	GQLClient         GQLClient
	AuthState         AuthState
	RewardProgress    RewardProgressStore
	WatchProgress     WatchProgressStore
	Clock             func() time.Time
	Sleep             func(context.Context, time.Duration) error
	WatchInterval     time.Duration
	ProgressDelay     time.Duration
	MaintenanceReload time.Duration
	ErrorRetryDelay   time.Duration
	DirectoryLimit    int
	MaxChannels       int
	ClaimSweepTimeout time.Duration
	RewardPruneGrace  time.Duration
}

type StatusSnapshot struct {
	State                  State
	WantedGames            []domain.Game
	WatchingChannelID      int64
	SelectedChannelID      int64
	FullCleanup            bool
	LastProgressAt         time.Time
	Channels               []domain.Channel
	InventoryCampaignCount int
	InventoryDropCount     int
	UserTopicUserID        int64
	AuthenticatedUserID    int64
	PubSub                 pubsub.Status
}

type Scheduler struct {
	logger            *slog.Logger
	settings          config.Settings
	refresher         InventoryRefresher
	tracker           WatchTracker
	pubsub            PubSubManager
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
	directoryLimit    int
	maxChannels       int
	claimSweepTimeout time.Duration
	rewardPruneGrace  time.Duration

	mu                       sync.RWMutex
	state                    State
	snapshot                 inventory.Snapshot
	wantedGames              []domain.Game
	channels                 map[int64]domain.Channel
	fullCleanup              bool
	selectedChannelID        int64
	watchingChannelID        int64
	lastProgressAt           time.Time
	announcedProgressDropIDs map[string]struct{}
	lastRuntimeError         error
	maintenanceCancel        context.CancelFunc
	userTopicUserID          int64

	stateChanged chan struct{}
	watchSignal  chan struct{}
	wg           sync.WaitGroup
}

type dropEventMessage struct {
	Type string        `json:"type"`
	Data dropEventData `json:"data"`
}

type dropEventData struct {
	DropID              string `json:"drop_id"`
	CurrentProgressMin  int    `json:"current_progress_min"`
	RequiredProgressMin int    `json:"required_progress_min"`
	DropInstanceID      string `json:"drop_instance_id"`
}

type notificationEventMessage struct {
	Type string                `json:"type"`
	Data notificationEventData `json:"data"`
}

type notificationEventData struct {
	Notification notificationPayload `json:"notification"`
}

type notificationPayload struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type claimCandidate struct {
	CampaignID string
	DropID     string
	ClaimID    string
}

type claimSweepResult struct {
	Total    int
	Claimed  int
	Failed   int
	TimedOut bool
}

type pubsubStatusProvider interface {
	Status() pubsub.Status
}

func (s *Scheduler) snapshotCopy() inventory.Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cloned, err := cloneInventorySnapshot(s.snapshot)
	if err != nil {
		s.logger.Warn("复制 inventory 快照失败", "error", err)
		return inventory.Snapshot{}
	}
	return cloned
}

func (s *Scheduler) settingsCopy() config.Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings.Clone()
}

func (s *Scheduler) recordProgress(stamp time.Time) {
	s.mu.Lock()
	s.lastProgressAt = stamp.UTC()
	s.mu.Unlock()
}

func (s *Scheduler) nowUTC() time.Time {
	return s.now().UTC()
}
