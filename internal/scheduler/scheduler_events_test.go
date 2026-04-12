package scheduler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/pubsub"
)

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
