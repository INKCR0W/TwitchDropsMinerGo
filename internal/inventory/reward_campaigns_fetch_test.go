package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/gql"
)

func TestFetchRewardCampaignsDeduplicatesTopLevelAndCurrentUserCampaigns(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName != "ViewerDropsDashboard" {
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
			rewardCampaign := builderCapeRewardCampaignMap()
			return gql.Response{Data: map[string]any{
				"rewardCampaignsAvailableToUser": []any{rewardCampaign},
				"currentUser": map[string]any{
					"rewardCampaigns": []any{rewardCampaign},
				},
			}}, nil
		},
	}
	refresher, err := NewRefresher(Options{
		GQLClient: client,
		AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 77}},
		Clock:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewRefresher 返回错误: %v", err)
	}

	payload, err := refresher.fetchRewardCampaigns(context.Background(), now)
	if err != nil {
		t.Fatalf("fetchRewardCampaigns 返回错误: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("同一个 reward campaign 在多个字段出现时应去重: %#v", payload)
	}
	if _, ok := payload["reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b"]; !ok {
		t.Fatalf("去重后应保留 Builder Cape: %#v", payload)
	}
}

func TestFetchRewardCampaignsSkipsDuplicateAfterInvalidCampaign(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	invalid := builderCapeRewardCampaignMap()
	invalid["game"] = nil
	valid := builderCapeRewardCampaignMap()

	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName != "ViewerDropsDashboard" {
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
			return gql.Response{Data: map[string]any{
				"rewardCampaignsAvailableToUser": []any{invalid, valid},
			}}, nil
		},
	}
	refresher, err := NewRefresher(Options{
		GQLClient: client,
		AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 77}},
		Clock:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewRefresher 返回错误: %v", err)
	}

	payload, err := refresher.fetchRewardCampaigns(context.Background(), now)
	if err != nil {
		t.Fatalf("fetchRewardCampaigns 返回错误: %v", err)
	}
	if len(payload) != 1 {
		t.Fatalf("无效重复项不应屏蔽后续有效项: %#v", payload)
	}
	if _, ok := payload["reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b"]; !ok {
		t.Fatalf("应保留后续有效 Builder Cape: %#v", payload)
	}
}

func TestRefresherRefreshKeepsNormalCampaignsWhenRewardDashboardDropCampaignsIsNull(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	normal := testCampaignMap(testCampaignInput{
		id:       "campaign-normal",
		name:     "Normal",
		status:   "ACTIVE",
		linked:   true,
		startsAt: now.Add(-time.Hour),
		endsAt:   now.Add(time.Hour),
		game:     testGameMap(100, "Normal Game", "normal-game", "https://static.example.com/game.jpg"),
		drops: []map[string]any{
			testDropMap(testDropInput{
				id:       "drop-normal",
				name:     "Normal Drop",
				startsAt: now.Add(-time.Hour),
				endsAt:   now.Add(time.Hour),
				required: 15,
				benefits: []map[string]any{
					testBenefitMap("benefit-normal", "Normal Reward", "DIRECT_ENTITLEMENT"),
				},
			}),
		},
	})

	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			switch operation.OperationName {
			case "Inventory":
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"inventory": map[string]any{
					"dropCampaignsInProgress": []any{},
					"gameEventDrops":          []any{},
				}}}}, nil
			case "ViewerDropsDashboard":
				if fetchRewardCampaigns, _ := operation.Variables["fetchRewardCampaigns"].(bool); fetchRewardCampaigns {
					return gql.Response{Data: map[string]any{
						"currentUser": map[string]any{"dropCampaigns": nil},
						"rewardCampaignsAvailableToUser": []any{
							builderCapeRewardCampaignMap(),
						},
					}}, nil
				}
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": []any{normal}}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			if len(operations) != 1 {
				return nil, fmt.Errorf("只应为普通 campaign 请求一次 CampaignDetails: %d", len(operations))
			}
			if got := operations[0].Variables["dropID"]; got != "campaign-normal" {
				return nil, fmt.Errorf("CampaignDetails 不应请求 reward campaign: %v", got)
			}
			return []gql.Response{{Data: map[string]any{"user": map[string]any{"dropCampaign": normal}}}}, nil
		},
	}

	refresher, err := NewRefresher(Options{
		GQLClient: client,
		AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 77}},
		Clock:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewRefresher 返回错误: %v", err)
	}

	snapshot, err := refresher.Refresh(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("Refresh 返回错误: %v", err)
	}
	if snapshot.Campaigns["campaign-normal"] == nil {
		t.Fatalf("普通 campaign 不应被 RewardCampaigns 的 dropCampaigns:null 影响: ids=%#v", campaignIDs(snapshot.Inventory))
	}
	if snapshot.Campaigns["reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b"] == nil {
		t.Fatalf("reward campaign 应同时存在: ids=%#v", campaignIDs(snapshot.Inventory))
	}
}
