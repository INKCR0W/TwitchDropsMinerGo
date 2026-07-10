package pubsub

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func TestTopicHelpersMatchReferenceFormat(t *testing.T) {
	t.Parallel()

	userTopic := MustNewTopic(CategoryUser, TopicDrops, 42, nil)
	if userTopic.Key() != "user-drop-events.42" {
		t.Fatalf("用户 topic key 不匹配: %q", userTopic.Key())
	}

	channelTopic, err := ChannelTopic(TopicStreamUpdate, 99, nil)
	if err != nil {
		t.Fatalf("创建频道 topic 失败: %v", err)
	}
	if channelTopic.Key() != "broadcast-settings-update.99" {
		t.Fatalf("频道 topic key 不匹配: %q", channelTopic.Key())
	}

	prefix, targetID, ok := ParseTopicKey(channelTopic.Key())
	if !ok || prefix != "broadcast-settings-update" || targetID != 99 {
		t.Fatalf("解析 topic key 失败: prefix=%q targetID=%d ok=%v", prefix, targetID, ok)
	}
}

func TestManagerShardsAndRecyclesTopics(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Auth:            &stubAuthState{snapshot: auth.Snapshot{AccessToken: "token-1"}},
		MaxShards:       2,
		ShardTopicLimit: 2,
	})

	topic1 := MustNewTopic(CategoryUser, TopicDrops, 1, nil)
	topic2 := MustNewTopic(CategoryUser, TopicNotifications, 1, nil)
	topic3 := MustNewTopic(CategoryChannel, TopicStreamState, 10, nil)

	if err := manager.AddTopics(topic1, topic2, topic3); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	status := manager.Status()
	if status.TopicCount != 3 || len(status.Shards) != 2 {
		t.Fatalf("分片状态不匹配: %#v", status)
	}
	if status.Shards[0].TopicCount != 2 || status.Shards[1].TopicCount != 1 {
		t.Fatalf("分片分配不正确: %#v", status.Shards)
	}

	manager.RemoveTopics(topic2.Key())

	status = manager.Status()
	if status.TopicCount != 2 || len(status.Shards) != 1 {
		t.Fatalf("回收冗余分片失败: %#v", status)
	}
	if status.Shards[0].TopicCount != 2 {
		t.Fatalf("回收后 topic 数量不匹配: %#v", status.Shards)
	}
}

func TestManagerRejectsTopicOverflow(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Auth:            &stubAuthState{snapshot: auth.Snapshot{AccessToken: "token-4"}},
		MaxShards:       1,
		ShardTopicLimit: 1,
	})

	err := manager.AddTopics(
		MustNewTopic(CategoryUser, TopicDrops, 1, nil),
		MustNewTopic(CategoryUser, TopicNotifications, 1, nil),
	)
	if !errors.Is(err, ErrTopicLimitExceeded) {
		t.Fatalf("期望返回 ErrTopicLimitExceeded，实际为 %v", err)
	}
}

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

	listen1 := conn.waitForType(t, "LISTEN", time.Second)
	listen2 := conn.waitForType(t, "LISTEN", time.Second)
	if dialer.CallCount() != 1 {
		t.Fatalf("Dial 次数不匹配: %d", dialer.CallCount())
	}
	if got := dialer.Header(0).Get("X-Device-Id"); got != "device-1" {
		t.Fatalf("握手请求头未透传: %q", got)
	}
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
	conn.pushText(t, map[string]any{
		"type":  "RESPONSE",
		"nonce": listen1["nonce"],
	})
	conn.pushText(t, map[string]any{
		"type":  "RESPONSE",
		"nonce": listen2["nonce"],
	})
	if err := manager.WaitUntilConnected(context.Background()); err != nil {
		t.Fatalf("WaitUntilConnected 返回错误: %v", err)
	}

	manager.RemoveTopics(topics[0].Key(), topics[1].Key())

	unlisten := conn.waitForType(t, "UNLISTEN", time.Second)
	if got := len(topicsFromEnvelope(t, unlisten)); got != 2 {
		t.Fatalf("UNLISTEN 分批大小不匹配: %#v", unlisten)
	}
	assertAuthTokenAndNonce(t, unlisten, "token-1")
}
