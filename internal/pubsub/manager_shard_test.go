package pubsub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func TestManagerBatchesListenAndUnlistenCommands(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn}}
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-1"},
		},
		HeadersProvider: func(context.Context) (http.Header, error) {
			headers := make(http.Header)
			headers.Set("X-Device-Id", "device-1")
			return headers, nil
		},
		Dialer:          dialer,
		ListenBatchSize: 2,
		ShardTopicLimit: 10,
		ReadTimeout:     5 * time.Millisecond,
		PingInterval:    time.Hour,
	})

	topics := []Topic{
		MustNewTopic(CategoryUser, TopicDrops, 1, nil),
		MustNewTopic(CategoryUser, TopicNotifications, 1, nil),
		MustNewTopic(CategoryChannel, TopicStreamState, 10, nil),
	}
	if err := manager.AddTopics(topics...); err != nil {
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

	if err := manager.WaitUntilConnected(context.Background()); err != nil {
		t.Fatalf("WaitUntilConnected 返回错误: %v", err)
	}

	if dialer.CallCount() != 1 {
		t.Fatalf("Dial 次数不匹配: %d", dialer.CallCount())
	}
	if got := dialer.Header(0).Get("X-Device-Id"); got != "device-1" {
		t.Fatalf("握手请求头未透传: %q", got)
	}

	listen1 := conn.waitForType(t, "LISTEN", time.Second)
	listen2 := conn.waitForType(t, "LISTEN", time.Second)
	listenBatches := [][]string{
		topicsFromEnvelope(t, listen1),
		topicsFromEnvelope(t, listen2),
	}
	if got := len(listenBatches[0]) + len(listenBatches[1]); got != 3 {
		t.Fatalf("LISTEN topic 总数不匹配: %#v", listenBatches)
	}
	if got := len(listenBatches[0]); got != 2 && len(listenBatches[1]) != 2 {
		t.Fatalf("LISTEN 分批大小不匹配: %#v", listenBatches)
	}
	assertAuthTokenAndNonce(t, listen1, "token-1")
	assertAuthTokenAndNonce(t, listen2, "token-1")

	manager.RemoveTopics(topics[0].Key(), topics[1].Key())

	unlisten := conn.waitForType(t, "UNLISTEN", time.Second)
	if got := len(topicsFromEnvelope(t, unlisten)); got != 2 {
		t.Fatalf("UNLISTEN 分批大小不匹配: %#v", unlisten)
	}
	assertAuthTokenAndNonce(t, unlisten, "token-1")
}

func TestHandleConnectionStopsAfterReadFailure(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-timeout"},
		},
		PingInterval: time.Hour,
		PingTimeout:  time.Hour,
		ReadTimeout:  10 * time.Millisecond,
	})

	shard := &shard{
		manager:   manager,
		topics:    make(map[string]Topic),
		submitted: make(map[string]Topic),
		wake:      make(chan struct{}, 1),
	}
	conn := &errorOnceConn{err: fakeTimeoutError{}}

	err := shard.handleConnection(context.Background(), conn)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("期望返回超时错误，实际为 %v", err)
	}
	if got := conn.reads.Load(); got != 1 {
		t.Fatalf("读失败后不应再次读取同一连接，实际读取次数为 %d", got)
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
