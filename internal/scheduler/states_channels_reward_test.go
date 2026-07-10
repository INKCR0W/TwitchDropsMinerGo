package scheduler

import (
	"context"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestHandleChannelsFetchUsesUnfilteredDirectoryForRewardCampaign(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 27471, Name: "Minecraft", SlugText: "minecraft"}
	var sawDirectoryRequest bool

	gqlClient := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName != "DirectoryPage_Game" {
				return gql.Response{}, nil
			}
			sawDirectoryRequest = true
			options, ok := operation.Variables["options"].(map[string]any)
			if !ok {
				t.Fatalf("GameDirectory options 类型不匹配: %#v", operation.Variables["options"])
			}
			filters, ok := options["systemFilters"].([]any)
			if !ok {
				t.Fatalf("systemFilters 类型不匹配: %#v", options["systemFilters"])
			}
			if len(filters) != 0 {
				t.Fatalf("reward campaign 目录查询不应要求 DROPS_ENABLED: %#v", filters)
			}
			return gql.Response{
				Data: map[string]any{
					"game": map[string]any{
						"streams": map[string]any{
							"edges": []any{
								map[string]any{
									"node": map[string]any{
										"id":           "2747101",
										"viewersCount": 99,
										"title":        "Minecraft Live",
										"game": map[string]any{
											"id":          "27471",
											"displayName": game.Name,
											"slug":        game.Slug(),
										},
										"broadcaster": map[string]any{
											"id":          "2747101",
											"login":       "minecraft-channel",
											"displayName": "Minecraft Channel",
										},
									},
								},
							},
						},
					},
				},
			}, nil
		},
	}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		gqlClient: gqlClient,
	})
	scheduler.state = StateChannelsFetch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, domain.CampaignSpec{
			ID:               "reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b",
			Name:             "Builder Cape",
			Game:             game,
			Linked:           true,
			Status:           "ACTIVE",
			StartsAt:         now.Add(-time.Hour),
			EndsAt:           now.Add(time.Hour),
			IsRewardCampaign: true,
			Drops: []domain.TimedDropSpec{
				{
					ID:              "reward:8659c1c1-5926-11f1-a66f-0a58a9feac02",
					Name:            "Builder Cape",
					StartsAt:        now.Add(-time.Hour),
					EndsAt:          now.Add(time.Hour),
					RequiredMinutes: 5,
					Benefits: []domain.Benefit{
						{ID: "8659c1c1-5926-11f1-a66f-0a58a9feac02", Name: "Builder Cape", Type: domain.BenefitTypeDirectEntitlement},
					},
				},
			},
		}),
	)

	if err := scheduler.handleChannelsFetch(context.Background()); err != nil {
		t.Fatalf("handleChannelsFetch 返回错误: %v", err)
	}
	if !sawDirectoryRequest {
		t.Fatal("reward campaign 应触发游戏目录查询")
	}

	channel, ok := scheduler.channels[2747101]
	if !ok {
		t.Fatalf("reward campaign 目录频道应被加入调度列表: %#v", scheduler.channels)
	}
	if channel.Stream == nil {
		t.Fatalf("目录频道应在线: %#v", channel)
	}
	if channel.Stream.DropsEnabled {
		t.Fatal("未使用 DROPS_ENABLED 过滤时频道不应被标记为普通 drops enabled")
	}
	if !scheduler.canWatch(channel) {
		t.Fatalf("reward campaign 应允许观看同游戏但非 DROPS_ENABLED 的频道: %#v", channel)
	}
}

func TestHandleChannelsFetchPreservesDropsEnabledWhenRewardDirectoryDuplicatesNormalChannel(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 27471, Name: "Minecraft", SlugText: "minecraft"}
	var requestCount int

	gqlClient := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName != "DirectoryPage_Game" {
				return gql.Response{}, nil
			}
			requestCount++
			return gql.Response{
				Data: map[string]any{
					"game": map[string]any{
						"streams": map[string]any{
							"edges": []any{
								map[string]any{
									"node": map[string]any{
										"id":           "2747101",
										"viewersCount": 99,
										"title":        "Minecraft Live",
										"game": map[string]any{
											"id":          "27471",
											"displayName": game.Name,
											"slug":        game.Slug(),
										},
										"broadcaster": map[string]any{
											"id":          "2747101",
											"login":       "minecraft-channel",
											"displayName": "Minecraft Channel",
										},
									},
								},
							},
						},
					},
				},
			}, nil
		},
	}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		gqlClient: gqlClient,
	})
	scheduler.state = StateChannelsFetch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-normal", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
		mustCampaign(t, domain.CampaignSpec{
			ID:               "reward:a62275d9-9fa6-43b8-9020-6ea9ebe4114b",
			Name:             "Builder Cape",
			Game:             game,
			Linked:           true,
			Status:           "ACTIVE",
			StartsAt:         now.Add(-time.Hour),
			EndsAt:           now.Add(time.Hour),
			IsRewardCampaign: true,
			Drops: []domain.TimedDropSpec{
				{
					ID:              "reward:8659c1c1-5926-11f1-a66f-0a58a9feac02",
					Name:            "Builder Cape",
					StartsAt:        now.Add(-time.Hour),
					EndsAt:          now.Add(time.Hour),
					RequiredMinutes: 5,
					Benefits: []domain.Benefit{
						{ID: "8659c1c1-5926-11f1-a66f-0a58a9feac02", Name: "Builder Cape", Type: domain.BenefitTypeDirectEntitlement},
					},
				},
			},
		}),
	)

	if err := scheduler.handleChannelsFetch(context.Background()); err != nil {
		t.Fatalf("handleChannelsFetch 返回错误: %v", err)
	}
	if requestCount != 2 {
		t.Fatalf("应分别查询普通 drops 目录和 reward 目录: %d", requestCount)
	}

	channel := scheduler.channels[2747101]
	if channel.Stream == nil {
		t.Fatalf("目录频道应在线: %#v", channel)
	}
	if !channel.Stream.DropsEnabled {
		t.Fatal("reward 无过滤目录不应覆盖同频道已知的 DROPS_ENABLED=true")
	}
}
