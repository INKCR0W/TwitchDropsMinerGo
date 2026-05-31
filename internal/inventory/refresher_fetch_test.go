package inventory

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/gql"
)

func TestRefresherRefreshFetchesCampaignDetailsInConcurrentChunks(t *testing.T) {
	t.Parallel()

	now := testNow()
	var maxConcurrent atomic.Int32
	var activeBatches atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once

	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			switch operation.OperationName {
			case "Inventory":
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"inventory": map[string]any{
								"dropCampaignsInProgress": []any{},
								"gameEventDrops":          []any{},
							},
						},
					},
				}, nil
			case "ViewerDropsDashboard":
				campaigns := make([]any, 0, 25)
				for i := range 25 {
					campaigns = append(campaigns, testCampaignMap(testCampaignInput{
						id:       fmt.Sprintf("campaign-%02d", i),
						name:     fmt.Sprintf("Campaign %02d", i),
						status:   "ACTIVE",
						linked:   true,
						startsAt: now.Add(-time.Hour),
						endsAt:   now.Add(time.Duration(i+1) * time.Hour),
						game:     testGameMap(int64(200+i), fmt.Sprintf("Game %02d", i), fmt.Sprintf("game-%02d", i), fmt.Sprintf("https://static.example.com/game-%02d-285x380.jpg", i)),
						drops:    []map[string]any{},
					}))
				}
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"dropCampaigns": campaigns,
						},
					},
				}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			current := activeBatches.Add(1)
			for {
				maxSeen := maxConcurrent.Load()
				if current <= maxSeen || maxConcurrent.CompareAndSwap(maxSeen, current) {
					break
				}
			}
			if current >= 2 {
				releaseOnce.Do(func() {
					close(release)
				})
			}

			select {
			case <-release:
			case <-time.After(200 * time.Millisecond):
			}
			time.Sleep(20 * time.Millisecond)

			activeBatches.Add(-1)

			responses := make([]gql.Response, 0, len(operations))
			for _, operation := range operations {
				if got := operation.Variables["channelLogin"]; got != "77" {
					t.Fatalf("channelLogin 变量不匹配: %v", got)
				}
				campaignID := operation.Variables["dropID"].(string)
				responses = append(responses, gql.Response{
					Data: map[string]any{
						"user": map[string]any{
							"dropCampaign": testCampaignMap(testCampaignInput{
								id:       campaignID,
								name:     campaignID,
								status:   "ACTIVE",
								linked:   true,
								startsAt: now.Add(-time.Hour),
								endsAt:   now.Add(2 * time.Hour),
								game:     testGameMap(999, "Game", "game", "https://static.example.com/game-285x380.jpg"),
								drops: []map[string]any{
									testDropMap(testDropInput{
										id:       "drop-" + campaignID,
										name:     "Drop " + campaignID,
										startsAt: now.Add(-time.Hour),
										endsAt:   now.Add(2 * time.Hour),
										required: 15,
										benefits: []map[string]any{
											testBenefitMap("benefit-"+campaignID, "Reward "+campaignID, "DIRECT_ENTITLEMENT"),
										},
									}),
								},
							}),
						},
					},
				})
			}
			return responses, nil
		},
	}

	refresher, err := NewRefresher(Options{
		GQLClient: client,
		AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 77}},
		Clock:     func() time.Time { return now },
		ChunkSize: 20,
	})
	if err != nil {
		t.Fatalf("NewRefresher 返回错误: %v", err)
	}

	snapshot, err := refresher.Refresh(context.Background(), RefreshOptions{})
	if err != nil {
		t.Fatalf("Refresh 返回错误: %v", err)
	}

	if len(snapshot.Inventory) != 25 {
		t.Fatalf("inventory 数量不匹配: %d", len(snapshot.Inventory))
	}

	batchSizes := append([]int(nil), client.batchSizes...)
	sort.Ints(batchSizes)
	if !slices.Equal(batchSizes, []int{5, 20}) {
		t.Fatalf("CampaignDetails 分块大小不匹配: %#v", batchSizes)
	}
	if maxConcurrent.Load() < 2 {
		t.Fatalf("CampaignDetails 应并发抓取至少两个分块，maxConcurrent=%d", maxConcurrent.Load())
	}
}

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
