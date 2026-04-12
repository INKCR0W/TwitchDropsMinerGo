package scheduler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/pubsub"
)

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
	ready := s.readyDrops(now)
	s.logger.Info("开始处理游戏筛选阶段", "ready_drop_count", len(ready), "claim_timeout", s.claimSweepTimeout.String())

	claimCtx, cancel := context.WithTimeout(ctx, s.claimSweepTimeout)
	claimResult := s.claimReadyDrops(claimCtx, ready)
	cancel()
	s.logger.Info(
		"待认领掉宝处理完成",
		"ready_drop_count", claimResult.Total,
		"claimed_count", claimResult.Claimed,
		"failed_count", claimResult.Failed,
		"timed_out", claimResult.TimedOut,
	)

	previousWantedGames := s.WantedGames()
	wantedGames := s.computeWantedGames(now)
	s.mu.Lock()
	s.wantedGames = wantedGames
	s.fullCleanup = true
	s.mu.Unlock()
	s.logWantedGamesUpdate(previousWantedGames, wantedGames)
	s.logger.Info("游戏筛选完成", "wanted_game_count", len(wantedGames))

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
	s.sortGamesByPriority(games, wantedGames)
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
	s.sortChannelsByPriority(ordered, wantedGames)

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
