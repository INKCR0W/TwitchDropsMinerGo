package scheduler

import (
	"context"
	"fmt"
	"slices"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/pubsub"
)

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

	if s.channelStalled(channel.ID, now) {
		return false
	}

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
			s.lastAdvanceAt = time.Time{}
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

// 目录返回的 stream 不含 AvailableDrops 结果, 别把 ACL 同步已确认的答案抹回未知
func carryOverKnownStream(existing domain.Channel, channel *domain.Channel) {
	if existing.Stream == nil || channel == nil || channel.Stream == nil {
		return
	}
	if existing.Stream.DropsEnabled {
		channel.Stream.DropsEnabled = true
	}
	// 换了一场直播就不能沿用上一场的结论, 否则会把"拿不到"永久钉死在新广播上
	if channel.Stream.OfferedCampaignIDs == nil && existing.Stream.BroadcastID == channel.Stream.BroadcastID {
		channel.Stream.OfferedCampaignIDs = slices.Clone(existing.Stream.OfferedCampaignIDs)
	}
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
