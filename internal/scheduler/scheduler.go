package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/pubsub"
)

const (
	DefaultWatchInterval     = 59 * time.Second
	DefaultProgressDelay     = 20 * time.Second
	DefaultMaintenanceReload = time.Hour
	defaultDirectoryLimit    = 20
)

var errWatchInterrupted = errors.New("watch 循环被中断")

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
	Clock             func() time.Time
	Sleep             func(context.Context, time.Duration) error
	WatchInterval     time.Duration
	ProgressDelay     time.Duration
	MaintenanceReload time.Duration
	DirectoryLimit    int
	MaxChannels       int
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
	now               func() time.Time
	sleep             func(context.Context, time.Duration) error
	watchInterval     time.Duration
	progressDelay     time.Duration
	maintenanceReload time.Duration
	directoryLimit    int
	maxChannels       int

	mu                sync.RWMutex
	state             State
	snapshot          inventory.Snapshot
	wantedGames       []domain.Game
	channels          map[int64]domain.Channel
	fullCleanup       bool
	selectedChannelID int64
	watchingChannelID int64
	lastProgressAt    time.Time
	maintenanceCancel context.CancelFunc
	userTopicUserID   int64

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

type pubsubStatusProvider interface {
	Status() pubsub.Status
}

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

	directoryLimit := options.DirectoryLimit
	if directoryLimit <= 0 {
		directoryLimit = defaultDirectoryLimit
	}

	maxChannels := options.MaxChannels
	if maxChannels <= 0 {
		maxChannels = pubsub.MaxChannels
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
		now:               now,
		sleep:             sleep,
		watchInterval:     watchInterval,
		progressDelay:     progressDelay,
		maintenanceReload: maintenanceReload,
		directoryLimit:    directoryLimit,
		maxChannels:       maxChannels,
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

func (s *Scheduler) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("scheduler 未初始化")
	}
	if ctx == nil {
		return fmt.Errorf("scheduler 运行上下文不能为空")
	}

	watchCtx, cancelWatch := context.WithCancel(ctx)
	s.wg.Add(1)
	go s.watchLoop(watchCtx)

	defer func() {
		cancelWatch()
		s.cancelMaintenance()
		s.clearStateChange()
		s.signalWatch()
		s.wg.Wait()
		if err := s.pubsub.Stop(context.Background(), true); err != nil {
			s.logger.Warn("停止 PubSub 失败", "error", err)
		}
		if err := s.tracker.Close(context.Background()); err != nil {
			s.logger.Warn("关闭 watch 跟踪器失败", "error", err)
		}
	}()

	s.ChangeState(StateInventoryFetch)
	for {
		if err := ctx.Err(); err != nil {
			s.ChangeState(StateExit)
		}

		switch s.State() {
		case StateIdle:
			s.handleIdle()
		case StateInventoryFetch:
			if err := s.handleInventoryFetch(ctx); err != nil {
				return err
			}
		case StateGamesUpdate:
			if err := s.handleGamesUpdate(ctx); err != nil {
				return err
			}
		case StateChannelsCleanup:
			s.handleChannelsCleanup()
		case StateChannelsFetch:
			if err := s.handleChannelsFetch(ctx); err != nil {
				return err
			}
		case StateChannelSwitch:
			s.handleChannelSwitch()
		case StateExit:
			return nil
		default:
			return fmt.Errorf("未知 scheduler 状态: %s", s.State())
		}

		if s.State() == StateExit {
			return nil
		}
		if err := s.waitForStateChange(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				s.ChangeState(StateExit)
				continue
			}
			return err
		}
	}
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

func (s *Scheduler) ActiveCampaign(channel *domain.Channel) *domain.DropsCampaign {
	if s == nil {
		return nil
	}

	now := s.nowUTC()
	settings := s.settingsCopy()
	snapshot := s.snapshotCopy()

	if channel == nil {
		watching := s.currentWatchingChannel()
		if watching == nil {
			return nil
		}
		channel = watching
	}

	var selected *domain.DropsCampaign
	for _, campaign := range snapshot.Inventory {
		if campaign == nil || !campaign.CanEarn(now, channel, settings.EnableBadgesEmotes, false) {
			continue
		}
		if selected == nil ||
			campaign.RemainingMinutes() < selected.RemainingMinutes() ||
			(campaign.RemainingMinutes() == selected.RemainingMinutes() && campaign.ID < selected.ID) {
			selected = campaign
		}
	}
	return selected
}

func (s *Scheduler) handleIdle() {
	s.stopWatching()
	s.clearStateChange()
}

func (s *Scheduler) handleInventoryFetch(ctx context.Context) error {
	s.logger.Info("开始刷新 inventory")

	if err := s.pubsub.Start(ctx); err != nil {
		return fmt.Errorf("启动 PubSub 失败: %w", err)
	}
	s.logger.Info("PubSub 已启动，开始校验认证并拉取 inventory")

	snapshot, err := s.refresher.Refresh(ctx, inventory.RefreshOptions{
		EnableBadgesEmotes: s.settingsCopy().EnableBadgesEmotes,
	})
	if err != nil {
		return fmt.Errorf("刷新 inventory 失败: %w", err)
	}
	s.logger.Info(
		"inventory 刷新完成",
		"campaign_count", len(snapshot.Campaigns),
		"drop_count", len(snapshot.Drops),
		"maintenance_trigger_count", len(snapshot.MaintenanceTriggers),
	)

	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()
	trackerSnapshot, err := cloneInventorySnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("复制 tracker inventory 快照失败: %w", err)
	}
	s.tracker.Configure(s.settingsCopy(), trackerSnapshot)
	if err := s.ensureUserTopics(); err != nil {
		return fmt.Errorf("订阅用户 PubSub topic 失败: %w", err)
	}
	s.logger.Info("inventory 已装载，准备进入游戏筛选阶段")
	s.restartMaintenance(ctx, snapshot.MaintenanceTriggers)
	s.ChangeState(StateGamesUpdate)
	return nil
}

func (s *Scheduler) handleGamesUpdate(ctx context.Context) error {
	now := s.nowUTC()
	s.claimReadyDrops(ctx, now)

	s.mu.Lock()
	s.wantedGames = s.computeWantedGames(now)
	s.fullCleanup = true
	s.mu.Unlock()

	s.restartWatching()
	s.ChangeState(StateChannelsCleanup)
	return nil
}

func (s *Scheduler) handleChannelsCleanup() {
	s.mu.RLock()
	fullCleanup := s.fullCleanup
	wantedGames := slices.Clone(s.wantedGames)
	channels := make([]domain.Channel, 0, len(s.channels))
	for _, channel := range s.channels {
		channels = append(channels, cloneChannel(channel))
	}
	s.mu.RUnlock()

	toRemove := make([]int64, 0)
	if len(wantedGames) == 0 || fullCleanup {
		for _, channel := range channels {
			toRemove = append(toRemove, channel.ID)
		}
	} else {
		for _, channel := range channels {
			if channel.ACLBased {
				continue
			}
			if channel.Offline() {
				toRemove = append(toRemove, channel.ID)
				continue
			}
			game := channel.CurrentGame()
			if game == nil || !gameInList(*game, wantedGames) {
				toRemove = append(toRemove, channel.ID)
			}
		}
	}

	s.removeChannels(toRemove)

	s.mu.Lock()
	s.fullCleanup = false
	s.mu.Unlock()

	if len(wantedGames) == 0 {
		s.ChangeState(StateIdle)
		return
	}
	s.ChangeState(StateChannelsFetch)
}

func (s *Scheduler) handleChannelsFetch(ctx context.Context) error {
	now := s.nowUTC()
	settings := s.settingsCopy()
	snapshot := s.snapshotCopy()
	wantedGames := s.WantedGames()

	existing := s.channelsMapCopy()
	newChannels := make(map[int64]domain.Channel, len(existing))
	for channelID, channel := range existing {
		newChannels[channelID] = channel
	}

	nextHour := now.Add(time.Hour)
	aclChannels := make(map[int64]domain.Channel)
	noACLGames := make(map[string]domain.Game)

	for _, campaign := range snapshot.Inventory {
		if campaign == nil ||
			!gameInList(campaign.Game, wantedGames) ||
			!campaign.CanEarnWithin(now, nextHour, settings.EnableBadgesEmotes) {
			continue
		}

		if len(campaign.AllowedChannels) > 0 {
			for _, channel := range campaign.AllowedChannels {
				if _, exists := newChannels[channel.ID]; exists {
					continue
				}
				aclChannels[channel.ID] = channel
			}
			continue
		}
		noACLGames[gameKey(campaign.Game)] = campaign.Game
	}

	if len(aclChannels) > 0 {
		ids := make([]int64, 0, len(aclChannels))
		for _, channel := range aclChannels {
			s.upsertChannel(channel)
			newChannels[channel.ID] = cloneChannel(channel)
			ids = append(ids, channel.ID)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if err := s.tracker.SyncChannels(ctx, ids...); err != nil {
			return fmt.Errorf("批量同步 ACL 频道失败: %w", err)
		}
		for _, channelID := range ids {
			channel, ok := s.tracker.Channel(channelID)
			if !ok {
				continue
			}
			newChannels[channelID] = channel
		}
	}

	games := make([]domain.Game, 0, len(noACLGames))
	for _, game := range noACLGames {
		games = append(games, game)
	}
	sort.Slice(games, func(i, j int) bool {
		return s.priorityIndexByGame(games[i], wantedGames) < s.priorityIndexByGame(games[j], wantedGames)
	})
	for _, game := range games {
		channels, err := s.getLiveStreams(ctx, game, s.directoryLimit, true)
		if err != nil {
			return err
		}
		for _, channel := range channels {
			s.upsertChannel(channel)
			newChannels[channel.ID] = channel
		}
	}

	ordered := make([]domain.Channel, 0, len(newChannels))
	for _, channel := range newChannels {
		ordered = append(ordered, channel)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return viewerSortKey(ordered[i]) > viewerSortKey(ordered[j])
	})
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ACLBased && !ordered[j].ACLBased
	})
	sort.SliceStable(ordered, func(i, j int) bool {
		return s.priorityIndex(ordered[i]) < s.priorityIndex(ordered[j])
	})

	limit := min(len(ordered), s.maxChannels)
	desired := make(map[int64]domain.Channel, limit)
	for _, channel := range ordered[:limit] {
		desired[channel.ID] = channel
	}

	toRemove := make([]int64, 0)
	for channelID := range existing {
		if _, keep := desired[channelID]; keep {
			continue
		}
		toRemove = append(toRemove, channelID)
	}
	for _, channel := range ordered[limit:] {
		if _, existed := existing[channel.ID]; existed {
			continue
		}
		toRemove = append(toRemove, channel.ID)
	}
	s.removeChannels(toRemove)

	for _, channel := range ordered[:limit] {
		s.upsertChannel(channel)
	}

	topics := make([]pubsub.Topic, 0, len(desired)*2)
	for channelID := range desired {
		streamState, err := pubsub.ChannelTopic(pubsub.TopicStreamState, channelID, s.handleStreamState)
		if err != nil {
			return err
		}
		streamUpdate, err := pubsub.ChannelTopic(pubsub.TopicStreamUpdate, channelID, s.handleStreamUpdate)
		if err != nil {
			return err
		}
		topics = append(topics, streamState, streamUpdate)
	}
	if err := s.pubsub.AddTopics(topics...); err != nil {
		return fmt.Errorf("订阅频道 PubSub topic 失败: %w", err)
	}

	watchingChannel := s.currentWatchingChannel()
	if watchingChannel != nil {
		if refreshed, ok := s.channel(watchingChannel.ID); ok && s.canWatch(refreshed) {
			s.watch(refreshed.ID)
		} else {
			s.stopWatching()
		}
	}

	s.ChangeState(StateChannelSwitch)
	return nil
}

func (s *Scheduler) handleChannelSwitch() {
	var selected *domain.Channel
	if channelID := s.selectedChannel(); channelID > 0 {
		if channel, ok := s.channel(channelID); ok {
			selected = &channel
		}
	}

	if selected != nil && s.canWatch(*selected) {
		s.watch(selected.ID)
		s.clearStateChange()
		return
	}

	channels := s.channelsSliceSortedByPriority()
	var newWatching *domain.Channel
	for _, channel := range channels {
		channel := channel
		if s.canWatch(channel) && s.shouldSwitch(channel) {
			newWatching = &channel
			break
		}
	}

	if newWatching != nil {
		s.watch(newWatching.ID)
		s.clearStateChange()
		return
	}

	if watching := s.currentWatchingChannel(); watching != nil && s.canWatch(*watching) {
		s.clearStateChange()
		return
	}

	s.ChangeState(StateIdle)
}

func (s *Scheduler) handleStreamState(ctx context.Context, event pubsub.Event) error {
	return s.tracker.ProcessStreamState(ctx, event.Topic.TargetID(), event.Message)
}

func (s *Scheduler) handleStreamUpdate(ctx context.Context, event pubsub.Event) error {
	return s.tracker.ProcessStreamUpdate(ctx, event.Topic.TargetID(), event.Message)
}

func (s *Scheduler) handleDropEvent(ctx context.Context, event pubsub.Event) error {
	var message dropEventMessage
	if err := json.Unmarshal(event.Message, &message); err != nil {
		return fmt.Errorf("解析掉宝 PubSub 事件失败: %w", err)
	}

	switch message.Type {
	case "drop-progress":
		s.processDropProgress(message)
	case "drop-claim":
		return s.processDropClaim(ctx, message)
	}

	return nil
}

func (s *Scheduler) handleNotificationEvent(ctx context.Context, event pubsub.Event) error {
	var message notificationEventMessage
	if err := json.Unmarshal(event.Message, &message); err != nil {
		return fmt.Errorf("解析通知 PubSub 事件失败: %w", err)
	}

	if message.Type != "create-notification" {
		return nil
	}

	notification := message.Data.Notification
	switch notification.Type {
	case "user_drop_reward_reminder_notification", "quests_viewer_reward_campaign_earned_emote":
		s.ChangeState(StateInventoryFetch)
		if strings.TrimSpace(notification.ID) == "" {
			s.logger.Warn("奖励通知缺少 id，跳过删除", "type", notification.Type)
			return nil
		}
		if err := s.deleteNotification(ctx, notification.ID); err != nil {
			return fmt.Errorf("删除奖励通知失败: %w", err)
		}
	}

	return nil
}

func (s *Scheduler) onChannelChange(before, after domain.Channel) {
	if s == nil || after.ID <= 0 {
		return
	}

	s.mu.Lock()
	if _, exists := s.channels[after.ID]; exists {
		s.channels[after.ID] = cloneChannel(after)
	}
	watchingChannelID := s.watchingChannelID
	state := s.state
	s.mu.Unlock()

	if state == StateInventoryFetch || state == StateGamesUpdate || state == StateChannelsCleanup || state == StateChannelsFetch || state == StateExit {
		return
	}

	if !before.Online() {
		if after.Online() && s.canWatch(after) && s.shouldSwitch(after) {
			s.watch(after.ID)
		}
		return
	}

	if watchingChannelID != 0 && watchingChannelID == after.ID {
		if !s.canWatch(after) {
			s.ChangeState(StateChannelSwitch)
		}
		return
	}

	if after.Online() && s.canWatch(after) && s.shouldSwitch(after) {
		s.watch(after.ID)
	}
}

func (s *Scheduler) claimReadyDrops(ctx context.Context, now time.Time) {
	for _, candidate := range s.readyDrops(now) {
		ok, err := s.claimDropRequest(ctx, candidate.ClaimID)
		if err != nil {
			s.logger.Warn("认领掉宝失败", "campaign_id", candidate.CampaignID, "drop_id", candidate.DropID, "error", err)
			continue
		}
		if ok {
			s.markDropClaimed(candidate.DropID)
			s.logger.Info("认领掉宝成功", "campaign_id", candidate.CampaignID, "drop_id", candidate.DropID)
		}
	}
}

func (s *Scheduler) claimDropRequest(ctx context.Context, claimID string) (bool, error) {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return false, nil
	}

	operation, err := gql.MustLookup(gql.OperationClaimDrop).WithVariables(map[string]any{
		"input": map[string]any{
			"dropInstanceID": claimID,
		},
	})
	if err != nil {
		return false, fmt.Errorf("构造 ClaimDrop 请求失败: %w", err)
	}

	response, err := s.gqlClient.Do(ctx, operation)
	if err != nil {
		return false, err
	}

	data, err := asMap(response.Data, "data")
	if err != nil {
		return false, err
	}
	claimData := optionalMap(data["claimDropRewards"])
	switch stringValue(claimData, "status") {
	case "ELIGIBLE_FOR_ALL", "DROP_INSTANCE_ALREADY_CLAIMED":
		return true, nil
	default:
		return false, nil
	}
}

func (s *Scheduler) computeWantedGames(now time.Time) []domain.Game {
	settings := s.settingsCopy()
	snapshot := s.snapshotCopy()
	nextHour := now.Add(time.Hour)

	campaigns := append([]*domain.DropsCampaign(nil), snapshot.Inventory...)
	if settings.PriorityMode != config.PriorityOnly {
		switch settings.PriorityMode {
		case config.EndingSoonest:
			sort.SliceStable(campaigns, func(i, j int) bool {
				return campaigns[i].EndsAt.Before(campaigns[j].EndsAt)
			})
		case config.LowAvailabilityFirst:
			sort.SliceStable(campaigns, func(i, j int) bool {
				return campaigns[i].Availability(now) < campaigns[j].Availability(now)
			})
		}
	}
	sort.SliceStable(campaigns, func(i, j int) bool {
		return priorityNameIndex(campaigns[i].Game.Name, settings.Priority) < priorityNameIndex(campaigns[j].Game.Name, settings.Priority)
	})

	wanted := make([]domain.Game, 0)
	for _, campaign := range campaigns {
		if campaign == nil {
			continue
		}
		game := campaign.Game
		if gameInList(game, wanted) ||
			stringInList(game.Name, settings.Exclude) ||
			(settings.PriorityMode == config.PriorityOnly && !stringInList(game.Name, settings.Priority)) ||
			!campaign.CanEarnWithin(now, nextHour, settings.EnableBadgesEmotes) {
			continue
		}
		wanted = append(wanted, game)
	}
	return wanted
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
		if (game != nil && channel.Stream != nil && channel.Stream.DropsEnabled && gameInList(*game, wantedGames)) ||
			campaign.Game.IsSpecialEvents() {
			return true
		}
	}
	return false
}

func (s *Scheduler) shouldSwitch(channel domain.Channel) bool {
	watching := s.currentWatchingChannel()
	if watching == nil {
		return true
	}

	channelOrder := s.priorityIndex(channel)
	watchingOrder := s.priorityIndex(*watching)
	return channelOrder < watchingOrder ||
		(channelOrder == watchingOrder && channel.ACLBased && !watching.ACLBased)
}

func (s *Scheduler) watch(channelID int64) {
	s.mu.Lock()
	changed := s.watchingChannelID != channelID
	s.watchingChannelID = channelID
	s.mu.Unlock()

	if changed {
		s.signalWatch()
	}
}

func (s *Scheduler) stopWatching() {
	s.mu.Lock()
	changed := s.watchingChannelID != 0 || !s.lastProgressAt.IsZero()
	s.watchingChannelID = 0
	s.lastProgressAt = time.Time{}
	s.mu.Unlock()

	if changed {
		s.signalWatch()
	}
}

func (s *Scheduler) restartWatching() {
	s.signalWatch()
}

func (s *Scheduler) watchLoop(ctx context.Context) {
	defer s.wg.Done()

	for {
		channelID, ok := s.waitForWatchingChannel(ctx)
		if !ok {
			return
		}

		channel, exists := s.channel(channelID)
		if !exists || !channel.Online() {
			s.stopWatching()
			continue
		}

		sentAt := s.nowUTC()
		succeeded, err := s.tracker.SendWatch(ctx, channelID)
		if err != nil {
			s.logger.Warn("发送 watch 请求失败", "channel_id", channelID, "error", err)
		} else if !succeeded {
			s.logger.Warn("watch 请求未成功", "channel_id", channelID)
		}

		if err := s.sleepWithWatchSignal(ctx, s.progressDelay); err != nil {
			if errors.Is(err, errWatchInterrupted) {
				continue
			}
			return
		}

		if s.shouldResolveProgress(sentAt) {
			if err := s.resolveProgress(ctx, channel); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Warn("处理 watch 进度失败", "channel_id", channelID, "error", err)
			}
		}

		elapsed := s.nowUTC().Sub(sentAt)
		if elapsed > s.watchInterval {
			elapsed = s.watchInterval
		}
		if err := s.sleepWithWatchSignal(ctx, s.watchInterval-elapsed); err != nil {
			if errors.Is(err, errWatchInterrupted) {
				continue
			}
			return
		}
	}
}

func (s *Scheduler) resolveProgress(ctx context.Context, channel domain.Channel) error {
	now := s.nowUTC()

	if dropID, currentMinutes, ok, err := s.fetchCurrentDrop(ctx, channel.ID); err != nil {
		s.logger.Warn("CurrentDrop 查询失败，回退本地补分钟", "channel_id", channel.ID, "error", err)
	} else if ok && s.applyDropProgress(now, &channel, dropID, currentMinutes) {
		return nil
	}

	reachedLimit, updated := s.bumpActiveCampaign(now, &channel)
	if !updated {
		return nil
	}
	if reachedLimit {
		s.ChangeState(StateChannelSwitch)
	}
	return nil
}

func (s *Scheduler) fetchCurrentDrop(ctx context.Context, channelID int64) (string, int, bool, error) {
	operation, err := gql.MustLookup(gql.OperationCurrentDrop).WithVariables(map[string]any{
		"channelID": strconv.FormatInt(channelID, 10),
	})
	if err != nil {
		return "", 0, false, fmt.Errorf("构造 CurrentDrop 请求失败: %w", err)
	}

	response, err := s.gqlClient.Do(ctx, operation)
	if err != nil {
		return "", 0, false, err
	}

	data, err := asMap(response.Data, "data")
	if err != nil {
		return "", 0, false, err
	}
	currentUser := optionalMap(data["currentUser"])
	if len(currentUser) == 0 {
		return "", 0, false, nil
	}
	session := optionalMap(currentUser["dropCurrentSession"])
	if len(session) == 0 {
		return "", 0, false, nil
	}

	dropID := stringValue(session, "dropID")
	if dropID == "" {
		return "", 0, false, nil
	}

	return dropID, int(int64Value(session, "currentMinutesWatched")), true, nil
}

func (s *Scheduler) restartMaintenance(ctx context.Context, triggers []time.Time) {
	s.cancelMaintenance()

	maintenanceCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.maintenanceCancel = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	go s.maintenanceLoop(maintenanceCtx, append([]time.Time(nil), triggers...))
}

func (s *Scheduler) maintenanceLoop(ctx context.Context, triggers []time.Time) {
	defer s.wg.Done()

	sort.Slice(triggers, func(i, j int) bool {
		return triggers[i].Before(triggers[j])
	})

	nextReload := s.nowUTC().Add(s.maintenanceReload)
	index := 0
	for {
		now := s.nowUTC()
		if !now.Before(nextReload) {
			break
		}

		nextTrigger := nextReload
		for index < len(triggers) && !triggers[index].After(now) {
			index++
		}
		if index < len(triggers) && triggers[index].Before(nextTrigger) {
			nextTrigger = triggers[index]
			index++
		}

		if err := s.sleep(ctx, nextTrigger.Sub(now)); err != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if nextTrigger.Equal(nextReload) {
			break
		}
		s.ChangeState(StateChannelsCleanup)
	}

	s.ChangeState(StateInventoryFetch)
}

func (s *Scheduler) cancelMaintenance() {
	s.mu.Lock()
	cancel := s.maintenanceCancel
	s.maintenanceCancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
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

func (s *Scheduler) priorityIndex(channel domain.Channel) int {
	game := channel.CurrentGame()
	if game == nil {
		return math.MaxInt
	}
	return s.priorityIndexByGame(*game, s.WantedGames())
}

func (s *Scheduler) priorityIndexByGame(game domain.Game, wantedGames []domain.Game) int {
	for index, wantedGame := range wantedGames {
		if sameGame(game, wantedGame) {
			return index
		}
	}
	return math.MaxInt
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

func (s *Scheduler) channelsSliceSortedByPriority() []domain.Channel {
	channels := s.Channels()
	sort.SliceStable(channels, func(i, j int) bool {
		return viewerSortKey(channels[i]) > viewerSortKey(channels[j])
	})
	sort.SliceStable(channels, func(i, j int) bool {
		return channels[i].ACLBased && !channels[j].ACLBased
	})
	sort.SliceStable(channels, func(i, j int) bool {
		return s.priorityIndex(channels[i]) < s.priorityIndex(channels[j])
	})
	return channels
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

func (s *Scheduler) shouldResolveProgress(sentAt time.Time) bool {
	if s == nil {
		return false
	}

	s.mu.RLock()
	lastProgressAt := s.lastProgressAt
	s.mu.RUnlock()

	return lastProgressAt.IsZero() || lastProgressAt.Before(sentAt)
}

func (s *Scheduler) nowUTC() time.Time {
	return s.now().UTC()
}

func (s *Scheduler) waitForStateChange(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stateChanged:
		return nil
	}
}

func (s *Scheduler) clearStateChange() {
	for {
		select {
		case <-s.stateChanged:
		default:
			return
		}
	}
}

func (s *Scheduler) signalStateChange() {
	select {
	case s.stateChanged <- struct{}{}:
	default:
	}
}

func (s *Scheduler) waitForWatchingChannel(ctx context.Context) (int64, bool) {
	for {
		s.mu.RLock()
		watchingChannelID := s.watchingChannelID
		s.mu.RUnlock()
		if watchingChannelID != 0 {
			return watchingChannelID, true
		}

		select {
		case <-ctx.Done():
			return 0, false
		case <-s.watchSignal:
		}
	}
}

func (s *Scheduler) sleepWithWatchSignal(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.watchSignal:
		return errWatchInterrupted
	case <-timer.C:
		return nil
	}
}

func (s *Scheduler) signalWatch() {
	select {
	case s.watchSignal <- struct{}{}:
	default:
	}
}

func (s *Scheduler) ensureUserTopics() error {
	authSnapshot := s.authState.Snapshot()
	userID := authSnapshot.UserID
	if userID <= 0 {
		return fmt.Errorf("认证状态缺少 user_id")
	}

	s.mu.Lock()
	previousUserID := s.userTopicUserID
	s.userTopicUserID = userID
	s.mu.Unlock()

	if previousUserID > 0 && previousUserID != userID {
		s.pubsub.RemoveTopics(userTopicKeys(previousUserID)...)
	}

	dropTopic, err := pubsub.UserTopic(pubsub.TopicDrops, userID, s.handleDropEvent)
	if err != nil {
		return err
	}
	notificationTopic, err := pubsub.UserTopic(pubsub.TopicNotifications, userID, s.handleNotificationEvent)
	if err != nil {
		return err
	}
	return s.pubsub.AddTopics(dropTopic, notificationTopic)
}

func (s *Scheduler) processDropProgress(message dropEventMessage) {
	if message.Data.DropID == "" {
		return
	}

	watchingChannel := s.currentWatchingChannel()
	if s.applyDropProgress(s.nowUTC(), watchingChannel, message.Data.DropID, message.Data.CurrentProgressMin) {
		s.logger.Info(
			"收到掉宝进度更新",
			"drop_id", message.Data.DropID,
			"current_minutes", message.Data.CurrentProgressMin,
			"required_minutes", message.Data.RequiredProgressMin,
		)
	}
}

func (s *Scheduler) processDropClaim(ctx context.Context, message dropEventMessage) error {
	dropID := strings.TrimSpace(message.Data.DropID)
	claimID := strings.TrimSpace(message.Data.DropInstanceID)
	if dropID == "" {
		return nil
	}

	campaignID, effectiveClaimID, ok := s.updateDropClaim(dropID, claimID)
	if !ok {
		s.logger.Warn("收到未知掉宝的认领事件", "drop_id", dropID, "claim_id", claimID)
		return nil
	}

	claimed, err := s.claimDropRequest(ctx, effectiveClaimID)
	if err != nil {
		return fmt.Errorf("认领 websocket 掉宝失败: %w", err)
	}
	if claimed {
		s.markDropClaimed(dropID)
	}

	watchingChannel := s.currentWatchingChannel()
	if watchingChannel != nil {
		if err := s.sleep(ctx, 4*time.Second); err != nil {
			return err
		}
		for attempt := 0; attempt < 8; attempt++ {
			currentDropID, _, ok, err := s.fetchCurrentDrop(ctx, watchingChannel.ID)
			if err != nil {
				return fmt.Errorf("轮询 CurrentDrop 失败: %w", err)
			}
			if !ok || currentDropID != dropID {
				break
			}
			if err := s.sleep(ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}

	if s.campaignCanEarn(campaignID, watchingChannel) {
		s.restartWatching()
		return nil
	}

	s.ChangeState(StateInventoryFetch)
	return nil
}

func (s *Scheduler) deleteNotification(ctx context.Context, notificationID string) error {
	operation, err := gql.MustLookup(gql.OperationNotificationsDelete).WithVariables(map[string]any{
		"input": map[string]any{
			"id": notificationID,
		},
	})
	if err != nil {
		return fmt.Errorf("构造 NotificationsDelete 请求失败: %w", err)
	}

	if _, err := s.gqlClient.Do(ctx, operation); err != nil {
		return err
	}
	return nil
}

func (s *Scheduler) readyDrops(now time.Time) []claimCandidate {
	snapshot := s.snapshotCopy()
	ready := make([]claimCandidate, 0)
	for _, campaign := range snapshot.Inventory {
		if campaign == nil || campaign.UpcomingAt(now) {
			continue
		}
		for _, drop := range campaign.Drops() {
			if !drop.CanClaim(now) {
				continue
			}
			ready = append(ready, claimCandidate{
				CampaignID: campaign.ID,
				DropID:     drop.ID,
				ClaimID:    drop.ClaimID,
			})
		}
	}
	return ready
}

func (s *Scheduler) applyDropProgress(now time.Time, channel *domain.Channel, dropID string, currentMinutes int) bool {
	if channel == nil || strings.TrimSpace(dropID) == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil || !drop.CanEarn(now, channel, s.settings.EnableBadgesEmotes, false) {
		return false
	}

	drop.UpdateMinutes(currentMinutes)
	s.lastProgressAt = now.UTC()
	return true
}

func (s *Scheduler) bumpActiveCampaign(now time.Time, channel *domain.Channel) (bool, bool) {
	if channel == nil {
		return false, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	activeCampaign := s.activeCampaignLocked(now, channel)
	if activeCampaign == nil {
		return false, false
	}

	reachedLimit := activeCampaign.BumpMinutes(now, channel, s.settings.EnableBadgesEmotes, false)
	s.lastProgressAt = now.UTC()
	return reachedLimit, true
}

func (s *Scheduler) activeCampaignLocked(now time.Time, channel *domain.Channel) *domain.DropsCampaign {
	var selected *domain.DropsCampaign
	for _, campaign := range s.snapshot.Inventory {
		if campaign == nil || !campaign.CanEarn(now, channel, s.settings.EnableBadgesEmotes, false) {
			continue
		}
		if selected == nil ||
			campaign.RemainingMinutes() < selected.RemainingMinutes() ||
			(campaign.RemainingMinutes() == selected.RemainingMinutes() && campaign.ID < selected.ID) {
			selected = campaign
		}
	}
	return selected
}

func (s *Scheduler) updateDropClaim(dropID string, claimID string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil {
		return "", "", false
	}
	if claimID != "" {
		drop.UpdateClaim(claimID)
	}

	campaignID := ""
	if drop.Campaign != nil {
		campaignID = drop.Campaign.ID
	}
	return campaignID, drop.ClaimID, true
}

func (s *Scheduler) markDropClaimed(dropID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil {
		return false
	}
	return drop.MarkClaimed()
}

func (s *Scheduler) campaignCanEarn(campaignID string, channel *domain.Channel) bool {
	now := s.nowUTC()

	s.mu.RLock()
	defer s.mu.RUnlock()

	campaign := s.snapshot.Campaigns[campaignID]
	if campaign == nil {
		return false
	}
	return campaign.CanEarn(now, channel, s.settings.EnableBadgesEmotes, false)
}

func userTopicKeys(userID int64) []string {
	if userID <= 0 {
		return nil
	}

	dropsKey, err := pubsub.TopicKey(pubsub.CategoryUser, pubsub.TopicDrops, userID)
	if err != nil {
		return nil
	}
	notificationsKey, err := pubsub.TopicKey(pubsub.CategoryUser, pubsub.TopicNotifications, userID)
	if err != nil {
		return []string{dropsKey}
	}
	return []string{dropsKey, notificationsKey}
}

func viewerSortKey(channel domain.Channel) int {
	if viewers := channel.ViewerCount(); viewers > 0 {
		return viewers
	}
	if channel.Online() {
		return 0
	}
	return -1
}

func priorityNameIndex(name string, priority []string) int {
	for index, item := range priority {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(name)) {
			return index
		}
	}
	return math.MaxInt
}

func stringInList(value string, values []string) bool {
	for _, item := range values {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func gameInList(game domain.Game, games []domain.Game) bool {
	for _, item := range games {
		if sameGame(game, item) {
			return true
		}
	}
	return false
}

func sameGame(left domain.Game, right domain.Game) bool {
	switch {
	case left.ID > 0 && right.ID > 0:
		return left.ID == right.ID
	default:
		return strings.EqualFold(strings.TrimSpace(left.Name), strings.TrimSpace(right.Name))
	}
}

func gameKey(game domain.Game) string {
	if game.ID > 0 {
		return strconv.FormatInt(game.ID, 10)
	}
	return strings.ToLower(strings.TrimSpace(game.Name))
}

func uniqueInt64s(values []int64) []int64 {
	if len(values) == 0 {
		return nil
	}

	seen := make(map[int64]struct{}, len(values))
	unique := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}

func cloneChannel(channel domain.Channel) domain.Channel {
	cloned := channel
	if channel.Stream != nil {
		stream := *channel.Stream
		if channel.Stream.Game != nil {
			game := *channel.Stream.Game
			stream.Game = &game
		}
		cloned.Stream = &stream
	}
	return cloned
}

func parseGame(data map[string]any) *domain.Game {
	if len(data) == 0 {
		return nil
	}

	game := domain.Game{
		ID:       int64Value(data, "id"),
		Name:     firstNonEmpty(stringValue(data, "displayName"), stringValue(data, "name")),
		SlugText: stringValue(data, "slug"),
	}
	if game.ID == 0 && game.Name == "" {
		return nil
	}
	return &game
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func asMap(value any, label string) (map[string]any, error) {
	mapValue, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是对象", label)
	}
	return mapValue, nil
}

func optionalMap(value any) map[string]any {
	mapValue, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return mapValue
}

func mapFromMap(source map[string]any, key string) (map[string]any, error) {
	value, ok := source[key]
	if !ok {
		return nil, fmt.Errorf("缺少字段 %q", key)
	}
	return asMap(value, key)
}

func sliceFromMap(source map[string]any, key string) ([]any, error) {
	value, ok := source[key]
	if !ok || value == nil {
		return nil, nil
	}
	sliceValue, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是数组", key)
	}
	return sliceValue, nil
}

func stringValue(source map[string]any, key string) string {
	if len(source) == 0 {
		return ""
	}
	value, ok := source[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func int64Value(source map[string]any, key string) int64 {
	if len(source) == 0 {
		return 0
	}
	value, ok := source[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func cloneInventorySnapshot(snapshot inventory.Snapshot) (inventory.Snapshot, error) {
	cloned := inventory.Snapshot{
		Inventory:           make([]*domain.DropsCampaign, 0, len(snapshot.Inventory)),
		Campaigns:           make(map[string]*domain.DropsCampaign, len(snapshot.Campaigns)),
		Drops:               make(map[string]*domain.TimedDrop, len(snapshot.Drops)),
		MaintenanceTriggers: append([]time.Time(nil), snapshot.MaintenanceTriggers...),
	}

	for _, campaign := range snapshot.Inventory {
		copiedCampaign, err := cloneCampaign(campaign)
		if err != nil {
			return inventory.Snapshot{}, err
		}
		if copiedCampaign == nil {
			continue
		}
		cloned.Inventory = append(cloned.Inventory, copiedCampaign)
		cloned.Campaigns[copiedCampaign.ID] = copiedCampaign
		for _, drop := range copiedCampaign.Drops() {
			cloned.Drops[drop.ID] = drop
		}
	}

	for campaignID, campaign := range snapshot.Campaigns {
		if _, exists := cloned.Campaigns[campaignID]; exists {
			continue
		}
		copiedCampaign, err := cloneCampaign(campaign)
		if err != nil {
			return inventory.Snapshot{}, err
		}
		if copiedCampaign == nil {
			continue
		}
		cloned.Campaigns[campaignID] = copiedCampaign
		for _, drop := range copiedCampaign.Drops() {
			cloned.Drops[drop.ID] = drop
		}
	}

	return cloned, nil
}

func cloneCampaign(campaign *domain.DropsCampaign) (*domain.DropsCampaign, error) {
	if campaign == nil {
		return nil, nil
	}

	spec := domain.CampaignSpec{
		ID:              campaign.ID,
		Name:            campaign.Name,
		Game:            campaign.Game,
		Linked:          campaign.Linked,
		LinkURL:         campaign.LinkURL,
		ImageURL:        campaign.ImageURL,
		StartsAt:        campaign.StartsAt,
		EndsAt:          campaign.EndsAt,
		Status:          campaignStatus(campaign),
		AllowedChannels: cloneChannels(campaign.AllowedChannels),
		Drops:           make([]domain.TimedDropSpec, 0, len(campaign.TimedDrops)),
	}

	for _, drop := range campaign.Drops() {
		if drop == nil {
			continue
		}
		spec.Drops = append(spec.Drops, domain.TimedDropSpec{
			ID:                  drop.ID,
			Name:                drop.Name,
			Benefits:            slices.Clone(drop.Benefits),
			StartsAt:            drop.StartsAt,
			EndsAt:              drop.EndsAt,
			ClaimID:             drop.ClaimID,
			IsClaimed:           drop.IsClaimed,
			PreconditionDropIDs: slices.Clone(drop.PreconditionDropIDs),
			RealCurrentMinutes:  drop.RealCurrentMinutes,
			RequiredMinutes:     drop.RequiredMinutes,
			ExtraCurrentMinutes: drop.ExtraCurrentMinutes,
		})
	}

	return domain.NewCampaign(spec)
}

func cloneChannels(channels []domain.Channel) []domain.Channel {
	if len(channels) == 0 {
		return nil
	}

	cloned := make([]domain.Channel, 0, len(channels))
	for _, channel := range channels {
		channel.Stream = nil
		channel.PendingStream = false
		cloned = append(cloned, channel)
	}
	return cloned
}

func campaignStatus(campaign *domain.DropsCampaign) string {
	if campaign == nil || !campaign.Valid {
		return "EXPIRED"
	}
	return "ACTIVE"
}

func min(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
