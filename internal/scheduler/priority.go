package scheduler

import (
	"math"
	"sort"
	"strings"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
)

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

	return selectActiveCampaign(snapshot.Inventory, now, channel, settings.EnableBadgesEmotes)
}

func selectActiveCampaign(campaigns []*domain.DropsCampaign, now time.Time, channel *domain.Channel, enableBadgesEmotes bool) *domain.DropsCampaign {
	var selected *domain.DropsCampaign
	for _, campaign := range campaigns {
		if campaign == nil || !campaign.CanEarn(now, channel, enableBadgesEmotes, false) {
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

func (s *Scheduler) computeWantedGames(now time.Time) []domain.Game {
	settings := s.settingsCopy()
	snapshot := s.snapshotCopy()
	nextHour := now.Add(time.Hour)

	campaigns := append([]*domain.DropsCampaign(nil), snapshot.Inventory...)
	if settings.PriorityMode == config.SmartBalance {
		return computeSmartWantedGames(campaigns, now, nextHour, settings)
	}

	return computeLegacyWantedGames(campaigns, now, nextHour, settings)
}

func computeLegacyWantedGames(campaigns []*domain.DropsCampaign, now time.Time, nextHour time.Time, settings config.Settings) []domain.Game {
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

type smartGameCandidate struct {
	game             domain.Game
	campaign         *domain.DropsCampaign
	active           bool
	availability     float64
	availabilityRisk int
	nextDropMinutes  int
	remainingMinutes int
	progress         float64
	priorityIndex    int
}

func computeSmartWantedGames(campaigns []*domain.DropsCampaign, now time.Time, nextHour time.Time, settings config.Settings) []domain.Game {
	bestByGame := make(map[string]smartGameCandidate)
	for _, campaign := range campaigns {
		if campaign == nil ||
			stringInList(campaign.Game.Name, settings.Exclude) ||
			!campaign.CanEarnWithin(now, nextHour, settings.EnableBadgesEmotes) ||
			campaignCertainlyUnfinishable(campaign, now) {
			continue
		}

		candidate := buildSmartGameCandidate(campaign, now, settings)
		key := gameKey(candidate.game)
		current, exists := bestByGame[key]
		if !exists || smartGameCandidateLess(candidate, current) {
			bestByGame[key] = candidate
		}
	}

	candidates := make([]smartGameCandidate, 0, len(bestByGame))
	for _, candidate := range bestByGame {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return smartGameCandidateLess(candidates[i], candidates[j])
	})

	wanted := make([]domain.Game, 0, len(candidates))
	for _, candidate := range candidates {
		wanted = append(wanted, candidate.game)
	}
	return wanted
}

func buildSmartGameCandidate(campaign *domain.DropsCampaign, now time.Time, settings config.Settings) smartGameCandidate {
	return smartGameCandidate{
		game:             campaign.Game,
		campaign:         campaign,
		active:           campaign.ActiveAt(now),
		availability:     campaign.Availability(now),
		availabilityRisk: smartAvailabilityRisk(campaign.Availability(now)),
		nextDropMinutes:  smartNextDropMinutes(campaign, now, settings.EnableBadgesEmotes),
		remainingMinutes: max(campaign.RemainingMinutes(), 0),
		progress:         campaign.Progress(),
		priorityIndex:    priorityNameIndex(campaign.Game.Name, settings.Priority),
	}
}

func smartGameCandidateLess(left smartGameCandidate, right smartGameCandidate) bool {
	switch {
	case left.active != right.active:
		return left.active && !right.active
	case left.availabilityRisk != right.availabilityRisk:
		return left.availabilityRisk < right.availabilityRisk
	case left.availability != right.availability:
		return left.availability < right.availability
	case left.nextDropMinutes != right.nextDropMinutes:
		return left.nextDropMinutes < right.nextDropMinutes
	case left.priorityIndex != right.priorityIndex:
		return left.priorityIndex < right.priorityIndex
	case left.remainingMinutes != right.remainingMinutes:
		return left.remainingMinutes < right.remainingMinutes
	case left.progress != right.progress:
		return left.progress > right.progress
	case !left.campaign.EndsAt.Equal(right.campaign.EndsAt):
		return left.campaign.EndsAt.Before(right.campaign.EndsAt)
	default:
		return strings.ToLower(gameName(left.game)) < strings.ToLower(gameName(right.game))
	}
}

func smartAvailabilityRisk(value float64) int {
	switch {
	case value <= 2:
		return 0
	case value <= 4:
		return 1
	default:
		return 2
	}
}

func smartNextDropMinutes(campaign *domain.DropsCampaign, now time.Time, enableBadgesEmotes bool) int {
	if campaign == nil {
		return math.MaxInt
	}

	if drop := campaign.FirstEarnableDrop(now, nil, enableBadgesEmotes, true); drop != nil {
		return max(drop.RemainingMinutes(), 0)
	}

	return max(campaign.RemainingMinutes(), 0)
}

func campaignCertainlyUnfinishable(campaign *domain.DropsCampaign, now time.Time) bool {
	if campaign == nil {
		return true
	}

	remainingMinutes := campaign.RemainingMinutes()
	if remainingMinutes <= 0 {
		return false
	}

	earliestEarnAt := now
	if campaign.StartsAt.After(earliestEarnAt) {
		earliestEarnAt = campaign.StartsAt
	}
	if !campaign.EndsAt.After(earliestEarnAt) {
		return true
	}

	return campaign.EndsAt.Sub(earliestEarnAt) < time.Duration(remainingMinutes)*time.Minute
}

func (s *Scheduler) activeCampaignLocked(now time.Time, channel *domain.Channel) *domain.DropsCampaign {
	return selectActiveCampaign(s.snapshot.Inventory, now, channel, s.settings.EnableBadgesEmotes)
}

func (s *Scheduler) pendingRewardCompletionCampaignLocked(now time.Time, channel *domain.Channel) *domain.DropsCampaign {
	var selected *domain.DropsCampaign
	for _, campaign := range s.snapshot.Inventory {
		if campaign == nil || !campaign.CanRecordRewardCompletion(now, channel, s.settings.EnableBadgesEmotes, false) {
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
