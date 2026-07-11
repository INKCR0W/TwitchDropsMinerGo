package watch

import (
	"context"
	"fmt"
	"strconv"

	"twitchdropsminergo/internal/gql"
)

func (t *Tracker) lookupChannel(channelID int64) (channelSpec, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return channelSpec{}, ErrChannelNotTracked
	}

	return channelSpec{
		ID:          tracked.channel.ID,
		Login:       tracked.channel.Login,
		DisplayName: tracked.channel.DisplayName,
		ACLBased:    tracked.channel.ACLBased,
		Epoch:       tracked.epoch,
	}, nil
}

func (t *Tracker) collectChannels(channelIDs []int64) ([]channelSpec, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(channelIDs) == 0 {
		specs := make([]channelSpec, 0, len(t.channels))
		for _, tracked := range t.channels {
			if tracked == nil || tracked.channel == nil {
				continue
			}
			specs = append(specs, channelSpec{
				ID:          tracked.channel.ID,
				Login:       tracked.channel.Login,
				DisplayName: tracked.channel.DisplayName,
				ACLBased:    tracked.channel.ACLBased,
				Epoch:       tracked.epoch,
			})
		}
		return specs, nil
	}

	specs := make([]channelSpec, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		tracked, ok := t.channels[channelID]
		if !ok || tracked == nil || tracked.channel == nil {
			return nil, ErrChannelNotTracked
		}
		specs = append(specs, channelSpec{
			ID:          tracked.channel.ID,
			Login:       tracked.channel.Login,
			DisplayName: tracked.channel.DisplayName,
			ACLBased:    tracked.channel.ACLBased,
			Epoch:       tracked.epoch,
		})
	}
	return specs, nil
}

func (t *Tracker) fetchChannel(ctx context.Context, spec channelSpec) (fetchedChannel, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	operation, err := gql.MustLookup(gql.OperationGetStreamInfo).WithVariables(map[string]any{
		"channel": spec.Login,
	})
	if err != nil {
		return fetchedChannel{}, fmt.Errorf("构造 GetStreamInfo 请求失败: %w", err)
	}

	response, err := t.gqlClient.Do(ctx, operation)
	if err != nil {
		return fetchedChannel{}, fmt.Errorf("请求 GetStreamInfo 失败: %w", err)
	}

	fetched, err := parseGetStreamInfoResponse(spec, response)
	if err != nil {
		return fetchedChannel{}, err
	}

	if fetched.Stream == nil {
		return fetched, nil
	}

	available, err := gql.MustLookup(gql.OperationAvailableDrops).WithVariables(map[string]any{
		"channelID": strconv.FormatInt(spec.ID, 10),
	})
	if err != nil {
		return fetchedChannel{}, fmt.Errorf("构造 AvailableDrops 请求失败: %w", err)
	}

	response, err = t.gqlClient.Do(ctx, available)
	if err != nil {
		return fetched, nil
	}

	campaignIDs, err := parseAvailableDropsResponse(response)
	if err != nil {
		// 保留已确认的在线 stream, 不能因为查不到可推进活动就把频道判为离线
		t.logger.Warn("解析 AvailableDrops 失败，保留在线状态", "channel_id", spec.ID, "error", err)
		return fetched, nil
	}

	fetched.Stream.OfferedCampaignIDs = campaignIDs
	return fetched, nil
}

func (t *Tracker) fillOfferedCampaigns(ctx context.Context, specs []channelSpec, fetched map[int64]fetchedChannel) error {
	operations := make([]gql.Operation, 0, len(specs))
	for _, spec := range specs {
		operation, err := gql.MustLookup(gql.OperationAvailableDrops).WithVariables(map[string]any{
			"channelID": strconv.FormatInt(spec.ID, 10),
		})
		if err != nil {
			return fmt.Errorf("构造 AvailableDrops 请求失败: %w", err)
		}
		operations = append(operations, operation)
	}

	responses, err := t.gqlClient.DoBatch(ctx, operations)
	if err != nil {
		t.logger.Warn("批量请求 AvailableDrops 失败，保留在线状态", "channel_count", len(specs), "error", err)
		return nil
	}
	if len(responses) != len(operations) {
		t.logger.Warn("AvailableDrops batch 响应数量不匹配，保留在线状态", "requested", len(operations), "received", len(responses))
		return nil
	}

	for index, response := range responses {
		campaignIDs, err := parseAvailableDropsResponse(response)
		if err != nil {
			t.logger.Warn("解析 AvailableDrops 失败，保留在线状态", "channel_id", specs[index].ID, "error", err)
			continue
		}

		result := fetched[specs[index].ID]
		if result.Stream == nil {
			continue
		}
		result.Stream.OfferedCampaignIDs = campaignIDs
		fetched[specs[index].ID] = result
	}

	return nil
}
