package pubsub

import (
	"context"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func TestManagerReconnectsAfterPingTimeoutAndResubscribes(t *testing.T) {
	t.Parallel()

	conn1 := newFakeConn()
	conn2 := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn1, conn2}}
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-2"},
		},
		Dialer:          dialer,
		ReadTimeout:     5 * time.Millisecond,
		PingInterval:    10 * time.Millisecond,
		PingTimeout:     10 * time.Millisecond,
		ShardTopicLimit: 10,
	})

	topic := MustNewTopic(CategoryUser, TopicDrops, 77, nil)
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

	conn1.waitForType(t, "PING", time.Second)
	waitUntil(t, time.Second, func() bool {
		return dialer.CallCount() == 2
	})
	conn1.waitClosed(t, time.Second)

	listen := conn2.waitForType(t, "LISTEN", time.Second)
	if got := topicsFromEnvelope(t, listen); len(got) != 1 || got[0] != topic.Key() {
		t.Fatalf("重连后未重新订阅 topic: %#v", got)
	}
}
