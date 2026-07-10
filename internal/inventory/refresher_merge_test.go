package inventory

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/gql"
)

func TestRefresherRefreshMergesNullableCampaignFields(t *testing.T) {
	t.Parallel()

	now := testNow()
	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			switch operation.OperationName {
			case "Inventory":
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"inventory": map[string]any{
					"dropCampaignsInProgress": []any{},
					"gameEventDrops":          []any{},
				}}}}, nil
			case "ViewerDropsDashboard":
				campaign := testCampaignMap(testCampaignInput{
					id:       "campaign-nullable",
					name:     "Nullable",
					status:   "ACTIVE",
					linked:   true,
					startsAt: now.Add(-time.Hour),
					endsAt:   now.Add(time.Hour),
					game:     nil,
					allow:    nil,
					drops:    nil,
				})
				campaign["game"] = nil
				campaign["allow"] = nil
				campaign["timeBasedDrops"] = nil
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": []any{campaign}}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			return []gql.Response{{
				Data: map[string]any{"user": map[string]any{"dropCampaign": testCampaignMap(testCampaignInput{
					id:       "campaign-nullable",
					name:     "Nullable",
					status:   "ACTIVE",
					linked:   true,
					startsAt: now.Add(-time.Hour),
					endsAt:   now.Add(time.Hour),
					game:     testGameMap(100, "Game", "game", "https://static.example.com/game.jpg"),
					allow:    map[string]any{"isEnabled": true, "channels": []any{}},
					drops: []map[string]any{
						testDropMap(testDropInput{
							id:       "drop-nullable",
							name:     "Drop",
							startsAt: now.Add(-time.Hour),
							endsAt:   now.Add(time.Hour),
							required: 15,
							benefits: []map[string]any{
								testBenefitMap("benefit-nullable", "Reward", "DIRECT_ENTITLEMENT"),
							},
						}),
					},
				})}},
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

	snapshot, err := refresher.Refresh(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("Refresh 不应因 nullable merge 失败: %v", err)
	}
	if len(snapshot.Inventory) != 1 {
		t.Fatalf("inventory 数量不匹配: %d", len(snapshot.Inventory))
	}
}

func TestRefresherRefreshSkipsNullCampaignItems(t *testing.T) {
	t.Parallel()

	now := testNow()
	valid := testCampaignMap(testCampaignInput{
		id:       "campaign-valid",
		name:     "Valid",
		status:   "ACTIVE",
		linked:   true,
		startsAt: now.Add(-time.Hour),
		endsAt:   now.Add(time.Hour),
		game:     testGameMap(101, "Game", "game", "https://static.example.com/game.jpg"),
		drops: []map[string]any{
			testDropMap(testDropInput{
				id:       "drop-valid",
				name:     "Drop",
				startsAt: now.Add(-time.Hour),
				endsAt:   now.Add(time.Hour),
				required: 15,
				benefits: []map[string]any{
					testBenefitMap("benefit-valid", "Reward", "DIRECT_ENTITLEMENT"),
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
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": []any{nil, valid}}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			return []gql.Response{{Data: map[string]any{"user": map[string]any{"dropCampaign": valid}}}}, nil
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
		t.Fatalf("Refresh 不应因 null item 失败: %v", err)
	}
	if got := campaignIDs(snapshot.Inventory); !slices.Equal(got, []string{"campaign-valid"}) {
		t.Fatalf("campaign IDs 不匹配: %#v", got)
	}
}

func TestRefresherRefreshTreatsNullCampaignListAsEmpty(t *testing.T) {
	t.Parallel()

	now := testNow()
	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			switch operation.OperationName {
			case "Inventory":
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"inventory": map[string]any{
					"dropCampaignsInProgress": []any{},
					"gameEventDrops":          []any{},
				}}}}, nil
			case "ViewerDropsDashboard":
				return gql.Response{Data: map[string]any{"currentUser": map[string]any{"dropCampaigns": nil}}}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			return nil, fmt.Errorf("null dropCampaigns 不应请求 CampaignDetails")
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
		t.Fatalf("Refresh 不应因 null dropCampaigns 失败: %v", err)
	}
	if len(snapshot.Inventory) != 0 {
		t.Fatalf("inventory 应为空: %d", len(snapshot.Inventory))
	}
}
