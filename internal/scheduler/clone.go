package scheduler

import (
	"slices"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/inventory"
)

func cloneChannel(channel domain.Channel) domain.Channel {
	cloned := channel
	if channel.Stream != nil {
		stream := *channel.Stream
		if channel.Stream.Game != nil {
			game := *channel.Stream.Game
			stream.Game = &game
		}
		stream.OfferedCampaignIDs = slices.Clone(channel.Stream.OfferedCampaignIDs)
		cloned.Stream = &stream
	}
	return cloned
}

func cloneInventorySnapshot(snapshot inventory.Snapshot) (inventory.Snapshot, error) {
	cloned := inventory.Snapshot{
		Inventory:           make([]*domain.DropsCampaign, 0, len(snapshot.Inventory)),
		Campaigns:           make(map[string]*domain.DropsCampaign, len(snapshot.Campaigns)),
		Drops:               make(map[string]*domain.TimedDrop, len(snapshot.Drops)),
		MaintenanceTriggers: append([]time.Time(nil), snapshot.MaintenanceTriggers...),
	}

	for _, campaign := range snapshot.Inventory {
		copiedCampaign, err := cloneCampaign(campaign)
		if err != nil {
			return inventory.Snapshot{}, err
		}
		if copiedCampaign == nil {
			continue
		}
		cloned.Inventory = append(cloned.Inventory, copiedCampaign)
		cloned.Campaigns[copiedCampaign.ID] = copiedCampaign
		for _, drop := range copiedCampaign.Drops() {
			cloned.Drops[drop.ID] = drop
		}
	}

	for campaignID, campaign := range snapshot.Campaigns {
		if _, exists := cloned.Campaigns[campaignID]; exists {
			continue
		}
		copiedCampaign, err := cloneCampaign(campaign)
		if err != nil {
			return inventory.Snapshot{}, err
		}
		if copiedCampaign == nil {
			continue
		}
		cloned.Campaigns[campaignID] = copiedCampaign
		for _, drop := range copiedCampaign.Drops() {
			cloned.Drops[drop.ID] = drop
		}
	}

	return cloned, nil
}

func cloneCampaign(campaign *domain.DropsCampaign) (*domain.DropsCampaign, error) {
	if campaign == nil {
		return nil, nil
	}

	spec := domain.CampaignSpec{
		ID:               campaign.ID,
		Name:             campaign.Name,
		Game:             campaign.Game,
		Linked:           campaign.Linked,
		LinkURL:          campaign.LinkURL,
		ImageURL:         campaign.ImageURL,
		StartsAt:         campaign.StartsAt,
		EndsAt:           campaign.EndsAt,
		Status:           campaignStatus(campaign),
		IsRewardCampaign: campaign.IsRewardCampaign,
		AllowedChannels:  cloneChannels(campaign.AllowedChannels),
		Drops:            make([]domain.TimedDropSpec, 0, len(campaign.TimedDrops)),
	}

	for _, drop := range campaign.Drops() {
		if drop == nil {
			continue
		}
		spec.Drops = append(spec.Drops, domain.TimedDropSpec{
			ID:                  drop.ID,
			Name:                drop.Name,
			Benefits:            slices.Clone(drop.Benefits),
			StartsAt:            drop.StartsAt,
			EndsAt:              drop.EndsAt,
			ClaimID:             drop.ClaimID,
			IsClaimed:           drop.IsClaimed,
			PreconditionDropIDs: slices.Clone(drop.PreconditionDropIDs),
			RealCurrentMinutes:  drop.RealCurrentMinutes,
			RequiredMinutes:     drop.RequiredMinutes,
			ExtraCurrentMinutes: drop.ExtraCurrentMinutes,
		})
	}

	return domain.NewCampaign(spec)
}

func cloneChannels(channels []domain.Channel) []domain.Channel {
	if len(channels) == 0 {
		return nil
	}

	cloned := make([]domain.Channel, 0, len(channels))
	for _, channel := range channels {
		channel.Stream = nil
		channel.PendingStream = false
		cloned = append(cloned, channel)
	}
	return cloned
}

func campaignStatus(campaign *domain.DropsCampaign) string {
	if campaign == nil || !campaign.Valid {
		return "EXPIRED"
	}
	return "ACTIVE"
}
