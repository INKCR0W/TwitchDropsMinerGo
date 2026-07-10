package scheduler

import (
	"sort"
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
