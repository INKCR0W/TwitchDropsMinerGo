package inventory

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/rewards"
)

const defaultChunkSize = 20

type DiscoveryMode string

const (
	DiscoveryDashboard DiscoveryMode = ""
	DiscoveryCatalog   DiscoveryMode = "catalog"
)

type GQLClient interface {
	Do(context.Context, gql.Operation) (gql.Response, error)
	DoBatch(context.Context, []gql.Operation) ([]gql.Response, error)
}

type AuthState interface {
	Validate(context.Context) error
	Snapshot() auth.Snapshot
}

type Options struct {
	GQLClient         GQLClient
	AuthState         AuthState
	RewardProgress    map[string]rewards.Progress
	Clock             func() time.Time
	ChunkSize         int
	Logger            *slog.Logger
	DiscoveryMode     DiscoveryMode
	CatalogHTTPClient *httpclient.Client
	CatalogEndpoint   string
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
	logger                *slog.Logger
	rewardMu              sync.RWMutex
	completedRewardIDs    map[string]struct{}
	rewardProgressMinutes map[string]int
	now                   func() time.Time
	chunkSize             int
	discoveryMode         DiscoveryMode
	catalogHTTPClient     *httpclient.Client
	catalogEndpoint       string
}

func NewRefresher(options Options) (*Refresher, error) {
	if options.GQLClient == nil {
		return nil, fmt.Errorf("inventory GQL 客户端不能为空")
	}
	if options.AuthState == nil {
		return nil, fmt.Errorf("inventory 认证状态不能为空")
	}
	if options.DiscoveryMode != DiscoveryDashboard && options.DiscoveryMode != DiscoveryCatalog {
		return nil, fmt.Errorf("未知活动发现模式: %s", options.DiscoveryMode)
	}

	now := options.Clock
	if now == nil {
		now = time.Now
	}

	chunkSize := options.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	if options.DiscoveryMode == DiscoveryCatalog && options.CatalogHTTPClient == nil {
		return nil, fmt.Errorf("公共活动目录 HTTP 客户端不能为空")
	}
	catalogEndpoint := options.CatalogEndpoint
	if catalogEndpoint == "" {
		catalogEndpoint = defaultCatalogEndpoint
	}

	completedRewardIDs := rewards.CompletedCampaignIDs(options.RewardProgress)
	rewardProgressMinutes := rewardMinutesByCampaignID(options.RewardProgress)

	return &Refresher{
		gqlClient:             options.GQLClient,
		authState:             options.AuthState,
		logger:                logger,
		completedRewardIDs:    completedRewardIDs,
		rewardProgressMinutes: rewardProgressMinutes,
		now:                   now,
		chunkSize:             chunkSize,
		discoveryMode:         options.DiscoveryMode,
		catalogHTTPClient:     options.CatalogHTTPClient,
		catalogEndpoint:       catalogEndpoint,
	}, nil
}

func (r *Refresher) UpdateRewardProgress(progress map[string]rewards.Progress) {
	if r == nil {
		return
	}

	r.rewardMu.Lock()
	defer r.rewardMu.Unlock()
	r.completedRewardIDs = rewards.CompletedCampaignIDs(progress)
	r.rewardProgressMinutes = rewardMinutesByCampaignID(progress)
}

func (r *Refresher) rewardProgressState() (map[string]struct{}, map[string]int) {
	r.rewardMu.RLock()
	defer r.rewardMu.RUnlock()
	return cloneStringSet(r.completedRewardIDs), cloneIntMap(r.rewardProgressMinutes)
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

func cloneStringSet(values map[string]struct{}) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]struct{}, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneIntMap(values map[string]int) map[string]int {
	if len(values) == 0 {
		return nil
	}

	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
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
	var discovered map[string]any
	if r.discoveryMode == DiscoveryCatalog {
		var err error
		discovered, err = r.fetchCatalogCampaigns(ctx)
		if err != nil {
			return Snapshot{}, err
		}
	}

	// 目录只提供公开信息，随后取库存补入当前账号的进度和关联状态
	inventoryPayload, claimedBenefits, err := r.fetchInventoryPayload(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	var mergedPayload map[string]any
	if r.discoveryMode == DiscoveryCatalog {
		mergedPayload, err = mergeCampaignPayloads(inventoryPayload, discovered)
	} else {
		var availableCampaigns []campaignEnvelope
		availableCampaigns, err = r.fetchAvailableCampaigns(ctx)
		if err == nil {
			mergedPayload, err = r.fetchCampaignDetails(ctx, authSnapshot.UserID, inventoryPayload, availableCampaigns)
		}
	}
	if err != nil {
		return Snapshot{}, err
	}

	r.filterCatalogCampaigns(mergedPayload)
	now := r.now().UTC()
	rewardPayload, err := r.fetchRewardCampaigns(ctx, now)
	if err != nil {
		return Snapshot{}, err
	}
	mergedPayload, err = mergeMaps(mergedPayload, rewardPayload)
	if err != nil {
		return Snapshot{}, fmt.Errorf("合并 RewardCampaigns 数据失败: %w", err)
	}

	campaigns, err := buildCampaigns(mergedPayload, claimedBenefits, r.logger)
	if err != nil {
		return Snapshot{}, err
	}
	sortCampaigns(campaigns, now, options.EnableBadgesEmotes)

	return buildSnapshot(campaigns, now, options), nil
}
