package scheduler

import (
	"math"
	"sort"

	"twitchdropsminergo/internal/domain"
)

func (s *Scheduler) sortGamesByPriority(games []domain.Game, wantedGames []domain.Game) {
	sort.Slice(games, func(i, j int) bool {
		return s.priorityIndexByGame(games[i], wantedGames) < s.priorityIndexByGame(games[j], wantedGames)
	})
}

func (s *Scheduler) sortChannelsByPriority(channels []domain.Channel, wantedGames []domain.Game) {
	sort.SliceStable(channels, func(i, j int) bool {
		return viewerSortKey(channels[i]) > viewerSortKey(channels[j])
	})
	sort.SliceStable(channels, func(i, j int) bool {
		return channels[i].ACLBased && !channels[j].ACLBased
	})
	sort.SliceStable(channels, func(i, j int) bool {
		return s.priorityIndexByGameForChannel(channels[i], wantedGames) < s.priorityIndexByGameForChannel(channels[j], wantedGames)
	})
}

func (s *Scheduler) priorityIndex(channel domain.Channel) int {
	return s.priorityIndexByGameForChannel(channel, s.WantedGames())
}

func (s *Scheduler) priorityIndexByGameForChannel(channel domain.Channel, wantedGames []domain.Game) int {
	game := channel.CurrentGame()
	if game == nil {
		return math.MaxInt
	}
	return s.priorityIndexByGame(*game, wantedGames)
}

func (s *Scheduler) priorityIndexByGame(game domain.Game, wantedGames []domain.Game) int {
	for index, wantedGame := range wantedGames {
		if sameGame(game, wantedGame) {
			return index
		}
	}
	return math.MaxInt
}

func (s *Scheduler) channelsSliceSortedByPriority() []domain.Channel {
	channels := s.Channels()
	s.sortChannelsByPriority(channels, s.WantedGames())
	return channels
}
