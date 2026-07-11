package watch

import (
	"fmt"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func parseGetStreamInfoResponse(spec channelSpec, response gql.Response) (fetchedChannel, error) {
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
		DropsEnabled: true,
	}

	return fetchedChannel{
		DisplayName: displayName,
		Stream:      stream,
	}, nil
}

func parseAvailableDropsResponse(response gql.Response) ([]string, error) {
	// 出错时 GQL 层会把出错路径填成 null, 那不是"此频道无掉宝可拿", 只能当作未知
	if len(response.Errors) > 0 || response.Error != "" {
		return nil, nil
	}

	channelValue, err := nestedMap(response.Data, "data", "channel")
	if err != nil {
		return nil, err
	}
	if channelValue == nil {
		return []string{}, nil
	}

	dropsValue, ok := channelValue["viewerDropCampaigns"]
	if !ok {
		// 字段缺失是 schema 漂移, 不是"此频道无掉宝可拿"
		return nil, nil
	}
	if isNilValue(dropsValue) {
		return []string{}, nil
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
