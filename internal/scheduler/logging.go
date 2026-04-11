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
