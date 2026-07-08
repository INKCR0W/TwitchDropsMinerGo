package watch

import (
	"fmt"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
)

func parseGetStreamInfoResponse(spec channelSpec, response gql.Response, availableDropsCheck bool) (fetchedChannel, error) {
	userValue, err := nestedMap(response.Data, "data", "user")
	if err != nil {
		return fetchedChannel{}, err
	}
	if userValue == nil {
		return fetchedChannel{DisplayName: spec.DisplayName}, nil
	}

	displayName := firstNonEmpty(stringValue(userValue, "displayName"), spec.DisplayName)
	streamValue, exists := userValue["stream"]
	if !exists || isNilValue(streamValue) {
		return fetchedChannel{DisplayName: displayName}, nil
	}

	streamData, err := asMap(streamValue, "data.user.stream")
	if err != nil {
		return fetchedChannel{}, err
	}
	settingsData := optionalMap(userValue["broadcastSettings"])

	stream := &domain.Stream{
		BroadcastID:  int64Value(streamData, "id"),
		Game:         parseGame(optionalMap(settingsData["game"])),
		Viewers:      int(int64Value(streamData, "viewersCount")),
		Title:        stringValue(settingsData, "title"),
		DropsEnabled: !availableDropsCheck,
	}

	return fetchedChannel{
		DisplayName: displayName,
		Stream:      stream,
	}, nil
}

func parseAvailableDropsResponse(response gql.Response) ([]string, error) {
	channelValue, err := nestedMap(response.Data, "data", "channel")
	if err != nil {
		return nil, err
	}
	if channelValue == nil {
		return nil, nil
	}

	dropsValue, ok := channelValue["viewerDropCampaigns"]
	if !ok || isNilValue(dropsValue) {
		return nil, nil
	}

	items, err := asSlice(dropsValue, "data.channel.viewerDropCampaigns")
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(items))
	for index, item := range items {
		campaignData, err := asMap(item, fmt.Sprintf("data.channel.viewerDropCampaigns[%d]", index))
		if err != nil {
			return nil, err
		}
		if campaignID := stringValue(campaignData, "id"); campaignID != "" {
			ids = append(ids, campaignID)
		}
	}

	return ids, nil
}

func dropsEnabled(now time.Time, settings config.Settings, snapshot inventory.Snapshot, channel *domain.Channel, availableCampaignIDs []string) bool {
	for _, campaignID := range availableCampaignIDs {
		campaign := snapshot.Campaigns[campaignID]
		if campaign == nil {
			continue
		}
		if campaign.CanEarn(now, channel, settings.EnableBadgesEmotes, true) {
			return true
		}
	}
	return false
}
