package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (t *Tracker) ProcessStreamState(ctx context.Context, channelID int64, message json.RawMessage) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}

	var parsed streamStateMessage
	if err := json.Unmarshal(message, &parsed); err != nil {
		return fmt.Errorf("解析 StreamState 消息失败: %w", err)
	}

	switch strings.TrimSpace(parsed.Type) {
	case "viewcount":
		channel, ok := t.Channel(channelID)
		if !ok {
			return ErrChannelNotTracked
		}
		if !channel.Online() {
			return t.CheckOnline(channelID)
		}
		t.mu.Lock()
		tracked := t.channels[channelID]
		if tracked != nil && tracked.channel != nil && tracked.channel.Stream != nil {
			tracked.channel.Stream.Viewers = parsed.Viewers
		}
		t.mu.Unlock()
		return nil
	case "stream-down":
		t.setOffline(channelID)
		return nil
	case "stream-up":
		return t.CheckOnline(channelID)
	case "commercial":
		return nil
	default:
		return nil
	}
}

func (t *Tracker) ProcessStreamUpdate(ctx context.Context, channelID int64, message json.RawMessage) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}
	if _, ok := t.Channel(channelID); !ok {
		return ErrChannelNotTracked
	}
	if len(message) > 0 && !json.Valid(message) {
		return fmt.Errorf("解析 StreamUpdate 消息失败: 无效 JSON")
	}

	return t.CheckOnline(channelID)
}
