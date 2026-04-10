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
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestRefresherRefreshBuildsInventoryIndicesAndTriggers(t *testing.T) {
	t.Parallel()

	now := testNow()
	client := &fakeGQLClient{
		doFunc: func(_ context.Context, operation gql.Operation) (gql.Response, error) {
			switch operation.OperationName {
			case "Inventory":
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"inventory": map[string]any{
								"dropCampaignsInProgress": []any{
									testCampaignMap(testCampaignInput{
										id:       "campaign-active",
										name:     "Active",
										status:   "ACTIVE",
										linked:   true,
										startsAt: now.Add(-2 * time.Hour),
										endsAt:   now.Add(3 * time.Hour),
										game:     testGameMap(101, "Game Alpha", "game-alpha", "https://static.example.com/game-alpha-285x380.jpg"),
										allow: map[string]any{
											"isEnabled": true,
											"channels": []any{
												map[string]any{
													"id":          "9001",
													"name":        "allowed_channel",
													"displayName": "Allowed Channel",
												},
											},
										},
										drops: []map[string]any{
											testDropMap(testDropInput{
												id:             "drop-active",
												name:           "Drop Active",
												startsAt:       now.Add(-2 * time.Hour),
												endsAt:         now.Add(45 * time.Minute),
												required:       15,
												currentMinutes: 12,
												claimID:        "claim-active",
												benefits: []map[string]any{
													testBenefitMap("benefit-active", "Reward Active", "DIRECT_ENTITLEMENT"),
												},
											}),
											testDropMap(testDropInput{
												id:       "drop-future",
												name:     "Drop Future",
												startsAt: now.Add(30 * time.Minute),
												endsAt:   now.Add(2 * time.Hour),
												required: 20,
												benefits: []map[string]any{
													testBenefitMap("benefit-future", "Reward Future", "DIRECT_ENTITLEMENT"),
												},
												preconditions: []string{"drop-active"},
											}),
										},
									}),
								},
								"gameEventDrops": []any{
									map[string]any{
										"id":            "benefit-claimed",
										"lastAwardedAt": formatTime(now.Add(-20 * time.Minute)),
									},
								},
							},
						},
					},
				}, nil
			case "ViewerDropsDashboard":
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"dropCampaigns": []any{
								testCampaignMap(testCampaignInput{
									id:       "campaign-active",
									name:     "Active",
									status:   "ACTIVE",
									linked:   false,
									startsAt: now.Add(-2 * time.Hour),
									endsAt:   now.Add(3 * time.Hour),
									game:     testGameMap(101, "Game Alpha", "game-alpha", "https://static.example.com/game-alpha-285x380.jpg"),
									drops: []map[string]any{
										testDropMap(testDropInput{
											id:             "drop-active",
											name:           "Drop Active",
											startsAt:       now.Add(-2 * time.Hour),
											endsAt:         now.Add(45 * time.Minute),
											required:       15,
											currentMinutes: 1,
											claimID:        "claim-from-details",
											benefits: []map[string]any{
												testBenefitMap("benefit-active", "Reward Active", "DIRECT_ENTITLEMENT"),
											},
										}),
										testDropMap(testDropInput{
											id:       "drop-future",
											name:     "Drop Future",
											startsAt: now.Add(30 * time.Minute),
											endsAt:   now.Add(2 * time.Hour),
											required: 20,
											benefits: []map[string]any{
												testBenefitMap("benefit-future", "Reward Future", "DIRECT_ENTITLEMENT"),
											},
											preconditions: []string{"drop-active"},
										}),
									},
								}),
								testCampaignMap(testCampaignInput{
									id:       "campaign-claimed",
									name:     "Claimed",
									status:   "ACTIVE",
									linked:   true,
									startsAt: now.Add(-90 * time.Minute),
									endsAt:   now.Add(90 * time.Minute),
									game:     testGameMap(102, "Game Beta", "game-beta", "https://static.example.com/game-beta-285x380.jpg"),
									drops: []map[string]any{
										testDropMap(testDropInput{
											id:       "drop-claimed",
											name:     "Drop Claimed",
											startsAt: now.Add(-45 * time.Minute),
											endsAt:   now.Add(45 * time.Minute),
											required: 20,
											benefits: []map[string]any{
												testBenefitMap("benefit-claimed", "Reward Claimed", "DIRECT_ENTITLEMENT"),
											},
										}),
									},
								}),
								testCampaignMap(testCampaignInput{
									id:       "campaign-upcoming",
									name:     "Upcoming",
									status:   "UPCOMING",
									linked:   true,
									startsAt: now.Add(2 * time.Hour),
									endsAt:   now.Add(5 * time.Hour),
									game:     testGameMap(103, "Game Gamma", "game-gamma", "https://static.example.com/game-gamma-285x380.jpg"),
									drops: []map[string]any{
										testDropMap(testDropInput{
											id:       "drop-upcoming",
											name:     "Drop Upcoming",
											startsAt: now.Add(2 * time.Hour),
											endsAt:   now.Add(5 * time.Hour),
											required: 30,
											benefits: []map[string]any{
												testBenefitMap("benefit-upcoming", "Reward Upcoming", "DIRECT_ENTITLEMENT"),
											},
										}),
									},
								}),
								testCampaignMap(testCampaignInput{
									id:       "campaign-invalid",
									name:     "Invalid",
									status:   "ACTIVE",
									linked:   true,
									startsAt: now.Add(-time.Hour),
									endsAt:   now.Add(2 * time.Hour),
									game:     nil,
									drops: []map[string]any{
										testDropMap(testDropInput{
											id:       "drop-invalid",
											name:     "Drop Invalid",
											startsAt: now.Add(-time.Hour),
											endsAt:   now.Add(time.Hour),
											required: 10,
											benefits: []map[string]any{
												testBenefitMap("benefit-invalid", "Reward Invalid", "DIRECT_ENTITLEMENT"),
											},
										}),
									},
								}),
								testCampaignMap(testCampaignInput{
									id:       "campaign-expired",
									name:     "Expired",
									status:   "EXPIRED",
									linked:   true,
									startsAt: now.Add(-4 * time.Hour),
									endsAt:   now.Add(-time.Hour),
									game:     testGameMap(104, "Game Expired", "game-expired", "https://static.example.com/game-expired-285x380.jpg"),
									drops:    []map[string]any{},
								}),
							},
						},
					},
				}, nil
			default:
				return gql.Response{}, fmt.Errorf("收到意外单请求操作: %s", operation.OperationName)
			}
		},
		doBatchFunc: func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
			responses := make([]gql.Response, 0, len(operations))
			for _, operation := range operations {
				dropID := operation.Variables["dropID"].(string)
				channelLogin := operation.Variables["channelLogin"].(string)
				if channelLogin != "42" {
					t.Fatalf("CampaignDetails 的 channelLogin 应使用 user_id: %q", channelLogin)
				}

				var campaign map[string]any
				switch dropID {
				case "campaign-active":
					campaign = testCampaignMap(testCampaignInput{
						id:       "campaign-active",
						name:     "Active",
						status:   "ACTIVE",
						linked:   false,
						startsAt: now.Add(-2 * time.Hour),
						endsAt:   now.Add(3 * time.Hour),
						game:     testGameMap(101, "Game Alpha", "game-alpha", "https://static.example.com/game-alpha-285x380.jpg"),
						allow: map[string]any{
							"isEnabled": true,
							"channels": []any{
								map[string]any{
									"id":          "9001",
									"name":        "allowed_channel",
									"displayName": "Allowed Channel",
								},
							},
						},
						drops: []map[string]any{
							testDropMap(testDropInput{
								id:             "drop-active",
								name:           "Drop Active",
								startsAt:       now.Add(-2 * time.Hour),
								endsAt:         now.Add(45 * time.Minute),
								required:       15,
								currentMinutes: 1,
								claimID:        "claim-from-details",
								benefits: []map[string]any{
									testBenefitMap("benefit-active", "Reward Active", "DIRECT_ENTITLEMENT"),
								},
							}),
							testDropMap(testDropInput{
								id:       "drop-future",
								name:     "Drop Future",
								startsAt: now.Add(30 * time.Minute),
								endsAt:   now.Add(2 * time.Hour),
								required: 20,
								benefits: []map[string]any{
									testBenefitMap("benefit-future", "Reward Future", "DIRECT_ENTITLEMENT"),
								},
								preconditions: []string{"drop-active"},
							}),
						},
					})
				case "campaign-claimed":
					campaign = testCampaignMap(testCampaignInput{
						id:       "campaign-claimed",
						name:     "Claimed",
						status:   "ACTIVE",
						linked:   true,
						startsAt: now.Add(-90 * time.Minute),
						endsAt:   now.Add(90 * time.Minute),
						game:     testGameMap(102, "Game Beta", "game-beta", "https://static.example.com/game-beta-285x380.jpg"),
						drops: []map[string]any{
							testDropMap(testDropInput{
								id:       "drop-claimed",
								name:     "Drop Claimed",
								startsAt: now.Add(-45 * time.Minute),
								endsAt:   now.Add(45 * time.Minute),
								required: 20,
								benefits: []map[string]any{
									testBenefitMap("benefit-claimed", "Reward Claimed", "DIRECT_ENTITLEMENT"),
								},
							}),
						},
					})
				case "campaign-upcoming":
					campaign = testCampaignMap(testCampaignInput{
						id:       "campaign-upcoming",
						name:     "Upcoming",
						status:   "UPCOMING",
						linked:   true,
						startsAt: now.Add(2 * time.Hour),
						endsAt:   now.Add(5 * time.Hour),
						game:     testGameMap(103, "Game Gamma", "game-gamma", "https://static.example.com/game-gamma-285x380.jpg"),
						drops: []map[string]any{
							testDropMap(testDropInput{
								id:       "drop-upcoming",
								name:     "Drop Upcoming",
								startsAt: now.Add(2 * time.Hour),
								endsAt:   now.Add(5 * time.Hour),
								required: 30,
								benefits: []map[string]any{
									testBenefitMap("benefit-upcoming", "Reward Upcoming", "DIRECT_ENTITLEMENT"),
								},
							}),
						},
					})
				case "campaign-invalid":
					campaign = testCampaignMap(testCampaignInput{
						id:       "campaign-invalid",
						name:     "Invalid",
						status:   "ACTIVE",
						linked:   true,
						startsAt: now.Add(-time.Hour),
						endsAt:   now.Add(2 * time.Hour),
						game:     nil,
						drops: []map[string]any{
							testDropMap(testDropInput{
								id:       "drop-invalid",
								name:     "Drop Invalid",
								startsAt: now.Add(-time.Hour),
								endsAt:   now.Add(time.Hour),
								required: 10,
								benefits: []map[string]any{
									testBenefitMap("benefit-invalid", "Reward Invalid", "DIRECT_ENTITLEMENT"),
								},
							}),
						},
					})
				default:
					t.Fatalf("收到未知 campaign id: %s", dropID)
				}

				responses = append(responses, gql.Response{
					Data: map[string]any{
						"user": map[string]any{
							"dropCampaign": campaign,
						},
					},
				})
			}
			return responses, nil
		},
	}

	refresher, err := NewRefresher(Options{
		GQLClient: client,
		AuthState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 42}},
		Clock:     func() time.Time { return now },
		ChunkSize: 2,
	})
	if err != nil {
		t.Fatalf("NewRefresher 返回错误: %v", err)
	}

	snapshot, err := refresher.Refresh(context.Background(), RefreshOptions{
		EnableBadgesEmotes: false,
	})
	if err != nil {
		t.Fatalf("Refresh 返回错误: %v", err)
	}

	if got := campaignIDs(snapshot.Inventory); !slices.Equal(got, []string{"campaign-claimed", "campaign-upcoming", "campaign-active"}) {
		t.Fatalf("inventory 排序或过滤不符合预期: %#v", got)
	}
	if _, exists := snapshot.Campaigns["campaign-invalid"]; exists {
		t.Fatal("game 为空的 campaign 应被过滤")
	}
	if _, exists := snapshot.Campaigns["campaign-expired"]; exists {
		t.Fatal("EXPIRED campaign 不应进入 details/最终 inventory")
	}
	if len(snapshot.Drops) != 4 {
		t.Fatalf("drop 索引数量不匹配: %d", len(snapshot.Drops))
	}

	activeCampaign := snapshot.Campaigns["campaign-active"]
	if activeCampaign == nil {
		t.Fatal("campaign-active 未写入 campaign 索引")
	}
	if !activeCampaign.Linked {
		t.Fatal("inventory 主数据应覆盖 details 中的 linked=false")
	}
	if activeCampaign.ImageURL != "https://static.example.com/game-alpha.jpg" {
		t.Fatalf("boxArtURL 去尺寸失败: %q", activeCampaign.ImageURL)
	}
	if len(activeCampaign.AllowedChannels) != 1 || !activeCampaign.AllowedChannels[0].ACLBased {
		t.Fatalf("ACL 频道映射不正确: %#v", activeCampaign.AllowedChannels)
	}

	activeDrop := snapshot.Drops["drop-active"]
	if activeDrop == nil {
		t.Fatal("drop-active 未写入 drop 索引")
	}
	if activeDrop.RealCurrentMinutes != 12 {
		t.Fatalf("inventory 主数据的 currentMinutesWatched 应保留: %d", activeDrop.RealCurrentMinutes)
	}
	if activeDrop.ClaimID != "claim-active" {
		t.Fatalf("inventory 主数据的 claim_id 应保留: %q", activeDrop.ClaimID)
	}

	claimedDrop := snapshot.Drops["drop-claimed"]
	if claimedDrop == nil || !claimedDrop.IsClaimed {
		t.Fatalf("claimed_benefits 未能推断已领取掉宝: %#v", claimedDrop)
	}
	if claimedDrop.RealCurrentMinutes != claimedDrop.RequiredMinutes {
		t.Fatalf("已领取掉宝应归一化为满进度: current=%d required=%d", claimedDrop.RealCurrentMinutes, claimedDrop.RequiredMinutes)
	}

	expectedTriggers := []time.Time{
		now.Add(30 * time.Minute),
		now.Add(45 * time.Minute),
		now.Add(2 * time.Hour),
		now.Add(3 * time.Hour),
	}
	if !slices.Equal(snapshot.MaintenanceTriggers, expectedTriggers) {
		t.Fatalf("maintenance triggers 不匹配:\n got=%v\nwant=%v", snapshot.MaintenanceTriggers, expectedTriggers)
	}
}

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

type fakeGQLClient struct {
	mu          sync.Mutex
	doFunc      func(context.Context, gql.Operation) (gql.Response, error)
	doBatchFunc func(context.Context, []gql.Operation) ([]gql.Response, error)
	batchSizes  []int
}

func (f *fakeGQLClient) Do(ctx context.Context, operation gql.Operation) (gql.Response, error) {
	if f.doFunc == nil {
		return gql.Response{}, fmt.Errorf("缺少 Do 模拟实现")
	}
	return f.doFunc(ctx, operation)
}

func (f *fakeGQLClient) DoBatch(ctx context.Context, operations []gql.Operation) ([]gql.Response, error) {
	f.mu.Lock()
	f.batchSizes = append(f.batchSizes, len(operations))
	f.mu.Unlock()

	if f.doBatchFunc == nil {
		return nil, fmt.Errorf("缺少 DoBatch 模拟实现")
	}
	return f.doBatchFunc(ctx, operations)
}

type fakeAuthState struct {
	validateErr   error
	validateCalls int
	snapshot      auth.Snapshot
}

func (f *fakeAuthState) Validate(context.Context) error {
	f.validateCalls++
	return f.validateErr
}

func (f *fakeAuthState) Snapshot() auth.Snapshot {
	return f.snapshot
}

type testCampaignInput struct {
	id       string
	name     string
	status   string
	linked   bool
	startsAt time.Time
	endsAt   time.Time
	game     map[string]any
	allow    map[string]any
	drops    []map[string]any
}

func testCampaignMap(input testCampaignInput) map[string]any {
	allow := input.allow
	if allow == nil {
		allow = map[string]any{
			"isEnabled": true,
			"channels":  []any{},
		}
	}

	return map[string]any{
		"id":             input.id,
		"name":           input.name,
		"status":         input.status,
		"startAt":        formatTime(input.startsAt),
		"endAt":          formatTime(input.endsAt),
		"game":           input.game,
		"self":           map[string]any{"isAccountConnected": input.linked},
		"accountLinkURL": "https://www.twitch.tv/drops/campaigns/" + input.id,
		"allow":          allow,
		"timeBasedDrops": input.drops,
	}
}

type testDropInput struct {
	id             string
	name           string
	startsAt       time.Time
	endsAt         time.Time
	required       int
	currentMinutes int
	claimID        string
	isClaimed      bool
	benefits       []map[string]any
	preconditions  []string
}

func testDropMap(input testDropInput) map[string]any {
	drop := map[string]any{
		"id":                     input.id,
		"name":                   input.name,
		"startAt":                formatTime(input.startsAt),
		"endAt":                  formatTime(input.endsAt),
		"requiredMinutesWatched": input.required,
		"benefitEdges":           edgesFromBenefits(input.benefits),
	}
	if len(input.preconditions) > 0 {
		preconditions := make([]any, 0, len(input.preconditions))
		for _, dropID := range input.preconditions {
			preconditions = append(preconditions, map[string]any{"id": dropID})
		}
		drop["preconditionDrops"] = preconditions
	}
	if input.claimID != "" || input.currentMinutes > 0 || input.isClaimed {
		drop["self"] = map[string]any{
			"dropInstanceID":        input.claimID,
			"isClaimed":             input.isClaimed,
			"currentMinutesWatched": input.currentMinutes,
		}
	}
	return drop
}

func testBenefitMap(id string, name string, distributionType string) map[string]any {
	return map[string]any{
		"benefit": map[string]any{
			"id":               id,
			"name":             name,
			"distributionType": distributionType,
			"imageAssetURL":    "https://static.example.com/" + id + ".png",
		},
	}
}

func edgesFromBenefits(benefits []map[string]any) []any {
	result := make([]any, 0, len(benefits))
	for _, benefit := range benefits {
		result = append(result, benefit)
	}
	return result
}

func testGameMap(id int64, name string, slug string, boxArtURL string) map[string]any {
	return map[string]any{
		"id":          fmt.Sprintf("%d", id),
		"name":        name,
		"displayName": name,
		"slug":        slug,
		"boxArtURL":   boxArtURL,
	}
}

func campaignIDs(campaigns []*domain.DropsCampaign) []string {
	ids := make([]string, 0, len(campaigns))
	for _, campaign := range campaigns {
		if campaign == nil {
			continue
		}
		ids = append(ids, campaign.ID)
	}
	return ids
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func testNow() time.Time {
	return time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
}
