package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
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

func (s *Scheduler) getLiveStreams(ctx context.Context, game domain.Game, limit int, dropsEnabled bool) ([]domain.Channel, error) {
	filters := []any{}
	if dropsEnabled {
		filters = append(filters, "DROPS_ENABLED")
	}

	operation, err := gql.MustLookup(gql.OperationGameDirectory).WithVariables(map[string]any{
		"limit": limit,
		"slug":  game.Slug(),
		"options": map[string]any{
			"includeRestricted": []any{"SUB_ONLY_LIVE"},
			"systemFilters":     filters,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("构造 GameDirectory 请求失败: %w", err)
	}

	response, err := s.gqlClient.Do(ctx, operation)
	if err != nil {
		return nil, fmt.Errorf("请求 GameDirectory 失败: %w", err)
	}

	data, err := asMap(response.Data, "data")
	if err != nil {
		return nil, err
	}
	gameData := optionalMap(data["game"])
	if len(gameData) == 0 {
		return nil, nil
	}
	streamsData, err := mapFromMap(gameData, "streams")
	if err != nil {
		return nil, err
	}
	edges, err := sliceFromMap(streamsData, "edges")
	if err != nil {
		return nil, err
	}

	channels := make([]domain.Channel, 0, len(edges))
	for index, edgeValue := range edges {
		edgeData, err := asMap(edgeValue, fmt.Sprintf("edges[%d]", index))
		if err != nil {
			return nil, err
		}
		nodeData, err := mapFromMap(edgeData, "node")
		if err != nil {
			return nil, err
		}
		broadcaster := optionalMap(nodeData["broadcaster"])
		if len(broadcaster) == 0 {
			continue
		}

		channelID := int64Value(broadcaster, "id")
		login := stringValue(broadcaster, "login")
		if channelID <= 0 || login == "" {
			continue
		}

		channels = append(channels, domain.Channel{
			ID:          channelID,
			Login:       login,
			DisplayName: stringValue(broadcaster, "displayName"),
			Stream: &domain.Stream{
				BroadcastID:  int64Value(nodeData, "id"),
				Game:         parseGame(optionalMap(nodeData["game"])),
				Viewers:      int(int64Value(nodeData, "viewersCount")),
				Title:        stringValue(nodeData, "title"),
				DropsEnabled: dropsEnabled,
			},
		})
	}

	return channels, nil
}

func (s *Scheduler) canWatch(channel domain.Channel) bool {
	if !channel.Online() {
		return false
	}

	settings := s.settingsCopy()
	wantedGames := s.WantedGames()
	now := s.nowUTC()
	snapshot := s.snapshotCopy()

	for _, campaign := range snapshot.Inventory {
		if campaign == nil || !campaign.CanEarn(now, &channel, settings.EnableBadgesEmotes, false) {
			continue
		}
		game := channel.CurrentGame()
		if campaign.Game.IsSpecial() ||
			(game != nil && gameInList(*game, wantedGames) && (campaign.IsRewardCampaign || (channel.Stream != nil && channel.Stream.DropsEnabled))) {
			return true
		}
	}
	return false
}

func (s *Scheduler) shouldSwitch(channel domain.Channel) bool {
	watching := s.currentWatchingChannel()
	if watching == nil || !s.canWatch(*watching) {
		return true
	}

	channelOrder := s.priorityIndex(channel)
	watchingOrder := s.priorityIndex(*watching)
	return channelOrder < watchingOrder ||
		(channelOrder == watchingOrder && channel.ACLBased && !watching.ACLBased)
}

func (s *Scheduler) removeChannels(channelIDs []int64) {
	if len(channelIDs) == 0 {
		return
	}

	channelIDs = uniqueInt64s(channelIDs)
	topics := make([]string, 0, len(channelIDs)*2)

	s.mu.Lock()
	for _, channelID := range channelIDs {
		delete(s.channels, channelID)
		if s.selectedChannelID == channelID {
			s.selectedChannelID = 0
		}
		if s.watchingChannelID == channelID {
			s.watchingChannelID = 0
			s.lastProgressAt = time.Time{}
		}
		streamStateKey, _ := pubsub.TopicKey(pubsub.CategoryChannel, pubsub.TopicStreamState, channelID)
		streamUpdateKey, _ := pubsub.TopicKey(pubsub.CategoryChannel, pubsub.TopicStreamUpdate, channelID)
		topics = append(topics, streamStateKey, streamUpdateKey)
	}
	s.mu.Unlock()

	for _, channelID := range channelIDs {
		s.tracker.RemoveChannel(channelID)
	}
	s.pubsub.RemoveTopics(topics...)
	s.signalWatch()
}

func (s *Scheduler) upsertChannel(channel domain.Channel) {
	if channel.ID <= 0 {
		return
	}

	s.mu.Lock()
	s.channels[channel.ID] = cloneChannel(channel)
	s.mu.Unlock()
	s.tracker.AddChannel(channel)
}

func (s *Scheduler) selectedChannel() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selectedChannelID
}

func (s *Scheduler) currentWatchingChannel() *domain.Channel {
	s.mu.RLock()
	watchingChannelID := s.watchingChannelID
	channel, ok := s.channels[watchingChannelID]
	s.mu.RUnlock()

	if !ok || watchingChannelID == 0 {
		return nil
	}
	cloned := cloneChannel(channel)
	return &cloned
}

func (s *Scheduler) channel(channelID int64) (domain.Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	channel, ok := s.channels[channelID]
	if !ok {
		return domain.Channel{}, false
	}
	return cloneChannel(channel), true
}

func (s *Scheduler) channelsMapCopy() map[int64]domain.Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cloned := make(map[int64]domain.Channel, len(s.channels))
	for channelID, channel := range s.channels {
		cloned[channelID] = cloneChannel(channel)
	}
	return cloned
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
