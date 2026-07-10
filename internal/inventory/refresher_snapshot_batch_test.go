package inventory

import (
	"context"
	"testing"
	"time"

	"twitchdropsminergo/internal/gql"
)

func snapshotFixtureBatch(t *testing.T, now time.Time) func(context.Context, []gql.Operation) ([]gql.Response, error) {
	t.Helper()

	return func(_ context.Context, operations []gql.Operation) ([]gql.Response, error) {
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
	}
}
