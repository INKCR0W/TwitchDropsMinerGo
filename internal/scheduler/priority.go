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
	spareMinutes     float64
	priorityIndex    int
	topTier          bool
}

func computeSmartWantedGames(campaigns []*domain.DropsCampaign, now time.Time, nextHour time.Time, settings config.Settings) []domain.Game {
	// 只有当所有 priority 游戏都留有足够富余时间时,才允许非 priority 游戏插队。
	// 富余按绝对分钟数(而非比值)衡量,阈值可通过 smart_priority_safety_minutes 配置,
	// 保证 priority 游戏可以晚挖但不会因插队而来不及完成。
	safetyMinutes := float64(settings.SmartPrioritySafetyMinutes)
	if safetyMinutes <= 0 {
		safetyMinutes = float64(config.DefaultSmartPrioritySafetyMinutes)
	}

	bestByGame := make(map[string]smartGameCandidate)
	anyPriorityBelowSafety := false
	for _, campaign := range campaigns {
		if campaign == nil ||
			stringInList(campaign.Game.Name, settings.Exclude) ||
			!campaign.CanEarnWithin(now, nextHour, settings.EnableBadgesEmotes) ||
			campaignCertainlyUnfinishable(campaign, now) {
			continue
		}

		candidate := buildSmartGameCandidate(campaign, now, settings)
		// 用每个 priority 活动(去重前)自身的富余判断保护,避免同一游戏的宽裕活动
		// 在去重后掩盖了另一个更紧的活动,导致后者被插队挤掉。
		if smartCandidateIsPriority(candidate) && candidate.spareMinutes < safetyMinutes {
			anyPriorityBelowSafety = true
		}
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

	for i := range candidates {
		candidates[i].topTier = smartCandidateTopTier(candidates[i], anyPriorityBelowSafety)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return smartTieredLess(candidates[i], candidates[j])
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
		spareMinutes:     campaign.MinSpareMinutes(now),
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

func smartTieredLess(left smartGameCandidate, right smartGameCandidate) bool {
	if left.topTier != right.topTier {
		return left.topTier && !right.topTier
	}
	return smartGameCandidateLess(left, right)
}

func smartCandidateIsPriority(candidate smartGameCandidate) bool {
	return candidate.priorityIndex != math.MaxInt
}

func smartCandidateAtRisk(candidate smartGameCandidate) bool {
	return candidate.availabilityRisk == 0
}

func smartCandidateTopTier(candidate smartGameCandidate, anyPriorityBelowSafety bool) bool {
	if smartCandidateIsPriority(candidate) {
		return true
	}
	// 仅在以下条件全部满足时,才把非 priority 游戏提升到与 priority 同层参与插队:
	// 它当前可刷(upcoming 还挖不了,提升无意义)、它本身确实紧急,且没有任何
	// priority 游戏的富余时间已低于安全阈值。
	return candidate.active && smartCandidateAtRisk(candidate) && !anyPriorityBelowSafety
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

	if remaining, ok := cheapestTargetRemainingMinutes(campaign); ok {
		return max(remaining, 0)
	}

	return max(campaign.RemainingMinutes(), 0)
}

func isSmartTargetDrop(drop *domain.TimedDrop) bool {
	return drop != nil &&
		!drop.IsClaimed &&
		drop.RequiredMinutes > 0 &&
		len(drop.Benefits) > 0
}

func cheapestTargetRemainingMinutes(campaign *domain.DropsCampaign) (int, bool) {
	cheapest := math.MaxInt
	found := false
	for _, drop := range campaign.Drops() {
		if !isSmartTargetDrop(drop) {
			continue
		}
		if remaining := drop.TotalRemainingMinutes(); remaining < cheapest {
			cheapest = remaining
			found = true
		}
	}
	return cheapest, found
}

func campaignCertainlyUnfinishable(campaign *domain.DropsCampaign, now time.Time) bool {
	if campaign == nil {
		return true
	}

	sawTargetDrop := false
	for _, drop := range campaign.Drops() {
		if !isSmartTargetDrop(drop) {
			continue
		}
		sawTargetDrop = true

		// Weigh only the drop's own minutes against its own window; precondition
		// minutes belong to their (earlier) windows, not this drop's budget.
		remainingMinutes := drop.RemainingMinutes()
		if remainingMinutes <= 0 {
			return false
		}

		earliestEarnAt := now
		if drop.StartsAt.After(earliestEarnAt) {
			earliestEarnAt = drop.StartsAt
		}
		if campaign.StartsAt.After(earliestEarnAt) {
			earliestEarnAt = campaign.StartsAt
		}

		latestEarnAt := drop.EndsAt
		if campaign.EndsAt.Before(latestEarnAt) {
			latestEarnAt = campaign.EndsAt
		}
		if !latestEarnAt.After(earliestEarnAt) {
			continue
		}
		if latestEarnAt.Sub(earliestEarnAt) >= time.Duration(remainingMinutes)*time.Minute {
			return false
		}
	}

	return sawTargetDrop
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
