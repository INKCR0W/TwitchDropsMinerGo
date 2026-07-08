package watch

import (
	"context"
	"fmt"
	"strconv"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
)

func (t *Tracker) SyncChannel(ctx context.Context, channelID int64) (bool, error) {
	if t == nil {
		return false, fmt.Errorf("watch 跟踪器未初始化")
	}

	spec, settings, snapshot, err := t.lookupChannel(channelID)
	if err != nil {
		return false, err
	}

	return t.syncFetchedChannel(ctx, spec, settings, snapshot)
}

func (t *Tracker) syncFetchedChannel(ctx context.Context, spec channelSpec, settings config.Settings, snapshot inventory.Snapshot) (bool, error) {
	fetched, err := t.fetchChannel(ctx, spec, settings, snapshot)
	if err != nil {
		return false, err
	}

	t.applyFetched(spec.ID, spec.Epoch, fetched)
	return fetched.Stream != nil, nil
}

func (t *Tracker) SyncChannels(ctx context.Context, channelIDs ...int64) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	specs, settings, snapshot, err := t.collectChannels(channelIDs)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}

	fetched := make(map[int64]fetchedChannel, len(specs))
	for _, chunk := range chunkSpecs(specs, t.batchSize) {
		operations := make([]gql.Operation, 0, len(chunk))
		for _, spec := range chunk {
			operation, err := gql.MustLookup(gql.OperationGetStreamInfo).WithVariables(map[string]any{
				"channel": spec.Login,
			})
			if err != nil {
				return fmt.Errorf("构造 GetStreamInfo 请求失败: %w", err)
			}
			operations = append(operations, operation)
		}

		responses, err := t.gqlClient.DoBatch(ctx, operations)
		if err != nil {
			return fmt.Errorf("批量请求 GetStreamInfo 失败: %w", err)
		}
		if len(responses) != len(operations) {
			return fmt.Errorf("GetStreamInfo batch 响应数量不匹配: 请求 %d 个，响应 %d 个", len(operations), len(responses))
		}

		pendingDrops := make([]channelSpec, 0, len(chunk))
		for index, response := range responses {
			result, err := parseGetStreamInfoResponse(chunk[index], response, settings.AvailableDropsCheck)
			if err != nil {
				t.logger.Warn("跳过无法解析的 GetStreamInfo 响应", "channel_id", chunk[index].ID, "login", chunk[index].Login, "error", err)
				continue
			}
			fetched[chunk[index].ID] = result
			if result.Stream != nil && settings.AvailableDropsCheck {
				pendingDrops = append(pendingDrops, chunk[index])
			}
		}

		if len(pendingDrops) > 0 {
			if err := t.fillDropsEnabledBatch(ctx, pendingDrops, fetched, settings, snapshot); err != nil {
				return err
			}
		}
	}

	for _, spec := range specs {
		result, ok := fetched[spec.ID]
		if !ok {
			continue
		}
		t.applyFetched(spec.ID, spec.Epoch, result)
	}

	return nil
}

func (t *Tracker) CheckOnline(channelID int64) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}

	t.mu.Lock()
	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		t.mu.Unlock()
		return ErrChannelNotTracked
	}
	if tracked.pendingCancel != nil {
		t.mu.Unlock()
		return nil
	}

	pendingCtx, cancel := context.WithCancel(t.ctx)
	tracked.pendingSeq++
	sequence := tracked.pendingSeq
	tracked.pendingCancel = cancel
	tracked.channel.PendingStream = true
	t.wg.Add(1)
	t.mu.Unlock()

	go func() {
		defer t.wg.Done()

		if err := t.sleep(pendingCtx, t.onlineDelay); err != nil {
			return
		}

		var (
			spec     channelSpec
			settings config.Settings
			snapshot inventory.Snapshot
		)

		t.mu.Lock()
		tracked, ok := t.channels[channelID]
		if !ok || tracked == nil || tracked.channel == nil || tracked.pendingCancel == nil || tracked.pendingSeq != sequence {
			t.mu.Unlock()
			return
		}
		tracked.pendingCancel = nil
		tracked.channel.PendingStream = false
		spec = channelSpec{
			ID:          tracked.channel.ID,
			Login:       tracked.channel.Login,
			DisplayName: tracked.channel.DisplayName,
			ACLBased:    tracked.channel.ACLBased,
			Epoch:       tracked.epoch,
		}
		settings = t.settings
		snapshot = t.inventory
		t.mu.Unlock()

		_, _ = t.syncFetchedChannel(t.ctx, spec, settings, snapshot)
	}()

	return nil
}

func (t *Tracker) lookupChannel(channelID int64) (channelSpec, config.Settings, inventory.Snapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return channelSpec{}, config.Settings{}, inventory.Snapshot{}, ErrChannelNotTracked
	}

	return channelSpec{
		ID:          tracked.channel.ID,
		Login:       tracked.channel.Login,
		DisplayName: tracked.channel.DisplayName,
		ACLBased:    tracked.channel.ACLBased,
		Epoch:       tracked.epoch,
	}, t.settings, t.inventory, nil
}

func (t *Tracker) collectChannels(channelIDs []int64) ([]channelSpec, config.Settings, inventory.Snapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	settings := t.settings
	snapshot := t.inventory
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
		return specs, settings, snapshot, nil
	}

	specs := make([]channelSpec, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		tracked, ok := t.channels[channelID]
		if !ok || tracked == nil || tracked.channel == nil {
			return nil, config.Settings{}, inventory.Snapshot{}, ErrChannelNotTracked
		}
		specs = append(specs, channelSpec{
			ID:          tracked.channel.ID,
			Login:       tracked.channel.Login,
			DisplayName: tracked.channel.DisplayName,
			ACLBased:    tracked.channel.ACLBased,
			Epoch:       tracked.epoch,
		})
	}
	return specs, settings, snapshot, nil
}

func (t *Tracker) fetchChannel(ctx context.Context, spec channelSpec, settings config.Settings, snapshot inventory.Snapshot) (fetchedChannel, error) {
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

	fetched, err := parseGetStreamInfoResponse(spec, response, settings.AvailableDropsCheck)
	if err != nil {
		return fetchedChannel{}, err
	}

	if fetched.Stream == nil || !settings.AvailableDropsCheck {
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
		// 与网络错误分支一致：AvailableDrops 解析失败时保留已确认的在线 stream，
		// 而不是丢弃它把在线频道误判为离线。
		t.logger.Warn("解析 AvailableDrops 失败，保留在线状态", "channel_id", spec.ID, "error", err)
		return fetched, nil
	}

	channel := &domain.Channel{
		ID:          spec.ID,
		Login:       spec.Login,
		DisplayName: firstNonEmpty(fetched.DisplayName, spec.DisplayName),
		Stream:      cloneStream(fetched.Stream),
		ACLBased:    spec.ACLBased,
	}
	fetched.Stream.DropsEnabled = dropsEnabled(t.now().UTC(), settings, snapshot, channel, campaignIDs)
	return fetched, nil
}

func (t *Tracker) fillDropsEnabledBatch(ctx context.Context, specs []channelSpec, fetched map[int64]fetchedChannel, settings config.Settings, snapshot inventory.Snapshot) error {
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
		return fmt.Errorf("批量请求 AvailableDrops 失败: %w", err)
	}
	if len(responses) != len(operations) {
		return fmt.Errorf("AvailableDrops batch 响应数量不匹配: 请求 %d 个，响应 %d 个", len(operations), len(responses))
	}

	for index, response := range responses {
		campaignIDs, err := parseAvailableDropsResponse(response)
		if err != nil {
			// 保留在线状态：解析失败时不覆盖 DropsEnabled，也不丢弃该频道。
			t.logger.Warn("解析 AvailableDrops 失败，保留在线状态", "channel_id", specs[index].ID, "error", err)
			continue
		}

		result := fetched[specs[index].ID]
		if result.Stream == nil {
			continue
		}
		channel := &domain.Channel{
			ID:          specs[index].ID,
			Login:       specs[index].Login,
			DisplayName: firstNonEmpty(result.DisplayName, specs[index].DisplayName),
			Stream:      cloneStream(result.Stream),
			ACLBased:    specs[index].ACLBased,
		}
		result.Stream.DropsEnabled = dropsEnabled(t.now().UTC(), settings, snapshot, channel, campaignIDs)
		fetched[specs[index].ID] = result
	}

	return nil
}
