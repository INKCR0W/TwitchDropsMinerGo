package pubsub

import (
	"errors"
	"testing"

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
