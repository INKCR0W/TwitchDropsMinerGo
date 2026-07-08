package inventory

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"twitchdropsminergo/internal/gql"
)

type campaignEnvelope struct {
	ID   string
	Data map[string]any
}

type campaignChunkResult struct {
	Data map[string]any
	Err  error
}

func (r *Refresher) fetchInventoryPayload(ctx context.Context) (map[string]any, map[string]time.Time, error) {
	response, err := r.gqlClient.Do(ctx, gql.MustLookup(gql.OperationInventory))
	if err != nil {
		return nil, nil, fmt.Errorf("请求 Inventory 失败: %w", err)
	}

	inventoryRoot, err := nestedValue(response.Data, "currentUser", "inventory")
	if err != nil {
		return nil, nil, fmt.Errorf("解析 Inventory 响应失败: %w", err)
	}

	inventoryMap, err := asMap(inventoryRoot, "Inventory.currentUser.inventory")
	if err != nil {
		return nil, nil, err
	}

	ongoingCampaigns, err := sliceFromMap(inventoryMap, "dropCampaignsInProgress")
	if err != nil {
		return nil, nil, err
	}
	inventoryPayload, err := campaignsByID(ongoingCampaigns)
	if err != nil {
		return nil, nil, err
	}

	gameEventDrops, err := sliceFromMap(inventoryMap, "gameEventDrops")
	if err != nil {
		return nil, nil, err
	}
	claimedBenefits, err := parseClaimedBenefits(gameEventDrops)
	if err != nil {
		return nil, nil, err
	}

	return inventoryPayload, claimedBenefits, nil
}

func (r *Refresher) fetchAvailableCampaigns(ctx context.Context) ([]campaignEnvelope, error) {
	response, err := r.gqlClient.Do(ctx, gql.MustLookup(gql.OperationCampaigns))
	if err != nil {
		return nil, fmt.Errorf("请求 Campaigns 失败: %w", err)
	}

	campaignsRoot, err := nestedValue(response.Data, "currentUser", "dropCampaigns")
	if err != nil {
		return nil, fmt.Errorf("解析 Campaigns 响应失败: %w", err)
	}

	if campaignsRoot == nil {
		return nil, nil
	}
	campaignList, err := asSlice(campaignsRoot, "Campaigns.currentUser.dropCampaigns")
	if err != nil {
		return nil, err
	}

	applicableStatuses := map[string]struct{}{
		"ACTIVE":   {},
		"UPCOMING": {},
	}

	availableCampaigns := make([]campaignEnvelope, 0, len(campaignList))
	for index, item := range campaignList {
		if item == nil {
			continue
		}
		campaignData, err := asMap(item, fmt.Sprintf("Campaigns.currentUser.dropCampaigns[%d]", index))
		if err != nil {
			return nil, err
		}
		if _, ok := applicableStatuses[strings.ToUpper(stringValue(campaignData, "status"))]; !ok {
			continue
		}

		campaignID := stringValue(campaignData, "id")
		if campaignID == "" {
			return nil, fmt.Errorf("Campaigns.currentUser.dropCampaigns[%d].id 不能为空", index)
		}

		availableCampaigns = append(availableCampaigns, campaignEnvelope{
			ID:   campaignID,
			Data: cloneMap(campaignData),
		})
	}

	return availableCampaigns, nil
}

func (r *Refresher) fetchRewardCampaigns(ctx context.Context, now time.Time) (map[string]any, error) {
	response, err := r.gqlClient.Do(ctx, gql.MustLookup(gql.OperationRewardCampaigns))
	if err != nil {
		return nil, fmt.Errorf("请求 RewardCampaigns 失败: %w", err)
	}

	data, err := asMap(response.Data, "RewardCampaigns.data")
	if err != nil {
		return nil, fmt.Errorf("解析 RewardCampaigns 响应失败: %w", err)
	}

	roots := []map[string]any{data}
	if currentUser := optionalMap(data["currentUser"]); len(currentUser) > 0 {
		roots = append(roots, currentUser)
	}

	completedRewardIDs, rewardProgressMinutes := r.rewardProgressState()
	rewardPayload := make(map[string]any)
	seen := make(map[string]struct{})
	for _, root := range roots {
		campaigns, err := collectRewardCampaigns(root)
		if err != nil {
			return nil, err
		}
		for _, campaign := range campaigns {
			campaignID := rewardCampaignIDPrefix + stringValue(campaign, "id")
			if _, completed := completedRewardIDs[campaignID]; completed {
				continue
			}

			converted, ok, err := rewardCampaignToDropCampaignWithProgress(campaign, now, rewardProgressMinutes[campaignID])
			if err != nil {
				r.logger.Warn("跳过无法解析的 reward campaign", "campaign_id", stringValue(campaign, "id"), "error", err)
				continue
			}
			if !ok {
				continue
			}
			convertedID := stringValue(converted, "id")
			if _, exists := seen[convertedID]; exists {
				continue
			}
			seen[convertedID] = struct{}{}
			if !isApplicableRewardStatus(stringValue(converted, "status")) {
				continue
			}
			rewardPayload[convertedID] = converted
		}
	}

	return rewardPayload, nil
}

func (r *Refresher) fetchCampaignDetails(ctx context.Context, userID int64, inventoryPayload map[string]any, availableCampaigns []campaignEnvelope) (map[string]any, error) {
	if len(availableCampaigns) == 0 {
		return cloneMap(inventoryPayload), nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	chunks := splitCampaignChunks(availableCampaigns, r.chunkSize)
	results := make(chan campaignChunkResult, len(chunks))
	for _, chunk := range chunks {
		chunk := chunk
		go func() {
			data, err := r.fetchCampaignChunk(ctx, userID, chunk)
			results <- campaignChunkResult{Data: data, Err: err}
		}()
	}

	mergedPayload := cloneMap(inventoryPayload)
	var firstErr error
	for range chunks {
		result := <-results
		if result.Err != nil {
			if firstErr == nil {
				firstErr = result.Err
				cancel()
			}
			continue
		}

		if firstErr != nil {
			continue
		}

		mergedPayload, firstErr = mergeMaps(mergedPayload, result.Data)
		if firstErr != nil {
			cancel()
		}
	}

	if firstErr != nil {
		return nil, firstErr
	}

	return mergedPayload, nil
}

func (r *Refresher) fetchCampaignChunk(ctx context.Context, userID int64, chunk []campaignEnvelope) (map[string]any, error) {
	operations := make([]gql.Operation, 0, len(chunk))
	for _, campaign := range chunk {
		operation, err := gql.MustLookup(gql.OperationCampaignDetails).WithVariables(map[string]any{
			"channelLogin": strconv.FormatInt(userID, 10),
			"dropID":       campaign.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("构造 CampaignDetails 请求失败: %w", err)
		}
		operations = append(operations, operation)
	}

	responses, err := r.gqlClient.DoBatch(ctx, operations)
	if err != nil {
		return nil, fmt.Errorf("请求 CampaignDetails 失败: %w", err)
	}

	detailsPayload := make(map[string]any, len(chunk))
	for index, response := range responses {
		campaignValue, err := nestedValue(response.Data, "user", "dropCampaign")
		if err != nil {
			return nil, fmt.Errorf("解析 CampaignDetails[%d] 响应失败: %w", index, err)
		}
		campaignData, err := asMap(campaignValue, fmt.Sprintf("CampaignDetails[%d].user.dropCampaign", index))
		if err != nil {
			return nil, err
		}

		campaignID := stringValue(campaignData, "id")
		if campaignID == "" {
			return nil, fmt.Errorf("CampaignDetails[%d].user.dropCampaign.id 不能为空", index)
		}
		detailsPayload[campaignID] = cloneMap(campaignData)
	}

	availablePayload := make(map[string]any, len(chunk))
	for _, campaign := range chunk {
		availablePayload[campaign.ID] = cloneMap(campaign.Data)
	}

	mergedChunk, err := mergeMaps(availablePayload, detailsPayload)
	if err != nil {
		return nil, fmt.Errorf("合并 Campaigns 与 CampaignDetails 数据失败: %w", err)
	}

	return mergedChunk, nil
}

func splitCampaignChunks(campaigns []campaignEnvelope, chunkSize int) [][]campaignEnvelope {
	if len(campaigns) == 0 {
		return nil
	}
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	chunks := make([][]campaignEnvelope, 0, (len(campaigns)+chunkSize-1)/chunkSize)
	for start := 0; start < len(campaigns); start += chunkSize {
		end := start + chunkSize
		if end > len(campaigns) {
			end = len(campaigns)
		}
		chunk := make([]campaignEnvelope, end-start)
		copy(chunk, campaigns[start:end])
		chunks = append(chunks, chunk)
	}

	return chunks
}

func campaignsByID(values []any) (map[string]any, error) {
	campaigns := make(map[string]any, len(values))
	for index, item := range values {
		campaignData, err := asMap(item, fmt.Sprintf("campaigns[%d]", index))
		if err != nil {
			return nil, err
		}

		campaignID := stringValue(campaignData, "id")
		if campaignID == "" {
			return nil, fmt.Errorf("campaigns[%d].id 不能为空", index)
		}
		campaigns[campaignID] = cloneMap(campaignData)
	}

	return campaigns, nil
}
