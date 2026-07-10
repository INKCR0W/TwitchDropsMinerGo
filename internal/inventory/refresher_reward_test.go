package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/rewards"
)

func TestRefresherRefreshMergesRewardCampaignsWithoutCampaignDetails(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	var rewardCampaignsRequested bool
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
					rewardCampaignsRequested = true
					return gql.Response{Data: map[string]any{
						"currentUser": map[string]any{"dropCampaigns": nil},
						"rewardCampaignsAvailableToUser": []any{
							builderCapeRewardCampaignMap(),
						},
					}}, nil
				}
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": []any{}}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			return nil, fmt.Errorf("reward campaigns 不应请求 CampaignDetails: %#v", operations)
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
	if !rewardCampaignsRequested {
		t.Fatal("Refresh 应额外请求 RewardCampaigns")
	}

	campaign := snapshot.Campaigns["reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b"]
	if campaign == nil {
		t.Fatalf("snapshot 应包含转换后的 Builder Cape: ids=%#v", campaignIDs(snapshot.Inventory))
	} else if !campaign.IsRewardCampaign {
		t.Fatal("转换后的 Builder Cape campaign 应保留 reward 标记")
	} else if campaign.Game.ID != 27471 || campaign.Game.Name != "Minecraft" {
		t.Fatalf("Builder Cape game 不匹配: %#v", campaign.Game)
	} else {
		drop := campaign.Drop("reward:8659c1c1-5926-11f1-a66f-0a58a9feac02")
		if drop == nil {
			t.Fatal("Builder Cape 应生成 reward: 前缀伪 drop")
		} else if drop.RequiredMinutes != 5 {
			t.Fatalf("Builder Cape required minutes 不匹配: %d", drop.RequiredMinutes)
		}
	}
}

func TestRefresherRefreshSkipsLocallyCompletedRewardCampaigns(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
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
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": []any{}}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			return nil, fmt.Errorf("reward campaigns 不应请求 CampaignDetails: %#v", operations)
		},
	}

	refresher, err := NewRefresher(Options{
		GQLClient: client,
		AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 77}},
		Clock:     func() time.Time { return now },
		RewardProgress: map[string]rewards.Progress{
			"reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b": {
				CampaignID:     "reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b",
				DropID:         "reward:8659c1c1-5926-11f1-a66f-0a58a9feac02",
				MinutesWatched: 5,
				CompletedAt:    now.Add(-time.Minute),
				UpdatedAt:      now.Add(-time.Minute),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRefresher 返回错误: %v", err)
	}

	snapshot, err := refresher.Refresh(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("Refresh 返回错误: %v", err)
	}
	if len(snapshot.Inventory) != 0 {
		t.Fatalf("已本地完成的 reward campaign 不应进入 inventory: ids=%#v", campaignIDs(snapshot.Inventory))
	}
}

func TestRefresherRefreshRestoresRewardCampaignLocalMinutes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
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
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": []any{}}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			return nil, fmt.Errorf("reward campaigns 不应请求 CampaignDetails: %#v", operations)
		},
	}

	refresher, err := NewRefresher(Options{
		GQLClient: client,
		AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 77}},
		Clock:     func() time.Time { return now },
		RewardProgress: map[string]rewards.Progress{
			"reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b": {
				CampaignID:     "reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b",
				DropID:         "reward:8659c1c1-5926-11f1-a66f-0a58a9feac02",
				MinutesWatched: 3,
				UpdatedAt:      now.Add(-time.Minute),
			},
		},
	})
	if err != nil {
		t.Fatalf("NewRefresher 返回错误: %v", err)
	}

	snapshot, err := refresher.Refresh(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("Refresh 返回错误: %v", err)
	}
	campaign := snapshot.Campaigns["reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b"]
	if campaign == nil {
		t.Fatalf("未完成 reward campaign 应继续进入 inventory: ids=%#v", campaignIDs(snapshot.Inventory))
	}
	drop := campaign.Drop("reward:8659c1c1-5926-11f1-a66f-0a58a9feac02")
	if drop == nil || drop.CurrentMinutes() != 3 {
		t.Fatalf("reward 本地分钟未恢复: %#v", drop)
	}
}

func TestRefresherUpdateRewardProgressCanRunDuringRefresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
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
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": []any{}}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			return nil, fmt.Errorf("reward campaigns 不应请求 CampaignDetails: %#v", operations)
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

	for i := 0; i < 100; i++ {
		done := make(chan error, 1)
		go func() {
			_, refreshErr := refresher.Refresh(context.Background(), RefreshOptions{})
			done <- refreshErr
		}()

		refresher.UpdateRewardProgress(map[string]rewards.Progress{
			"reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b": {
				CampaignID:     "reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b",
				DropID:         "reward:8659c1c1-5926-11f1-a66f-0a58a9feac02",
				MinutesWatched: i % 5,
				UpdatedAt:      now.Add(time.Duration(i) * time.Second),
			},
		})

		if err := <-done; err != nil {
			t.Fatalf("Refresh 返回错误: %v", err)
		}
	}
}
