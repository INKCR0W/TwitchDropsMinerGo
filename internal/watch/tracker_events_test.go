package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestProcessStreamEventsHonorOnlineDelayAndStatusTransitions(t *testing.T) {
	t.Parallel()

	slept := make(chan time.Duration, 1)
	synced := make(chan struct{}, 1)
	var syncCalls atomic.Int32
	fakeGQL := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			call := syncCalls.Add(1)
			title := fmt.Sprintf("After delay %d", call)
			if call > 1 {
				title = "After update"
			}

			select {
			case synced <- struct{}{}:
			default:
			}
			return gql.Response{
				Data: map[string]any{
					"user": map[string]any{
						"id":          "55",
						"displayName": "Delayed",
						"stream": map[string]any{
							"id":           "555",
							"viewersCount": 10,
						},
						"broadcastSettings": map[string]any{
							"title": title,
							"game": map[string]any{
								"id":          "9",
								"displayName": "Game",
							},
						},
					},
				},
			}, nil
		},
	}

	tracker := newTestTracker(t, testTrackerOptions{
		gqlClient: fakeGQL,
		sleep: func(ctx context.Context, delay time.Duration) error {
			slept <- delay
			return nil
		},
		onlineDelay: 7 * time.Second,
	})

	tracker.AddChannel(domain.Channel{ID: 55, Login: "delayed"})

	if err := tracker.ProcessStreamState(context.Background(), 55, json.RawMessage(`{"type":"stream-up"}`)); err != nil {
		t.Fatalf("ProcessStreamState 返回错误: %v", err)
	}

	select {
	case delay := <-slept:
		if delay != 7*time.Second {
			t.Fatalf("ONLINE_DELAY 不匹配: %v", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("未触发延迟检查")
	}

	select {
	case <-synced:
	case <-time.After(time.Second):
		t.Fatal("延迟检查后未触发同步")
	}

	channel := waitForTrackedChannel(t, tracker, 55, func(channel domain.Channel) bool {
		return channel.Stream != nil && channel.Stream.Title == "After delay 1" && !channel.PendingOnline()
	})
	if channel.Stream == nil {
		t.Fatalf("延迟同步后频道应在线: %#v", channel)
	}

	if err := tracker.ProcessStreamState(context.Background(), 55, json.RawMessage(`{"type":"viewcount","viewers":77}`)); err != nil {
		t.Fatalf("处理 viewcount 返回错误: %v", err)
	}
	channel, _ = tracker.Channel(55)
	if channel.Stream == nil || channel.Stream.Viewers != 77 {
		t.Fatalf("viewcount 应更新 viewer 数: %#v", channel.Stream)
	}

	if err := tracker.ProcessStreamUpdate(context.Background(), 55, json.RawMessage(`{"type":"broadcast_settings_update"}`)); err != nil {
		t.Fatalf("处理 stream update 返回错误: %v", err)
	}
	select {
	case <-synced:
	case <-time.After(time.Second):
		t.Fatal("stream update 应重新触发延迟同步")
	}
	waitForTrackedChannel(t, tracker, 55, func(channel domain.Channel) bool {
		return channel.Stream != nil && channel.Stream.Title == "After update"
	})

	if err := tracker.ProcessStreamState(context.Background(), 55, json.RawMessage(`{"type":"stream-down"}`)); err != nil {
		t.Fatalf("处理 stream-down 返回错误: %v", err)
	}
	channel = waitForTrackedChannel(t, tracker, 55, func(channel domain.Channel) bool {
		return channel.Stream == nil && channel.Offline()
	})
	if channel.Stream != nil || !channel.Offline() {
		t.Fatalf("stream-down 后频道应离线: %#v", channel)
	}
}

func TestTrackerNotifiesChannelChangeHandler(t *testing.T) {
	t.Parallel()

	fakeGQL := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			return gql.Response{
				Data: map[string]any{
					"user": map[string]any{
						"id":          "90",
						"displayName": "Notify",
						"stream": map[string]any{
							"id":           "900",
							"viewersCount": 10,
						},
						"broadcastSettings": map[string]any{
							"title": "Live",
							"game": map[string]any{
								"id":          "9",
								"displayName": "Game",
							},
						},
					},
				},
			}, nil
		},
	}

	tracker := newTestTracker(t, testTrackerOptions{gqlClient: fakeGQL})
	tracker.AddChannel(domain.Channel{ID: 90, Login: "notify"})

	changes := make(chan struct {
		before domain.Channel
		after  domain.Channel
	}, 2)
	tracker.SetChannelChangeHandler(func(before, after domain.Channel) {
		changes <- struct {
			before domain.Channel
			after  domain.Channel
		}{before: before, after: after}
	})

	if _, err := tracker.SyncChannel(context.Background(), 90); err != nil {
		t.Fatalf("SyncChannel 返回错误: %v", err)
	}

	first := <-changes
	if first.before.Stream != nil || first.after.Stream == nil {
		t.Fatalf("首次同步的前后状态不匹配: %#v", first)
	}

	if err := tracker.ProcessStreamState(context.Background(), 90, json.RawMessage(`{"type":"stream-down"}`)); err != nil {
		t.Fatalf("ProcessStreamState 返回错误: %v", err)
	}

	second := <-changes
	if second.before.Stream == nil || second.after.Stream != nil {
		t.Fatalf("下线通知的前后状态不匹配: %#v", second)
	}
}

func waitForTrackedChannel(t *testing.T, tracker *Tracker, channelID int64, predicate func(domain.Channel) bool) domain.Channel {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		channel, ok := tracker.Channel(channelID)
		if ok && predicate(channel) {
			return channel
		}
		time.Sleep(10 * time.Millisecond)
	}

	channel, _ := tracker.Channel(channelID)
	t.Fatalf("等待频道状态超时: %#v", channel)
	return domain.Channel{}
}
