package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/pubsub"
)

func TestComputeWantedGamesHonorsPriorityExcludeAndWindow(t *testing.T) {
	t.Parallel()

	now := testTime()
	gameA := domain.Game{ID: 1, Name: "Apex Legends"}
	gameB := domain.Game{ID: 2, Name: "Rust"}
	gameIgnored := domain.Game{ID: 3, Name: "Ignored"}
	gameLater := domain.Game{ID: 4, Name: "Later"}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		settings: config.Settings{
			PriorityMode: config.EndingSoonest,
			Priority:     []string{gameA.Name},
			Exclude:      []string{gameIgnored.Name},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(
		mustCampaign(t, campaignSpec(now, "campaign-a", gameA, now.Add(-time.Hour), now.Add(4*time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-b", gameB, now.Add(-time.Hour), now.Add(2*time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-ignored", gameIgnored, now.Add(-time.Hour), now.Add(2*time.Hour), nil)),
		mustCampaign(t, campaignSpec(now, "campaign-later", gameLater, now.Add(2*time.Hour), now.Add(4*time.Hour), nil)),
	)

	got := scheduler.computeWantedGames(now)
	want := []domain.Game{gameA, gameB}
	if !slices.Equal(got, want) {
		t.Fatalf("wanted games 不匹配:\n got=%#v\nwant=%#v", got, want)
	}
}

func TestCloneInventorySnapshotCreatesIndependentCampaignGraph(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-clone", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	original := snapshotFromCampaigns(campaign)

	cloned, err := cloneInventorySnapshot(original)
	if err != nil {
		t.Fatalf("cloneInventorySnapshot 返回错误: %v", err)
	}

	originalDrop := original.Drops["campaign-clone-drop"]
	clonedDrop := cloned.Drops["campaign-clone-drop"]
	if originalDrop == nil || clonedDrop == nil {
		t.Fatalf("clone 后 drop 丢失: original=%#v cloned=%#v", originalDrop, clonedDrop)
	}
	if originalDrop == clonedDrop {
		t.Fatal("clone 后的 drop 不应与原对象共享指针")
	}

	originalDrop.ExtraCurrentMinutes = 5
	if clonedDrop.ExtraCurrentMinutes != 0 {
		t.Fatalf("clone 后的 drop 不应跟随原对象变化: %d", clonedDrop.ExtraCurrentMinutes)
	}
}

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

func TestHandleInventoryFetchAddsUserTopics(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Campaign"}
	refresher := &fakeRefresher{
		refreshFunc: func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error) {
			return snapshotFromCampaigns(
				mustCampaign(t, campaignSpec(now, "campaign-user-topics", game, now.Add(-time.Hour), now.Add(time.Hour), nil)),
			), nil
		},
	}
	pubsubManager := &fakePubSub{}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher: refresher,
		pubsub:    pubsubManager,
		authState: &fakeAuthState{snapshot: auth.Snapshot{UserID: 42}},
	})

	if err := scheduler.handleInventoryFetch(context.Background()); err != nil {
		t.Fatalf("handleInventoryFetch 返回错误: %v", err)
	}

	added := pubsubManager.addedKeys()
	for _, expected := range []string{"onsite-notifications.42", "user-drop-events.42"} {
		if !slices.Contains(added, expected) {
			t.Fatalf("缺少用户 topic %q, got=%#v", expected, added)
		}
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

func TestWatchLoopSendsWatchAndBumpsMinutesOnFallback(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-watch", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	drop := campaign.Drop("campaign-watch-drop")

	tracker := newFakeTracker()
	tracker.applyChannel(domain.Channel{
		ID:    99,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  999,
			Game:         &game,
			DropsEnabled: true,
		},
	})

	scheduler := newTestScheduler(t, testSchedulerOptions{
		tracker: tracker,
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				if operation.OperationName == "DropCurrentSessionContext" {
					return gql.Response{
						Data: map[string]any{
							"currentUser": map[string]any{
								"dropCurrentSession": nil,
							},
						},
					}, nil
				}
				return gql.Response{}, nil
			},
		},
		watchInterval: 2 * time.Millisecond,
		progressDelay: time.Millisecond,
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	scheduler.channels = tracker.snapshot()
	scheduler.watchingChannelID = 99

	ctx, cancel := context.WithCancel(context.Background())
	scheduler.wg.Add(1)
	go scheduler.watchLoop(ctx)

	time.Sleep(25 * time.Millisecond)
	cancel()
	scheduler.wg.Wait()

	if tracker.sendWatchCalls() == 0 {
		t.Fatal("watch loop 应调用 SendWatch")
	}
	if drop.ExtraCurrentMinutes == 0 {
		t.Fatal("CurrentDrop 缺失时应回退到本地补分钟")
	}
}

func TestResolveProgressRequestsSwitchWhenExtraMinutesHitLimit(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-limit", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	drop := campaign.Drop("campaign-limit-drop")
	drop.ExtraCurrentMinutes = domain.MaxExtraMinutes - 1

	scheduler := newTestScheduler(t, testSchedulerOptions{
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				return gql.Response{
					Data: map[string]any{
						"currentUser": map[string]any{
							"dropCurrentSession": nil,
						},
					},
				}, nil
			},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.wantedGames = []domain.Game{game}
	channel := domain.Channel{
		ID:    1,
		Login: "channel",
		Stream: &domain.Stream{
			BroadcastID:  1,
			Game:         &game,
			DropsEnabled: true,
		},
	}

	if err := scheduler.resolveProgress(context.Background(), channel); err != nil {
		t.Fatalf("resolveProgress 返回错误: %v", err)
	}
	if scheduler.State() != StateChannelSwitch {
		t.Fatalf("达到 extra minutes 上限后应请求切台: %s", scheduler.State())
	}
}

func TestHandleDropEventUpdatesProgressForWatchingDrop(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-progress", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	drop := campaign.Drop("campaign-progress-drop")

	scheduler := newTestScheduler(t, testSchedulerOptions{})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.channels = map[int64]domain.Channel{
		9: {
			ID:    9,
			Login: "watching",
			Stream: &domain.Stream{
				BroadcastID:  90,
				Game:         &game,
				DropsEnabled: true,
			},
		},
	}
	scheduler.watchingChannelID = 9

	event := pubsub.Event{
		Topic:   pubsub.MustNewTopic(pubsub.CategoryUser, pubsub.TopicDrops, 1, nil),
		Message: json.RawMessage(`{"type":"drop-progress","data":{"drop_id":"campaign-progress-drop","current_progress_min":12,"required_progress_min":30}}`),
	}
	if err := scheduler.handleDropEvent(context.Background(), event); err != nil {
		t.Fatalf("handleDropEvent 返回错误: %v", err)
	}

	if drop.RealCurrentMinutes != 12 {
		t.Fatalf("掉宝进度未更新: %d", drop.RealCurrentMinutes)
	}
}

func TestHandleDropClaimRestartsWatchingWhenCampaignStillEarnable(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	campaign, err := domain.NewCampaign(domain.CampaignSpec{
		ID:       "campaign-claim",
		Name:     "campaign-claim",
		Game:     game,
		Linked:   true,
		Status:   "ACTIVE",
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(time.Hour),
		Drops: []domain.TimedDropSpec{
			{
				ID:              "campaign-claim-drop",
				Name:            "campaign-claim-drop",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(time.Hour),
				RequiredMinutes: 30,
				Benefits: []domain.Benefit{
					{ID: "claim-benefit-1", Name: "claim-benefit-1", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
			{
				ID:              "campaign-claim-next-drop",
				Name:            "campaign-claim-next-drop",
				StartsAt:        now.Add(-time.Hour),
				EndsAt:          now.Add(time.Hour),
				RequiredMinutes: 30,
				Benefits: []domain.Benefit{
					{ID: "claim-benefit-2", Name: "claim-benefit-2", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewCampaign 返回错误: %v", err)
	}
	drop := campaign.Drop("campaign-claim-drop")
	currentDropCalls := 0

	scheduler := newTestScheduler(t, testSchedulerOptions{
		sleep: func(context.Context, time.Duration) error {
			return nil
		},
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				switch operation.OperationName {
				case "DropsPage_ClaimDropRewards":
					return gql.Response{
						Data: map[string]any{
							"claimDropRewards": map[string]any{
								"status": "ELIGIBLE_FOR_ALL",
							},
						},
					}, nil
				case "DropCurrentSessionContext":
					currentDropCalls++
					if currentDropCalls == 1 {
						return gql.Response{
							Data: map[string]any{
								"currentUser": map[string]any{
									"dropCurrentSession": map[string]any{
										"dropID":                drop.ID,
										"currentMinutesWatched": 30,
									},
								},
							},
						}, nil
					}
					return gql.Response{
						Data: map[string]any{
							"currentUser": map[string]any{
								"dropCurrentSession": nil,
							},
						},
					}, nil
				default:
					return gql.Response{}, nil
				}
			},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.channels = map[int64]domain.Channel{
		9: {
			ID:    9,
			Login: "watching",
			Stream: &domain.Stream{
				BroadcastID:  90,
				Game:         &game,
				DropsEnabled: true,
			},
		},
	}
	scheduler.watchingChannelID = 9

	event := pubsub.Event{
		Topic:   pubsub.MustNewTopic(pubsub.CategoryUser, pubsub.TopicDrops, 1, nil),
		Message: json.RawMessage(`{"type":"drop-claim","data":{"drop_id":"campaign-claim-drop","drop_instance_id":"instance-1"}}`),
	}
	if err := scheduler.handleDropEvent(context.Background(), event); err != nil {
		t.Fatalf("handleDropEvent 返回错误: %v", err)
	}

	if drop.ClaimID != "instance-1" {
		t.Fatalf("claim_id 未更新: %q", drop.ClaimID)
	}
	if !drop.IsClaimed {
		t.Fatal("认领成功后应标记为已领取")
	}
	if scheduler.State() == StateInventoryFetch {
		t.Fatalf("当前频道仍可推进时不应触发 inventory reload: %s", scheduler.State())
	}
	select {
	case <-scheduler.watchSignal:
	default:
		t.Fatal("campaign 仍可推进时应触发 restart_watching")
	}
}

func TestHandleDropClaimRequestsReloadWhenCampaignCannotContinue(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Watched"}
	otherGame := domain.Game{ID: 2, Name: "Other"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-claim-reload", game, now.Add(-time.Hour), now.Add(time.Hour), nil))

	scheduler := newTestScheduler(t, testSchedulerOptions{
		sleep: func(context.Context, time.Duration) error {
			return nil
		},
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				switch operation.OperationName {
				case "DropsPage_ClaimDropRewards":
					return gql.Response{
						Data: map[string]any{
							"claimDropRewards": map[string]any{
								"status": "ELIGIBLE_FOR_ALL",
							},
						},
					}, nil
				case "DropCurrentSessionContext":
					return gql.Response{
						Data: map[string]any{
							"currentUser": map[string]any{
								"dropCurrentSession": nil,
							},
						},
					}, nil
				default:
					return gql.Response{}, nil
				}
			},
		},
	})
	scheduler.snapshot = snapshotFromCampaigns(campaign)
	scheduler.channels = map[int64]domain.Channel{
		9: {
			ID:    9,
			Login: "watching",
			Stream: &domain.Stream{
				BroadcastID:  90,
				Game:         &otherGame,
				DropsEnabled: true,
			},
		},
	}
	scheduler.watchingChannelID = 9

	event := pubsub.Event{
		Topic:   pubsub.MustNewTopic(pubsub.CategoryUser, pubsub.TopicDrops, 1, nil),
		Message: json.RawMessage(`{"type":"drop-claim","data":{"drop_id":"campaign-claim-reload-drop","drop_instance_id":"instance-2"}}`),
	}
	if err := scheduler.handleDropEvent(context.Background(), event); err != nil {
		t.Fatalf("handleDropEvent 返回错误: %v", err)
	}

	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("当前频道无法继续推进时应触发 inventory reload: %s", scheduler.State())
	}
}

func TestHandleNotificationEventReloadsAndDeletesRewardNotification(t *testing.T) {
	t.Parallel()

	var deletedID string
	scheduler := newTestScheduler(t, testSchedulerOptions{
		gqlClient: &fakeGQLClient{
			doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
				if operation.OperationName == "OnsiteNotifications_DeleteNotification" {
					input := operation.Variables["input"].(map[string]any)
					deletedID = input["id"].(string)
				}
				return gql.Response{}, nil
			},
		},
	})

	event := pubsub.Event{
		Topic:   pubsub.MustNewTopic(pubsub.CategoryUser, pubsub.TopicNotifications, 1, nil),
		Message: json.RawMessage(`{"type":"create-notification","data":{"notification":{"id":"notif-1","type":"user_drop_reward_reminder_notification"}}}`),
	}
	if err := scheduler.handleNotificationEvent(context.Background(), event); err != nil {
		t.Fatalf("handleNotificationEvent 返回错误: %v", err)
	}

	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("奖励通知应触发 inventory reload: %s", scheduler.State())
	}
	if deletedID != "notif-1" {
		t.Fatalf("奖励通知未被删除: %q", deletedID)
	}
}

func TestMaintenanceLoopRequestsCleanupThenReload(t *testing.T) {
	t.Parallel()

	current := testTime()
	sleepCalls := 0
	var scheduler *Scheduler
	scheduler = newTestScheduler(t, testSchedulerOptions{
		now: func() time.Time {
			return current
		},
		sleep: func(ctx context.Context, delay time.Duration) error {
			sleepCalls++
			current = current.Add(delay)
			if sleepCalls == 2 && scheduler.State() != StateChannelsCleanup {
				return errors.New("cleanup 未按预期触发")
			}
			return nil
		},
		maintenanceReload: time.Hour,
	})

	scheduler.wg.Add(1)
	scheduler.maintenanceLoop(context.Background(), []time.Time{current.Add(10 * time.Minute)})

	if sleepCalls != 2 {
		t.Fatalf("maintenance sleep 次数不匹配: %d", sleepCalls)
	}
	if scheduler.State() != StateInventoryFetch {
		t.Fatalf("maintenance 结束后应请求 inventory reload: %s", scheduler.State())
	}
}

type testSchedulerOptions struct {
	settings          config.Settings
	refresher         InventoryRefresher
	tracker           *fakeTracker
	pubsub            *fakePubSub
	gqlClient         GQLClient
	authState         AuthState
	now               func() time.Time
	sleep             func(context.Context, time.Duration) error
	watchInterval     time.Duration
	progressDelay     time.Duration
	maintenanceReload time.Duration
}

func newTestScheduler(t *testing.T, options testSchedulerOptions) *Scheduler {
	t.Helper()

	refresher := options.refresher
	if refresher == nil {
		refresher = &fakeRefresher{}
	}

	tracker := options.tracker
	if tracker == nil {
		tracker = newFakeTracker()
	}

	pubsubManager := options.pubsub
	if pubsubManager == nil {
		pubsubManager = &fakePubSub{}
	}

	gqlClient := options.gqlClient
	if gqlClient == nil {
		gqlClient = &fakeGQLClient{}
	}

	authState := options.authState
	if authState == nil {
		authState = &fakeAuthState{snapshot: auth.Snapshot{UserID: 1}}
	}

	now := options.now
	if now == nil {
		now = testTime
	}

	scheduler, err := New(Options{
		Settings:          options.settings,
		Refresher:         refresher,
		Tracker:           tracker,
		PubSub:            pubsubManager,
		GQLClient:         gqlClient,
		AuthState:         authState,
		Clock:             now,
		Sleep:             options.sleep,
		WatchInterval:     options.watchInterval,
		ProgressDelay:     options.progressDelay,
		MaintenanceReload: options.maintenanceReload,
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	return scheduler
}

func trackerPubSubKeys(s *Scheduler) []string {
	fake, ok := s.pubsub.(*fakePubSub)
	if !ok {
		return nil
	}
	return fake.addedKeys()
}

type fakeRefresher struct {
	refreshFunc func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error)
}

func (f *fakeRefresher) Refresh(ctx context.Context, options inventory.RefreshOptions) (inventory.Snapshot, error) {
	if f.refreshFunc == nil {
		return inventory.Snapshot{}, nil
	}
	return f.refreshFunc(ctx, options)
}

type fakeTracker struct {
	mu               sync.Mutex
	channels         map[int64]domain.Channel
	onChange         func(before, after domain.Channel)
	syncChannelsFunc func(context.Context, []int64) error
	sendWatchFunc    func(context.Context, int64) (bool, error)
	sendCount        int
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{
		channels: make(map[int64]domain.Channel),
	}
}

func (f *fakeTracker) Configure(config.Settings, inventory.Snapshot) {}

func (f *fakeTracker) SetChannelChangeHandler(handler func(before, after domain.Channel)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onChange = handler
}

func (f *fakeTracker) AddChannel(channel domain.Channel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[channel.ID] = cloneChannel(channel)
}

func (f *fakeTracker) RemoveChannel(channelID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.channels, channelID)
}

func (f *fakeTracker) Channel(channelID int64) (domain.Channel, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	channel, ok := f.channels[channelID]
	return cloneChannel(channel), ok
}

func (f *fakeTracker) SyncChannels(ctx context.Context, channelIDs ...int64) error {
	if f.syncChannelsFunc == nil {
		return nil
	}
	return f.syncChannelsFunc(ctx, channelIDs)
}

func (f *fakeTracker) SendWatch(ctx context.Context, channelID int64) (bool, error) {
	f.mu.Lock()
	f.sendCount++
	sendWatchFunc := f.sendWatchFunc
	f.mu.Unlock()

	if sendWatchFunc == nil {
		return true, nil
	}
	return sendWatchFunc(ctx, channelID)
}

func (f *fakeTracker) ProcessStreamState(context.Context, int64, json.RawMessage) error {
	return nil
}

func (f *fakeTracker) ProcessStreamUpdate(context.Context, int64, json.RawMessage) error {
	return nil
}

func (f *fakeTracker) Close(context.Context) error {
	return nil
}

func (f *fakeTracker) applyChannel(channel domain.Channel) {
	f.mu.Lock()
	before := cloneChannel(f.channels[channel.ID])
	f.channels[channel.ID] = cloneChannel(channel)
	handler := f.onChange
	after := cloneChannel(channel)
	f.mu.Unlock()

	if handler != nil {
		handler(before, after)
	}
}

func (f *fakeTracker) snapshot() map[int64]domain.Channel {
	f.mu.Lock()
	defer f.mu.Unlock()

	cloned := make(map[int64]domain.Channel, len(f.channels))
	for channelID, channel := range f.channels {
		cloned[channelID] = cloneChannel(channel)
	}
	return cloned
}

func (f *fakeTracker) sendWatchCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendCount
}

type fakePubSub struct {
	mu      sync.Mutex
	started int
	added   []string
	removed []string
}

func (f *fakePubSub) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	return nil
}

func (f *fakePubSub) AddTopics(topics ...pubsub.Topic) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, topic := range topics {
		f.added = append(f.added, topic.Key())
	}
	return nil
}

func (f *fakePubSub) RemoveTopics(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, keys...)
}

func (f *fakePubSub) Stop(context.Context, bool) error {
	return nil
}

func (f *fakePubSub) addedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.added...)
}

type fakeGQLClient struct {
	doFunc func(context.Context, gql.Operation) (gql.Response, error)
}

func (f *fakeGQLClient) Do(ctx context.Context, operation gql.Operation) (gql.Response, error) {
	if f.doFunc == nil {
		return gql.Response{}, nil
	}
	return f.doFunc(ctx, operation)
}

type fakeAuthState struct {
	snapshot auth.Snapshot
}

func (f *fakeAuthState) Snapshot() auth.Snapshot {
	return f.snapshot
}

func mustCampaign(t *testing.T, spec domain.CampaignSpec) *domain.DropsCampaign {
	t.Helper()
	campaign, err := domain.NewCampaign(spec)
	if err != nil {
		t.Fatalf("NewCampaign 返回错误: %v", err)
	}
	return campaign
}

func snapshotFromCampaigns(campaigns ...*domain.DropsCampaign) inventory.Snapshot {
	snapshot := inventory.Snapshot{
		Inventory: make([]*domain.DropsCampaign, 0, len(campaigns)),
		Campaigns: make(map[string]*domain.DropsCampaign, len(campaigns)),
		Drops:     make(map[string]*domain.TimedDrop),
	}
	for _, campaign := range campaigns {
		if campaign == nil {
			continue
		}
		snapshot.Inventory = append(snapshot.Inventory, campaign)
		snapshot.Campaigns[campaign.ID] = campaign
		for _, drop := range campaign.Drops() {
			snapshot.Drops[drop.ID] = drop
		}
	}
	return snapshot
}

func campaignSpec(_ time.Time, id string, game domain.Game, startsAt time.Time, endsAt time.Time, allowed []domain.Channel) domain.CampaignSpec {
	return domain.CampaignSpec{
		ID:              id,
		Name:            id,
		Game:            game,
		Linked:          true,
		Status:          "ACTIVE",
		StartsAt:        startsAt,
		EndsAt:          endsAt,
		AllowedChannels: allowed,
		Drops: []domain.TimedDropSpec{
			{
				ID:              id + "-drop",
				Name:            id + "-drop",
				StartsAt:        startsAt,
				EndsAt:          endsAt,
				RequiredMinutes: 30,
				Benefits: []domain.Benefit{
					{ID: id + "-benefit", Name: id + "-reward", Type: domain.BenefitTypeDirectEntitlement},
				},
			},
		},
	}
}

func testTime() time.Time {
	return time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)
}
