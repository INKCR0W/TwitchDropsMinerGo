package scheduler

import (
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sort"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/pubsub"
)

func New(options Options) (*Scheduler, error) {
	if options.Refresher == nil {
		return nil, fmt.Errorf("scheduler inventory 刷新器不能为空")
	}
	if options.Tracker == nil {
		return nil, fmt.Errorf("scheduler watch 跟踪器不能为空")
	}
	if options.PubSub == nil {
		return nil, fmt.Errorf("scheduler PubSub 管理器不能为空")
	}
	if options.GQLClient == nil {
		return nil, fmt.Errorf("scheduler GQL 客户端不能为空")
	}
	if options.AuthState == nil {
		return nil, fmt.Errorf("scheduler 认证状态不能为空")
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	now := options.Clock
	if now == nil {
		now = time.Now
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	watchInterval := options.WatchInterval
	if watchInterval <= 0 {
		watchInterval = DefaultWatchInterval
	}

	progressDelay := options.ProgressDelay
	if progressDelay <= 0 {
		progressDelay = DefaultProgressDelay
	}

	maintenanceReload := options.MaintenanceReload
	if maintenanceReload <= 0 {
		maintenanceReload = DefaultMaintenanceReload
	}

	errorRetryDelay := options.ErrorRetryDelay
	if errorRetryDelay <= 0 {
		errorRetryDelay = DefaultErrorRetryDelay
	}

	directoryLimit := options.DirectoryLimit
	if directoryLimit <= 0 {
		directoryLimit = defaultDirectoryLimit
	}

	maxChannels := options.MaxChannels
	if maxChannels <= 0 {
		maxChannels = pubsub.MaxChannels
	}

	claimSweepTimeout := options.ClaimSweepTimeout
	if claimSweepTimeout <= 0 {
		claimSweepTimeout = DefaultClaimSweepTimeout
	}

	settings := options.Settings.Clone()
	if settings.IsZero() {
		settings = config.DefaultSettings()
	}

	scheduler := &Scheduler{
		logger:            logger,
		settings:          settings,
		refresher:         options.Refresher,
		tracker:           options.Tracker,
		pubsub:            options.PubSub,
		gqlClient:         options.GQLClient,
		authState:         options.AuthState,
		rewardProgress:    options.RewardProgress,
		now:               now,
		sleep:             sleep,
		watchInterval:     watchInterval,
		progressDelay:     progressDelay,
		maintenanceReload: maintenanceReload,
		errorRetryDelay:   errorRetryDelay,
		directoryLimit:    directoryLimit,
		maxChannels:       maxChannels,
		claimSweepTimeout: claimSweepTimeout,
		state:             StateIdle,
		channels:          make(map[int64]domain.Channel),
		stateChanged:      make(chan struct{}, 1),
		watchSignal:       make(chan struct{}, 1),
	}

	if registrar, ok := options.Tracker.(channelChangeRegistrar); ok {
		registrar.SetChannelChangeHandler(scheduler.onChannelChange)
	}

	return scheduler, nil
}

func (s *Scheduler) StatusSnapshot() StatusSnapshot {
	if s == nil {
		return StatusSnapshot{}
	}

	s.mu.RLock()
	status := StatusSnapshot{
		State:                  s.state,
		WantedGames:            append([]domain.Game(nil), s.wantedGames...),
		WatchingChannelID:      s.watchingChannelID,
		SelectedChannelID:      s.selectedChannelID,
		FullCleanup:            s.fullCleanup,
		LastProgressAt:         s.lastProgressAt,
		Channels:               make([]domain.Channel, 0, len(s.channels)),
		InventoryCampaignCount: len(s.snapshot.Campaigns),
		InventoryDropCount:     len(s.snapshot.Drops),
		UserTopicUserID:        s.userTopicUserID,
	}
	for _, channel := range s.channels {
		status.Channels = append(status.Channels, cloneChannel(channel))
	}
	s.mu.RUnlock()

	sort.Slice(status.Channels, func(i, j int) bool {
		return status.Channels[i].ID < status.Channels[j].ID
	})

	authSnapshot := s.authState.Snapshot()
	status.AuthenticatedUserID = authSnapshot.UserID

	if provider, ok := s.pubsub.(pubsubStatusProvider); ok {
		status.PubSub = provider.Status()
	}

	return status
}

func (s *Scheduler) Reload() {
	if s == nil {
		return
	}
	s.ChangeState(StateInventoryFetch)
}

func (s *Scheduler) UpdateSettings(settings config.Settings) error {
	if s == nil {
		return fmt.Errorf("scheduler 未初始化")
	}
	if err := settings.Validate(); err != nil {
		return err
	}

	cloned := settings.Clone()
	s.mu.Lock()
	s.settings = cloned
	s.mu.Unlock()

	trackerSnapshot := s.snapshotCopy()
	s.tracker.Configure(cloned, trackerSnapshot)
	s.Reload()
	return nil
}

func (s *Scheduler) State() State {
	if s == nil {
		return StateExit
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Scheduler) ChangeState(state State) {
	if s == nil {
		return
	}

	s.mu.Lock()
	previous := s.state
	if s.state != StateExit {
		s.state = state
	}
	current := s.state
	s.mu.Unlock()

	if previous != current {
		s.logger.Info("调度状态切换", "from", previous, "to", current)
	}
	s.signalStateChange()
}

func (s *Scheduler) SelectChannel(channelID int64) {
	if s == nil {
		return
	}

	s.mu.Lock()
	s.selectedChannelID = channelID
	s.mu.Unlock()
	s.ChangeState(StateChannelSwitch)
}

func (s *Scheduler) ClearSelectedChannel() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectedChannelID = 0
}

func (s *Scheduler) WantedGames() []domain.Game {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.wantedGames)
}

func (s *Scheduler) WatchingChannelID() int64 {
	if s == nil {
		return 0
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.watchingChannelID
}

func (s *Scheduler) Channels() []domain.Channel {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	channels := make([]domain.Channel, 0, len(s.channels))
	for _, channel := range s.channels {
		channels = append(channels, cloneChannel(channel))
	}
	return channels
}
