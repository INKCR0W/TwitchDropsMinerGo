package scheduler

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/pubsub"
)

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
		s.advanceState(StateChannelsCleanup, StateIdle)
		return
	}
	s.advanceState(StateChannelsCleanup, StateChannelsFetch)
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
	rewardNoACLGames := make(map[string]domain.Game)

	earnableFound := false
	for _, campaign := range snapshot.Inventory {
		if campaign == nil ||
			!gameInList(campaign.Game, wantedGames) ||
			!campaign.CanEarnWithin(now, nextHour, settings.EnableBadgesEmotes) {
			continue
		}

		earnableFound = true
		if len(campaign.AllowedChannels) > 0 {
			for _, channel := range campaign.AllowedChannels {
				if _, exists := newChannels[channel.ID]; exists {
					continue
				}
				aclChannels[channel.ID] = channel
			}
			continue
		}
		if campaign.IsRewardCampaign {
			rewardNoACLGames[gameKey(campaign.Game)] = campaign.Game
			continue
		}
		noACLGames[gameKey(campaign.Game)] = campaign.Game
	}

	if !earnableFound {
		s.logger.Info(
			"当前没有可推进的活动匹配规划游戏",
			"wanted_game_count", len(wantedGames),
			"wanted_games", formatGameNames(wantedGames),
		)
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

	rewardGames := make([]domain.Game, 0, len(rewardNoACLGames))
	for _, game := range rewardNoACLGames {
		rewardGames = append(rewardGames, game)
	}
	s.sortGamesByPriority(rewardGames, wantedGames)
	for _, game := range rewardGames {
		channels, err := s.getLiveStreams(ctx, game, s.directoryLimit, false)
		if err != nil {
			return err
		}
		for _, channel := range channels {
			if existing, exists := newChannels[channel.ID]; exists && existing.Stream != nil && existing.Stream.DropsEnabled && channel.Stream != nil {
				channel.Stream.DropsEnabled = true
			}
			s.upsertChannel(channel)
			newChannels[channel.ID] = channel
		}
	}

	ordered := make([]domain.Channel, 0, len(newChannels))
	for _, channel := range newChannels {
		ordered = append(ordered, channel)
	}
	s.sortChannelsByPriority(ordered, wantedGames)

	onlineCount := 0
	for _, channel := range ordered {
		if channel.Online() {
			onlineCount++
		}
	}
	directoryCount := 0
	for _, channel := range ordered {
		if !channel.ACLBased {
			directoryCount++
		}
	}
	s.logChannelsFetchSummary(len(aclChannels), directoryCount, len(ordered), onlineCount)

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

	s.clearRuntimeError()
	s.advanceState(StateChannelsFetch, StateChannelSwitch)
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
		return
	}

	if watching := s.currentWatchingChannel(); watching != nil && s.canWatch(*watching) {
		return
	}

	s.logNoWatchableChannel(channels)
	s.advanceState(StateChannelSwitch, StateIdle)
}
