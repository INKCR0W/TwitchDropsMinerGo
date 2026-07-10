package pubsub

import (
	"context"
	"errors"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func TestManagerWaitUntilConnectedWaitsForListenAck(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-wait"},
		},
		Dialer:       &fakeDialer{connections: []*fakeConn{conn}},
		ReadTimeout:  5 * time.Millisecond,
		PingInterval: time.Hour,
	})

	topic := MustNewTopic(CategoryUser, TopicDrops, 88, nil)
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
		_ = manager.Stop(stopCtx, true)
	}()

	listen := conn.waitForType(t, "LISTEN", time.Second)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	if err := manager.WaitUntilConnected(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LISTEN ack 前 WaitUntilConnected 应等待，实际为 %v", err)
	}

	nonce, _ := listen["nonce"].(string)
	if nonce == "" {
		t.Fatalf("LISTEN 应包含 nonce: %#v", listen)
	}
	conn.pushText(t, map[string]any{
		"type":  "RESPONSE",
		"nonce": nonce,
	})
	if err := manager.WaitUntilConnected(context.Background()); err != nil {
		t.Fatalf("LISTEN ack 后 WaitUntilConnected 应成功，实际为 %v", err)
	}
}

func TestWaitUntilConnectedReturnsErrorWhenManagerNotRunning(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-5"},
		},
		Dialer:       &fakeDialer{connections: []*fakeConn{conn}},
		ReadTimeout:  5 * time.Millisecond,
		PingInterval: time.Hour,
	})

	topic := MustNewTopic(CategoryUser, TopicDrops, 99, nil)
	if err := manager.AddTopics(topic); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	if err := manager.WaitUntilConnected(context.Background()); !errors.Is(err, ErrManagerNotRunning) {
		t.Fatalf("未启动时 WaitUntilConnected 应返回 ErrManagerNotRunning，实际为 %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start 返回错误: %v", err)
	}

	conn.waitForType(t, "LISTEN", time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := manager.Stop(stopCtx, false); err != nil {
		t.Fatalf("Stop 返回错误: %v", err)
	}

	if err := manager.WaitUntilConnected(context.Background()); !errors.Is(err, ErrManagerNotRunning) {
		t.Fatalf("停止后 WaitUntilConnected 应返回 ErrManagerNotRunning，实际为 %v", err)
	}
}
