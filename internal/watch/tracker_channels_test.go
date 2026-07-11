package watch

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

func TestStaleSyncResultDoesNotOverwriteReaddedChannel(t *testing.T) {
	t.Parallel()

	fetchStarted := make(chan struct{})
	releaseFetch := make(chan struct{})
	fakeGQL := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName != "VideoPlayerStreamInfoOverlayChannel" {
				return gql.Response{Data: map[string]any{"channel": nil}}, nil
			}
			close(fetchStarted)
			<-releaseFetch
			return gql.Response{
				Data: map[string]any{
					"user": map[string]any{
						"id":          "101",
						"displayName": "OldStreamer",
						"stream": map[string]any{
							"id":           "654",
							"viewersCount": 10,
						},
						"broadcastSettings": map[string]any{
							"title": "Old Live",
							"game": map[string]any{
								"id":          "7",
								"displayName": "Game",
							},
						},
					},
				},
			}, nil
		},
	}

	tracker := newTestTracker(t, testTrackerOptions{gqlClient: fakeGQL})
	tracker.AddChannel(domain.Channel{ID: 101, Login: "old-streamer"})

	syncDone := make(chan error, 1)
	go func() {
		_, err := tracker.SyncChannel(context.Background(), 101)
		syncDone <- err
	}()

	select {
	case <-fetchStarted:
	case <-time.After(time.Second):
		t.Fatal("SyncChannel 未开始")
	}

	tracker.RemoveChannel(101)
	tracker.AddChannel(domain.Channel{ID: 101, Login: "new-streamer", DisplayName: "NewStreamer"})

	close(releaseFetch)
	select {
	case err := <-syncDone:
		if err != nil {
			t.Fatalf("SyncChannel 返回错误: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SyncChannel 未结束")
	}

	channel, ok := tracker.Channel(101)
	if !ok {
		t.Fatal("重新添加的频道应仍被跟踪")
	}
	if channel.Online() || channel.DisplayName != "NewStreamer" {
		t.Fatalf("旧 sync 不应覆盖重新添加的频道: %#v", channel)
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
