package scheduler

import (
	"context"
	"slices"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestHandleChannelsCleanupRemovesOfflineAndUnwantedNonACLChannels(t *testing.T) {
	t.Parallel()

	now := testTime()
	wanted := domain.Game{ID: 1, Name: "Wanted"}
	other := domain.Game{ID: 2, Name: "Other"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelsCleanup
	scheduler.wantedGames = []domain.Game{wanted}
	scheduler.channels = map[int64]domain.Channel{
		1: {ID: 1, Login: "offline"},
		2: {
			ID:    2,
			Login: "other",
			Stream: &domain.Stream{
				BroadcastID:  22,
				Game:         &other,
				DropsEnabled: true,
			},
		},
		3: {
			ID:       3,
			Login:    "acl",
			ACLBased: true,
			Stream: &domain.Stream{
				BroadcastID:  33,
				Game:         &other,
				DropsEnabled: true,
			},
		},
		4: {
			ID:    4,
			Login: "wanted",
			Stream: &domain.Stream{
				BroadcastID:  44,
				Game:         &wanted,
				DropsEnabled: true,
			},
		},
	}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-wanted", wanted, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)

	scheduler.handleChannelsCleanup()

	if _, ok := scheduler.channels[1]; ok {
		t.Fatal("离线非 ACL 频道应被清理")
	}
	if _, ok := scheduler.channels[2]; ok {
		t.Fatal("不再想看的非 ACL 频道应被清理")
	}
	if _, ok := scheduler.channels[3]; !ok {
		t.Fatal("ACL 频道不应在增量 cleanup 中被移除")
	}
	if _, ok := scheduler.channels[4]; !ok {
		t.Fatal("仍然可看的频道不应被移除")
	}
	if scheduler.State() != StateChannelsFetch {
		t.Fatalf("cleanup 后应进入 CHANNELS_FETCH: %s", scheduler.State())
	}
}

func TestHandleChannelsFetchAddsACLDirectoryAndTopics(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameACL := domain.Game{ID: 10, Name: "ACL Game"}
	gameDir := domain.Game{ID: 20, Name: "Directory Game", SlugText: "directory-game"}

	tracker := newFakeTracker()
	tracker.syncChannelsFunc = func(ctx context.Context, channelIDs []int64) error {
		tracker.applyChannel(domain.Channel{
			ID:          10,
			Login:       "acl-channel",
			DisplayName: "ACL Channel",
			ACLBased:    true,
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &gameACL,
				Viewers:      50,
				DropsEnabled: true,
			},
		})
		return nil
	}

	gqlClient := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName != "DirectoryPage_Game" {
				return gql.Response{}, nil
			}
			return gql.Response{
				Data: map[string]any{
					"game": map[string]any{
						"streams": map[string]any{
							"edges": []any{
								map[string]any{
									"node": map[string]any{
										"id":           "200",
										"viewersCount": 77,
										"title":        "Live",
										"game": map[string]any{
											"id":          "20",
											"displayName": gameDir.Name,
											"slug":        gameDir.Slug(),
										},
										"broadcaster": map[string]any{
											"id":          "20",
											"login":       "directory-channel",
											"displayName": "Directory Channel",
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
		tracker:   tracker,
		gqlClient: gqlClient,
	})
	scheduler.state = StateChannelsFetch
	scheduler.wantedGames = []domain.Game{gameACL, gameDir}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-acl", gameACL, now.Add(-time.Hour), now.Add(time.Hour), []domain.Channel{
			{ID: 10, Login: "acl-channel", DisplayName: "ACL Channel", ACLBased: true},
		})),
		mustCampaign(t, campaignSpec(now, "campaign-dir", gameDir, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)

	if err := scheduler.handleChannelsFetch(context.Background()); err != nil {
		t.Fatalf("handleChannelsFetch 返回错误: %v", err)
	}

	if _, ok := scheduler.channels[10]; !ok {
		t.Fatal("ACL 频道应被加入调度列表")
	}
	if channel, ok := scheduler.channels[20]; !ok || channel.Stream == nil || channel.Stream.Viewers != 77 {
		t.Fatalf("目录频道应被加入调度列表: %#v", channel)
	}
	if scheduler.State() != StateChannelSwitch {
		t.Fatalf("fetch 后应进入 CHANNEL_SWITCH: %s", scheduler.State())
	}

	added := trackerPubSubKeys(scheduler)
	expected := []string{
		"broadcast-settings-update.10",
		"broadcast-settings-update.20",
		"video-playback-by-id.10",
		"video-playback-by-id.20",
	}
	for _, key := range expected {
		if !slices.Contains(added, key) {
			t.Fatalf("缺少订阅 topic: %s, got=%#v", key, added)
		}
	}
}

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

func TestCanWatchStillRequiresDropsEnabledForNormalCampaign(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}
	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-normal", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)

	channel := domain.Channel{
		ID:    10,
		Login: "normal-channel",
		Stream: &domain.Stream{
			BroadcastID:  100,
			Game:         &game,
			DropsEnabled: false,
		},
	}
	if scheduler.canWatch(channel) {
		t.Fatal("普通 campaign 仍应要求频道具备 DROPS_ENABLED")
	}
}

func TestCanWatchAllowsSpecialEventsWithoutDropsEnabled(t *testing.T) {
	t.Parallel()

	now := testTime()
	otherGame := domain.Game{ID: 999, Name: "Other"}
	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.wantedGames = []domain.Game{{ID: domain.SpecialEventsGameID, Name: "Special Events"}}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, domain.CampaignSpec{
			ID:       "campaign-special",
			Name:     "Special",
			Game:     domain.Game{ID: domain.SpecialEventsGameID, Name: "Special Events"},
			Linked:   true,
			Status:   "ACTIVE",
			StartsAt: now.Add(-time.Hour),
			EndsAt:   now.Add(time.Hour),
			Drops: []domain.TimedDropSpec{
				{
					ID:              "drop-special",
					Name:            "Special Drop",
					StartsAt:        now.Add(-time.Hour),
					EndsAt:          now.Add(time.Hour),
					RequiredMinutes: 10,
					Benefits: []domain.Benefit{
						{ID: "benefit-special", Name: "Special Reward", Type: domain.BenefitTypeDirectEntitlement},
					},
				},
			},
		}),
	)

	channel := domain.Channel{
		ID:    10,
		Login: "special-channel",
		Stream: &domain.Stream{
			BroadcastID:  100,
			Game:         &otherGame,
			DropsEnabled: false,
		},
	}
	if !scheduler.canWatch(channel) {
		t.Fatal("Special Events campaign 应继续允许任意游戏且不依赖 DROPS_ENABLED")
	}
}

func TestHandleChannelSwitchHonorsSelectionAndPriority(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameA := domain.Game{ID: 1, Name: "A Game"}
	gameB := domain.Game{ID: 2, Name: "B Game"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{gameA, gameB}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-a", gameA, now.Add(-time.Hour), now.Add(time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-b", gameB, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)
	scheduler.channels = map[int64]domain.Channel{
		10: {
			ID:    10,
			Login: "b",
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &gameB,
				DropsEnabled: true,
			},
		},
		20: {
			ID:    20,
			Login: "a",
			Stream: &domain.Stream{
				BroadcastID:  200,
				Game:         &gameA,
				DropsEnabled: true,
			},
		},
	}

	scheduler.SelectChannel(10)
	if selected := scheduler.selectedChannel(); selected != 10 {
		t.Fatalf("selected channel 未写入: %d", selected)
	}
	if !scheduler.canWatch(scheduler.channels[10]) {
		t.Fatalf("手选频道应可观看: %#v", scheduler.channels[10])
	}
	scheduler.handleChannelSwitch()
	if got := scheduler.WatchingChannelID(); got != 10 {
		t.Fatalf("手选频道应优先被切入: %d", got)
	}

	scheduler.ClearSelectedChannel()
	scheduler.handleChannelSwitch()
	if got := scheduler.WatchingChannelID(); got != 20 {
		t.Fatalf("高优先级游戏频道应接管观看: %d", got)
	}
}

func TestHandleChannelSwitchLogsNoWatchableAndGoesIdle(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "ACL Game"}

	var logBuf logBuffer
	logger := logBuf.logger()

	scheduler := newTestScheduler(t, testSchedulerOptions{
		logger: logger,
	})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-acl", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)
	// All channels are offline — no Stream set.
	scheduler.channels = map[int64]domain.Channel{
		10: {ID: 10, Login: "offline-a", ACLBased: true},
		20: {ID: 20, Login: "offline-b", ACLBased: true},
	}

	scheduler.handleChannelSwitch()

	if scheduler.State() != StateIdle {
		t.Fatalf("无可观看频道时应进入 IDLE: %s", scheduler.State())
	}
	if got := scheduler.WatchingChannelID(); got != 0 {
		t.Fatalf("无可观看频道时不应有 watching channel: %d", got)
	}
	if !logBuf.contains("当前没有可观看的频道") {
		t.Fatal("应输出无可观看频道的诊断日志")
	}
}

func TestHandleChannelSwitchPreflightsFullSpecialEventRewardGroup(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{Name: "Special Events"}
	campaign := mustCampaign(t, domain.CampaignSpec{
		ID:       "campaign-ewc",
		Name:     "EWC 2026",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(24 * time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:              "bronze",
				Name:            "EWC Bronze",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 60,
				Benefits: []domain.Benefit{
					{ID: "bronze-benefit", Name: "Bronze", Type: domain.BenefitTypeBadge},
				},
			},
			{
				ID:              "diamond",
				Name:            "EWC 2026 (Diamond) Reward Group",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(24 * time.Hour),
				RequiredMinutes: 720,
				Benefits: []domain.Benefit{
					{ID: "diamond-benefit", Name: "Diamond", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})

	scheduler := newTestScheduler(t, testSchedulerOptions{
		gqlClient: currentDropGQLClient("diamond", 720),
	})
	scheduler.state = StateChannelSwitch
	scheduler.settings.EnableBadgesEmotes = true
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.channels = map[int64]domain.Channel{
		10: {
			ID:    10,
			Login: "special-events",
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &game,
				DropsEnabled: true,
			},
		},
	}

	scheduler.handleChannelSwitch()

	if got := scheduler.WatchingChannelID(); got != 0 {
		t.Fatalf("满进度 Special Events 频道应在切台前被跳过: %d", got)
	}
	if scheduler.State() != StateGamesUpdate {
		t.Fatalf("预检查收口后应回到 GAMES_UPDATE 重算规划: %s", scheduler.State())
	}
	for _, dropID := range []string{"bronze", "diamond"} {
		if drop := campaign.Drop(dropID); drop == nil || !drop.IsClaimed {
			t.Fatalf("预检查应收口同窗口 Special Events 里程碑: %s %#v", dropID, drop)
		}
	}
}

func TestHandleChannelSwitchLeavesInvalidatedWatchingChannel(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)
	scheduler.channels = map[int64]domain.Channel{
		10: {
			ID:    10,
			Login: "invalidated",
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &game,
				DropsEnabled: false,
			},
		},
		20: {
			ID:    20,
			Login: "healthy",
			Stream: &domain.Stream{
				BroadcastID:  200,
				Game:         &game,
				DropsEnabled: true,
			},
		},
	}
	scheduler.watchingChannelID = 10

	if scheduler.canWatch(scheduler.channels[10]) {
		t.Fatal("前置条件不成立: 频道 10 应已不可观看")
	}
	if !scheduler.canWatch(scheduler.channels[20]) {
		t.Fatal("前置条件不成立: 频道 20 应可观看")
	}

	scheduler.handleChannelSwitch()

	if got := scheduler.WatchingChannelID(); got != 20 {
		t.Fatalf("当前频道失效且存在同优先级健康频道时应切换过去, got=%d", got)
	}
	if scheduler.State() == StateIdle {
		t.Fatal("存在可观看频道时不应进入 IDLE")
	}
}

func TestHandleChannelSwitchIdlesWhenInvalidatedChannelHasNoReplacement(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.state = StateChannelSwitch
	scheduler.wantedGames = []domain.Game{game}
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
	)
	scheduler.channels = map[int64]domain.Channel{
		10: {
			ID:    10,
			Login: "invalidated",
			Stream: &domain.Stream{
				BroadcastID:  100,
				Game:         &game,
				DropsEnabled: false,
			},
		},
	}
	scheduler.watchingChannelID = 10

	scheduler.handleChannelSwitch()

	if scheduler.State() != StateIdle {
		t.Fatalf("当前频道失效且无可替代频道时应进入 IDLE: %s", scheduler.State())
	}
}
