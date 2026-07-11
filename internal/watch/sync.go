package watch

import (
	"context"
	"fmt"

	"twitchdropsminergo/internal/gql"
)

func (t *Tracker) SyncChannel(ctx context.Context, channelID int64) (bool, error) {
	if t == nil {
		return false, fmt.Errorf("watch 跟踪器未初始化")
	}

	spec, err := t.lookupChannel(channelID)
	if err != nil {
		return false, err
	}

	return t.syncFetchedChannel(ctx, spec)
}

func (t *Tracker) syncFetchedChannel(ctx context.Context, spec channelSpec) (bool, error) {
	fetched, err := t.fetchChannel(ctx, spec)
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

	specs, err := t.collectChannels(channelIDs)
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
			result, err := parseGetStreamInfoResponse(chunk[index], response)
			if err != nil {
				t.logger.Warn("跳过无法解析的 GetStreamInfo 响应", "channel_id", chunk[index].ID, "login", chunk[index].Login, "error", err)
				continue
			}
			fetched[chunk[index].ID] = result
			if result.Stream != nil {
				pendingDrops = append(pendingDrops, chunk[index])
			}
		}

		if len(pendingDrops) > 0 {
			if err := t.fillOfferedCampaigns(ctx, pendingDrops, fetched); err != nil {
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

		t.mu.Lock()
		tracked, ok := t.channels[channelID]
		if !ok || tracked == nil || tracked.channel == nil || tracked.pendingCancel == nil || tracked.pendingSeq != sequence {
			t.mu.Unlock()
			return
		}
		tracked.pendingCancel = nil
		tracked.channel.PendingStream = false
		spec := channelSpec{
			ID:          tracked.channel.ID,
			Login:       tracked.channel.Login,
			DisplayName: tracked.channel.DisplayName,
			ACLBased:    tracked.channel.ACLBased,
			Epoch:       tracked.epoch,
		}
		t.mu.Unlock()

		_, _ = t.syncFetchedChannel(t.ctx, spec)
	}()

	return nil
}
