package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func TestShardDefersSubmittedUntilListenAck(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-ack"},
		},
		PingInterval: time.Hour,
	})
	shard := newShard(manager, 0)
	topic := MustNewTopic(CategoryUser, TopicDrops, 123, nil)
	if !shard.addTopic(topic, 10) {
		t.Fatal("addTopic 应成功")
	}

	conn := newFakeConn()
	if err := shard.syncTopics(context.Background(), conn); err != nil {
		t.Fatalf("syncTopics 返回错误: %v", err)
	}
	listen := conn.waitForType(t, "LISTEN", time.Second)
	nonce, _ := listen["nonce"].(string)
	if nonce == "" {
		t.Fatalf("LISTEN 应包含 nonce: %#v", listen)
	}
	if got := shard.status().SubmittedCount; got != 0 {
		t.Fatalf("LISTEN ack 前不应标记 submitted，实际为 %d", got)
	}
	if state := shard.status().State; state != ShardStateDisconnected {
		t.Fatalf("未运行的 shard 状态不应被 ack 刷成 connected，实际为 %s", state)
	}

	payload, err := json.Marshal(map[string]any{
		"type":  "RESPONSE",
		"nonce": nonce,
	})
	if err != nil {
		t.Fatalf("编码 RESPONSE 失败: %v", err)
	}
	if _, _, err := shard.handleInbound(context.Background(), textMessageType, payload); err != nil {
		t.Fatalf("处理 RESPONSE 失败: %v", err)
	}
	if got := shard.status().SubmittedCount; got != 1 {
		t.Fatalf("LISTEN ack 后应标记 submitted，实际为 %d", got)
	}
}

func TestShardDefersUnsubmittedUntilUnlistenAck(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-unlisten-ack"},
		},
		PingInterval: time.Hour,
	})
	shard := newShard(manager, 0)
	topic := MustNewTopic(CategoryUser, TopicDrops, 124, nil)
	if !shard.addTopic(topic, 10) {
		t.Fatal("addTopic 应成功")
	}

	conn := newFakeConn()
	if err := shard.syncTopics(context.Background(), conn); err != nil {
		t.Fatalf("LISTEN syncTopics 返回错误: %v", err)
	}
	listen := conn.waitForType(t, "LISTEN", time.Second)
	listenNonce, _ := listen["nonce"].(string)
	if listenNonce == "" {
		t.Fatalf("LISTEN 应包含 nonce: %#v", listen)
	}
	listenAck, err := json.Marshal(map[string]any{
		"type":  "RESPONSE",
		"nonce": listenNonce,
	})
	if err != nil {
		t.Fatalf("编码 LISTEN RESPONSE 失败: %v", err)
	}
	if _, _, err := shard.handleInbound(context.Background(), textMessageType, listenAck); err != nil {
		t.Fatalf("处理 LISTEN RESPONSE 失败: %v", err)
	}
	if got := shard.status().SubmittedCount; got != 1 {
		t.Fatalf("LISTEN ack 后应标记 submitted，实际为 %d", got)
	}

	shard.removeTopics([]string{topic.Key()})
	if err := shard.syncTopics(context.Background(), conn); err != nil {
		t.Fatalf("UNLISTEN syncTopics 返回错误: %v", err)
	}
	unlisten := conn.waitForType(t, "UNLISTEN", time.Second)
	unlistenNonce, _ := unlisten["nonce"].(string)
	if unlistenNonce == "" {
		t.Fatalf("UNLISTEN 应包含 nonce: %#v", unlisten)
	}
	if got := shard.status().SubmittedCount; got != 1 {
		t.Fatalf("UNLISTEN ack 前不应清除 submitted，实际为 %d", got)
	}

	unlistenAck, err := json.Marshal(map[string]any{
		"type":  "RESPONSE",
		"nonce": unlistenNonce,
	})
	if err != nil {
		t.Fatalf("编码 UNLISTEN RESPONSE 失败: %v", err)
	}
	if _, _, err := shard.handleInbound(context.Background(), textMessageType, unlistenAck); err != nil {
		t.Fatalf("处理 UNLISTEN RESPONSE 失败: %v", err)
	}
	if got := shard.status().SubmittedCount; got != 0 {
		t.Fatalf("UNLISTEN ack 后应清除 submitted，实际为 %d", got)
	}
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

func TestShardResponseErrorClearsSubmittedAndReconnects(t *testing.T) {
	t.Parallel()

	conn1 := newFakeConn()
	conn2 := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn1, conn2}}
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-listen"},
		},
		Dialer:          dialer,
		ReadTimeout:     5 * time.Millisecond,
		PingInterval:    time.Hour,
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
		_ = manager.Stop(stopCtx, true)
	}()

	listen := conn1.waitForType(t, "LISTEN", time.Second)
	nonce, _ := listen["nonce"].(string)
	if nonce == "" {
		t.Fatalf("LISTEN 应包含 nonce: %#v", listen)
	}

	conn1.pushText(t, map[string]any{
		"type":  "RESPONSE",
		"nonce": nonce,
		"error": "ERR_BADAUTH",
	})

	waitUntil(t, time.Second, func() bool {
		return dialer.CallCount() == 2
	})
	_ = conn2.waitForType(t, "LISTEN", time.Second)
}

func TestShardPerTopicErrorDropsTopicWithoutReconnect(t *testing.T) {
	t.Parallel()

	conn1 := newFakeConn()
	conn2 := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn1, conn2}}
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-badtopic"},
		},
		Dialer:          dialer,
		ReadTimeout:     5 * time.Millisecond,
		PingInterval:    time.Hour,
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
		_ = manager.Stop(stopCtx, true)
	}()

	listen := conn1.waitForType(t, "LISTEN", time.Second)
	nonce, _ := listen["nonce"].(string)
	if nonce == "" {
		t.Fatalf("LISTEN 应包含 nonce: %#v", listen)
	}

	// 非认证类 per-topic 错误：应丢弃该 topic 并保持连接，而不是拆连接重连
	conn1.pushText(t, map[string]any{
		"type":  "RESPONSE",
		"nonce": nonce,
		"error": "ERR_BADTOPIC",
	})

	waitUntil(t, time.Second, func() bool {
		return manager.Status().TopicCount == 0
	})
	if got := dialer.CallCount(); got != 1 {
		t.Fatalf("per-topic 错误不应触发重连，实际 dial 次数为 %d", got)
	}
}
