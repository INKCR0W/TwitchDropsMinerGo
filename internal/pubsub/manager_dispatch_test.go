package pubsub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func TestManagerDispatchesIncomingMessage(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-3"},
		},
		Dialer:       &fakeDialer{connections: []*fakeConn{conn}},
		ReadTimeout:  5 * time.Millisecond,
		PingInterval: time.Hour,
	})

	events := make(chan Event, 1)
	topic := MustNewTopic(CategoryUser, TopicDrops, 42, func(_ context.Context, event Event) error {
		events <- event
		return nil
	})
	if err := manager.AddTopics(topic); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start 返回错误: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := manager.Stop(stopCtx, true); err != nil {
			t.Fatalf("Stop 返回错误: %v", err)
		}
	}()

	conn.waitForType(t, "LISTEN", time.Second)

	conn.pushText(t, map[string]any{
		"type": "MESSAGE",
		"data": map[string]any{
			"topic":   topic.Key(),
			"message": `{"type":"drop-progress","current_progress_min":17}`,
		},
	})

	select {
	case event := <-events:
		if event.Topic.Key() != topic.Key() {
			t.Fatalf("分发 topic 不匹配: %q", event.Topic.Key())
		}

		var payload map[string]any
		if err := json.Unmarshal(event.Message, &payload); err != nil {
			t.Fatalf("解析事件消息失败: %v", err)
		}
		if got := payload["type"]; got != "drop-progress" {
			t.Fatalf("事件类型不匹配: %#v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 PubSub 事件分发")
	}
}

func TestManagerProcessesHandlersSerially(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-limit"},
		},
		Dialer:       &fakeDialer{connections: []*fakeConn{conn}},
		ReadTimeout:  5 * time.Millisecond,
		PingInterval: time.Hour,
	})

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	topic := MustNewTopic(CategoryUser, TopicDrops, 42, func(_ context.Context, event Event) error {
		entered <- struct{}{}
		<-release
		return nil
	})
	if err := manager.AddTopics(topic); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start 返回错误: %v", err)
	}
	defer func() {
		close(release)
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = manager.Stop(stopCtx, true)
	}()

	conn.waitForType(t, "LISTEN", time.Second)
	for i := 0; i < 2; i++ {
		conn.pushText(t, map[string]any{
			"type": "MESSAGE",
			"data": map[string]any{
				"topic":   topic.Key(),
				"message": `{"type":"drop-progress"}`,
			},
		})
	}

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("第一个 handler 未启动")
	}
	select {
	case <-entered:
		t.Fatal("串行分发时，第一个 handler 阻塞期间第二个 handler 不应启动")
	case <-time.After(50 * time.Millisecond):
	}
}
