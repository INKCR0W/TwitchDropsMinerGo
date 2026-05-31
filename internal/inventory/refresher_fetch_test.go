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
	}
	if !campaign.IsRewardCampaign {
		t.Fatal("转换后的 Builder Cape campaign 应保留 reward 标记")
	}
	if campaign.Game.ID != 27471 || campaign.Game.Name != "Minecraft" {
		t.Fatalf("Builder Cape game 不匹配: %#v", campaign.Game)
	}
	drop := campaign.Drop("reward:8659c1c1-5926-11f1-a66f-0a58a9feac02")
	if drop == nil {
		t.Fatal("Builder Cape 应生成 reward: 前缀伪 drop")
	}
	if drop.RequiredMinutes != 5 {
		t.Fatalf("Builder Cape required minutes 不匹配: %d", drop.RequiredMinutes)
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
