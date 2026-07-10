package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"twitchdropsminergo/internal/gql"
)

func snapshotFixtureClient(t *testing.T, now time.Time) *fakeGQLClient {
	t.Helper()

	return &fakeGQLClient{
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
		doBatchFunc: snapshotFixtureBatch(t, now),
	}
}
