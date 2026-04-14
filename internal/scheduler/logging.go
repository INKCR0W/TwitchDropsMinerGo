package scheduler

import "twitchdropsminergo/internal/domain"

func (s *Scheduler) logWantedGamesUpdate(previous []domain.Game, current []domain.Game) {
	if s == nil {
		return
	}

	s.logger.Info(
		"规划挂游戏列表已更新",
		"wanted_game_count", len(current),
		"wanted_games", formatGameNames(current),
	)
	for _, game := range previous {
		if gameInList(game, current) {
			continue
		}
		s.logger.Info("游戏已移出规划列表", "game", gameName(game))
	}
}

func (s *Scheduler) logNoWatchableChannel(channels []domain.Channel) {
	if s == nil {
		return
	}

	total := len(channels)
	online := 0
	offline := 0
	pending := 0
	for _, ch := range channels {
		switch {
		case ch.Online():
			online++
		case ch.PendingOnline():
			pending++
		default:
			offline++
		}
	}

	s.logger.Info(
		"当前没有可观看的频道",
		"channel_count", total,
		"online", online,
		"offline", offline,
		"pending", pending,
	)
}

func (s *Scheduler) logChannelsFetchSummary(aclCount int, directoryCount int, totalCount int, onlineCount int) {
	if s == nil {
		return
	}

	s.logger.Info(
		"频道抓取完成",
		"acl_channel_count", aclCount,
		"directory_channel_count", directoryCount,
		"total_channel_count", totalCount,
		"online_channel_count", onlineCount,
	)
}

func watchingLogAttrs(channel domain.Channel) []any {
	attrs := []any{
		"channel_id", channel.ID,
		"channel_login", channel.Login,
		"channel_name", channel.DisplayName,
		"acl_based", channel.ACLBased,
	}
	if channel.Stream != nil {
		if channel.Stream.Game != nil {
			attrs = append(attrs, "game", gameName(*channel.Stream.Game))
		}
		if channel.Stream.Viewers > 0 {
			attrs = append(attrs, "viewers", channel.Stream.Viewers)
		}
		attrs = append(attrs, "drops_enabled", channel.Stream.DropsEnabled)
	}
	return attrs
}
