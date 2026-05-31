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
	} else if !activeCampaign.Linked {
		t.Fatal("inventory 主数据应覆盖 details 中的 linked=false")
	} else if activeCampaign.ImageURL != "https://static.example.com/game-alpha.jpg" {
		t.Fatalf("boxArtURL 去尺寸失败: %q", activeCampaign.ImageURL)
	} else if len(activeCampaign.AllowedChannels) != 1 || !activeCampaign.AllowedChannels[0].ACLBased {
		t.Fatalf("ACL 频道映射不正确: %#v", activeCampaign.AllowedChannels)
	}

	activeDrop := snapshot.Drops["drop-active"]
	if activeDrop == nil {
		t.Fatal("drop-active 未写入 drop 索引")
	} else if activeDrop.RealCurrentMinutes != 12 {
		t.Fatalf("inventory 主数据的 currentMinutesWatched 应保留: %d", activeDrop.RealCurrentMinutes)
	} else if activeDrop.ClaimID != "claim-active" {
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
