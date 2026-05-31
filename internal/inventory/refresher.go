package inventory

import (
	"context"
	"fmt"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/rewards"
)

const defaultChunkSize = 20

type GQLClient interface {
	Do(context.Context, gql.Operation) (gql.Response, error)
	DoBatch(context.Context, []gql.Operation) ([]gql.Response, error)
}

type AuthState interface {
	Validate(context.Context) error
	Snapshot() auth.Snapshot
}

type Options struct {
	GQLClient      GQLClient
	AuthState      AuthState
	RewardProgress map[string]rewards.Progress
	Clock          func() time.Time
	ChunkSize      int
}

type RefreshOptions struct {
	EnableBadgesEmotes bool
}

type Snapshot struct {
	Inventory           []*domain.DropsCampaign
	Campaigns           map[string]*domain.DropsCampaign
	Drops               map[string]*domain.TimedDrop
	MaintenanceTriggers []time.Time
}

type Refresher struct {
	gqlClient             GQLClient
	authState             AuthState
	completedRewardIDs    map[string]struct{}
	rewardProgressMinutes map[string]int
	now                   func() time.Time
	chunkSize             int
}

func NewRefresher(options Options) (*Refresher, error) {
	if options.GQLClient == nil {
		return nil, fmt.Errorf("inventory GQL 客户端不能为空")
	}
	if options.AuthState == nil {
		return nil, fmt.Errorf("inventory 认证状态不能为空")
	}

	now := options.Clock
	if now == nil {
		now = time.Now
	}

	chunkSize := options.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	completedRewardIDs := rewards.CompletedCampaignIDs(options.RewardProgress)
	rewardProgressMinutes := rewardMinutesByCampaignID(options.RewardProgress)

	return &Refresher{
		gqlClient:             options.GQLClient,
		authState:             options.AuthState,
		completedRewardIDs:    completedRewardIDs,
		rewardProgressMinutes: rewardProgressMinutes,
		now:                   now,
		chunkSize:             chunkSize,
	}, nil
}

func (r *Refresher) UpdateRewardProgress(progress map[string]rewards.Progress) {
	if r == nil {
		return
	}

	r.completedRewardIDs = rewards.CompletedCampaignIDs(progress)
	r.rewardProgressMinutes = rewardMinutesByCampaignID(progress)
}

func rewardMinutesByCampaignID(progress map[string]rewards.Progress) map[string]int {
	if len(progress) == 0 {
		return nil
	}

	result := make(map[string]int, len(progress))
	for campaignID, item := range progress {
		if item.MinutesWatched <= 0 {
			continue
		}
		result[campaignID] = item.MinutesWatched
	}
	return result
}

func (r *Refresher) Refresh(ctx context.Context, options RefreshOptions) (Snapshot, error) {
	if r == nil {
		return Snapshot{}, fmt.Errorf("inventory 刷新器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := r.authState.Validate(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("校验认证状态失败: %w", err)
	}

	authSnapshot := r.authState.Snapshot()
	if authSnapshot.UserID == 0 {
		return Snapshot{}, fmt.Errorf("认证状态缺少 user_id")
	}
	now := r.now().UTC()

	inventoryPayload, claimedBenefits, err := r.fetchInventoryPayload(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	availableCampaigns, err := r.fetchAvailableCampaigns(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	mergedPayload, err := r.fetchCampaignDetails(ctx, authSnapshot.UserID, inventoryPayload, availableCampaigns)
	if err != nil {
		return Snapshot{}, err
	}

	rewardPayload, err := r.fetchRewardCampaigns(ctx, now)
	if err != nil {
		return Snapshot{}, err
	}
	mergedPayload, err = mergeMaps(mergedPayload, rewardPayload)
	if err != nil {
		return Snapshot{}, fmt.Errorf("合并 RewardCampaigns 数据失败: %w", err)
	}

	campaigns, err := buildCampaigns(mergedPayload, claimedBenefits)
	if err != nil {
		return Snapshot{}, err
	}
	sortCampaigns(campaigns, now, options.EnableBadgesEmotes)

	return buildSnapshot(campaigns, now, options), nil
}
