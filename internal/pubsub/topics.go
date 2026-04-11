package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DefaultEndpoint        = "wss://pubsub-edge.twitch.tv/v1"
	DefaultMaxShards       = 8
	DefaultShardTopicLimit = 50
	DefaultListenBatchSize = 20
	DefaultReadTimeout     = 500 * time.Millisecond
	BaseTopics             = 2
	TopicsPerChannel       = 2
	MaxChannels            = ((DefaultMaxShards * DefaultShardTopicLimit) - BaseTopics) / TopicsPerChannel
)

type Category string

const (
	CategoryUser    Category = "User"
	CategoryChannel Category = "Channel"
)

type Name string

const (
	TopicPresence          Name = "Presence"
	TopicDrops             Name = "Drops"
	TopicNotifications     Name = "Notifications"
	TopicCommunityPoints   Name = "CommunityPoints"
	TopicStreamState       Name = "StreamState"
	TopicStreamUpdate      Name = "StreamUpdate"
	TopicChannelDropEvents Name = "ChannelDrops"
)

var topicPrefixes = map[Category]map[Name]string{
	CategoryUser: {
		TopicPresence:        "presence",
		TopicDrops:           "user-drop-events",
		TopicNotifications:   "onsite-notifications",
		TopicCommunityPoints: "community-points-user-v1",
	},
	CategoryChannel: {
		TopicChannelDropEvents: "channel-drop-events",
		TopicStreamState:       "video-playback-by-id",
		TopicStreamUpdate:      "broadcast-settings-update",
		TopicCommunityPoints:   "community-points-channel-v1",
	},
}

type Event struct {
	Topic      Topic
	Message    json.RawMessage
	ReceivedAt time.Time
}

type Handler func(context.Context, Event) error

type Topic struct {
	category Category
	name     Name
	targetID int64
	key      string
	handler  Handler
}

func NewTopic(category Category, name Name, targetID int64, handler Handler) (Topic, error) {
	if targetID <= 0 {
		return Topic{}, fmt.Errorf("topic target_id 必须大于 0")
	}

	key, err := TopicKey(category, name, targetID)
	if err != nil {
		return Topic{}, err
	}

	return Topic{
		category: category,
		name:     name,
		targetID: targetID,
		key:      key,
		handler:  handler,
	}, nil
}

func MustNewTopic(category Category, name Name, targetID int64, handler Handler) Topic {
	topic, err := NewTopic(category, name, targetID, handler)
	if err != nil {
		panic(err)
	}

	return topic
}

func UserTopic(name Name, userID int64, handler Handler) (Topic, error) {
	return NewTopic(CategoryUser, name, userID, handler)
}

func ChannelTopic(name Name, channelID int64, handler Handler) (Topic, error) {
	return NewTopic(CategoryChannel, name, channelID, handler)
}

func TopicKey(category Category, name Name, targetID int64) (string, error) {
	prefixes, ok := topicPrefixes[category]
	if !ok {
		return "", fmt.Errorf("不支持的 topic 分类 %q", category)
	}

	prefix, ok := prefixes[name]
	if !ok {
		return "", fmt.Errorf("分类 %q 不支持 topic %q", category, name)
	}

	return fmt.Sprintf("%s.%d", prefix, targetID), nil
}

func ParseTopicKey(key string) (string, int64, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", 0, false
	}

	index := strings.LastIndexByte(key, '.')
	if index <= 0 || index == len(key)-1 {
		return "", 0, false
	}

	prefix := key[:index]
	var targetID int64
	if _, err := fmt.Sscanf(key[index+1:], "%d", &targetID); err != nil || targetID <= 0 {
		return "", 0, false
	}

	return prefix, targetID, true
}

func NormalizeTopicKeys(keys []string) []string {
	if len(keys) == 0 {
		return []string{}
	}

	seen := make(map[string]struct{}, len(keys))
	normalized := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}

	sort.Strings(normalized)
	return normalized
}

func (t Topic) Category() Category {
	return t.category
}

func (t Topic) Name() Name {
	return t.name
}

func (t Topic) TargetID() int64 {
	return t.targetID
}

func (t Topic) Key() string {
	return t.key
}

func (t Topic) String() string {
	return t.key
}

func (t Topic) Handler() Handler {
	return t.handler
}
