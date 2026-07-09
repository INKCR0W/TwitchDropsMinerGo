package pubsub

import (
	"context"
	"sync"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/httpclient"
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
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
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

func TestManagerBacksOffAfterEstablishedConnectionFailure(t *testing.T) {
	t.Parallel()

	conn1 := newFakeConn()
	conn2 := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn1, conn2}}
	sleepCalls := make(chan time.Duration, 2)

	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-backoff"},
		},
		Dialer:          dialer,
		ReadTimeout:     5 * time.Millisecond,
		PingInterval:    time.Hour,
		ShardTopicLimit: 10,
		Backoff: httpclient.BackoffConfig{
			Base:     2,
			Variance: 0,
			Maximum:  time.Minute,
		},
		Sleep: func(ctx context.Context, delay time.Duration) error {
			select {
			case sleepCalls <- delay:
			default:
			}
			return nil
		},
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
		_ = manager.Stop(stopCtx, true)
	}()

	conn1.waitForType(t, "LISTEN", time.Second)
	if err := conn1.Close(); err != nil {
		t.Fatalf("关闭首个连接失败: %v", err)
	}

	select {
	case delay := <-sleepCalls:
		if delay != time.Second {
			t.Fatalf("首次连接后失败退避不匹配: %v", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("连接后失败应触发退避 sleep")
	}

	waitUntil(t, time.Second, func() bool { return dialer.CallCount() == 2 })
	_ = conn2.waitForType(t, "LISTEN", time.Second)
}

type testClock struct {
	mu      sync.Mutex
	current time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *testClock) advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(delta)
}

func newBackoffTestManager(t *testing.T, clock *testClock, dialer *fakeDialer, sleepCalls chan time.Duration) *Manager {
	t.Helper()

	return newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-backoff-reset"},
		},
		Dialer:          dialer,
		ReadTimeout:     5 * time.Millisecond,
		PingInterval:    time.Hour,
		PingTimeout:     time.Hour,
		ShardTopicLimit: 10,
		Now:             clock.now,
		Backoff: httpclient.BackoffConfig{
			Base:     2,
			Variance: 0,
			Maximum:  time.Minute,
		},
		Sleep: func(_ context.Context, delay time.Duration) error {
			select {
			case sleepCalls <- delay:
			default:
			}
			return nil
		},
	})
}

func startBackoffTestManager(t *testing.T, manager *Manager, userID int64) {
	t.Helper()

	if err := manager.AddTopics(MustNewTopic(CategoryUser, TopicDrops, userID, nil)); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start 返回错误: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = manager.Stop(stopCtx, true)
	})
}

func nextSleepDelay(t *testing.T, sleepCalls chan time.Duration, label string) time.Duration {
	t.Helper()

	select {
	case delay := <-sleepCalls:
		return delay
	case <-time.After(time.Second):
		t.Fatalf("%s 后应触发退避 sleep", label)
		return 0
	}
}

func TestManagerResetsBackoffAfterLongLivedConnection(t *testing.T) {
	t.Parallel()

	clock := &testClock{current: time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)}
	conn1 := newFakeConn()
	conn2 := newFakeConn()
	conn3 := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn1, conn2, conn3}}
	sleepCalls := make(chan time.Duration, 4)

	manager := newBackoffTestManager(t, clock, dialer, sleepCalls)
	startBackoffTestManager(t, manager, 78)

	for index, conn := range []*fakeConn{conn1, conn2} {
		conn.waitForType(t, "LISTEN", time.Second)
		clock.advance(minConnectionLifetimeForBackoffReset)
		if err := conn.Close(); err != nil {
			t.Fatalf("关闭第 %d 个连接失败: %v", index+1, err)
		}
		if delay := nextSleepDelay(t, sleepCalls, "连接断开"); delay != time.Second {
			t.Fatalf("连接存活足够久后退避应重置为首档, 第 %d 次 got=%v", index+1, delay)
		}
	}

	waitUntil(t, time.Second, func() bool { return dialer.CallCount() == 3 })
	_ = conn3.waitForType(t, "LISTEN", time.Second)
}

func TestManagerEscalatesBackoffOnShortLivedConnections(t *testing.T) {
	t.Parallel()

	clock := &testClock{current: time.Date(2026, 4, 11, 8, 0, 0, 0, time.UTC)}
	conn1 := newFakeConn()
	conn2 := newFakeConn()
	conn3 := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn1, conn2, conn3}}
	sleepCalls := make(chan time.Duration, 4)

	manager := newBackoffTestManager(t, clock, dialer, sleepCalls)
	startBackoffTestManager(t, manager, 79)

	conn1.waitForType(t, "LISTEN", time.Second)
	if err := conn1.Close(); err != nil {
		t.Fatalf("关闭首个连接失败: %v", err)
	}
	if delay := nextSleepDelay(t, sleepCalls, "首个连接断开"); delay != time.Second {
		t.Fatalf("首次重连退避不匹配: %v", delay)
	}

	conn2.waitForType(t, "LISTEN", time.Second)
	if err := conn2.Close(); err != nil {
		t.Fatalf("关闭第二个连接失败: %v", err)
	}
	if delay := nextSleepDelay(t, sleepCalls, "第二个连接断开"); delay != 2*time.Second {
		t.Fatalf("握手后立即断开不应重置退避，应继续升级: %v", delay)
	}
}
