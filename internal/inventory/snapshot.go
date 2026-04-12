package inventory

import (
	"sort"
	"time"

	"twitchdropsminergo/internal/domain"
)

func buildSnapshot(campaigns []*domain.DropsCampaign, now time.Time, options RefreshOptions) Snapshot {
	snapshot := Snapshot{
		Inventory: make([]*domain.DropsCampaign, 0, len(campaigns)),
		Campaigns: make(map[string]*domain.DropsCampaign, len(campaigns)),
		Drops:     make(map[string]*domain.TimedDrop),
	}

	nextHour := now.Add(time.Hour)
	triggerSet := make(map[time.Time]struct{})
	for _, campaign := range campaigns {
		if campaign == nil {
			continue
		}

		snapshot.Inventory = append(snapshot.Inventory, campaign)
		snapshot.Campaigns[campaign.ID] = campaign
		for _, drop := range campaign.Drops() {
			snapshot.Drops[drop.ID] = drop
		}

		if campaign.CanEarnWithin(now, nextHour, options.EnableBadgesEmotes) {
			for _, trigger := range campaign.TimeTriggers() {
				if trigger.After(now) {
					triggerSet[trigger] = struct{}{}
				}
			}
		}
	}

	snapshot.MaintenanceTriggers = make([]time.Time, 0, len(triggerSet))
	for trigger := range triggerSet {
		snapshot.MaintenanceTriggers = append(snapshot.MaintenanceTriggers, trigger)
	}
	sort.Slice(snapshot.MaintenanceTriggers, func(i int, j int) bool {
		return snapshot.MaintenanceTriggers[i].Before(snapshot.MaintenanceTriggers[j])
	})

	return snapshot
}

func sortCampaigns(campaigns []*domain.DropsCampaign, now time.Time, enableBadgesEmotes bool) {
	sort.SliceStable(campaigns, func(i int, j int) bool {
		return campaigns[i].ID < campaigns[j].ID
	})
	sort.SliceStable(campaigns, func(i int, j int) bool {
		return campaigns[i].ActiveAt(now) && !campaigns[j].ActiveAt(now)
	})
	sort.SliceStable(campaigns, func(i int, j int) bool {
		return campaignSortTime(campaigns[i], now).Before(campaignSortTime(campaigns[j], now))
	})
	sort.SliceStable(campaigns, func(i int, j int) bool {
		return campaigns[i].Eligible(enableBadgesEmotes) && !campaigns[j].Eligible(enableBadgesEmotes)
	})
}

func campaignSortTime(campaign *domain.DropsCampaign, now time.Time) time.Time {
	if campaign != nil && campaign.UpcomingAt(now) {
		return campaign.StartsAt
	}
	if campaign == nil {
		return time.Time{}
	}
	return campaign.EndsAt
}
