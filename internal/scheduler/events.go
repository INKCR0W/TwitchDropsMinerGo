package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/pubsub"
)

func (s *Scheduler) handleStreamState(ctx context.Context, event pubsub.Event) error {
	return s.tracker.ProcessStreamState(ctx, event.Topic.TargetID(), event.Message)
}

func (s *Scheduler) handleStreamUpdate(ctx context.Context, event pubsub.Event) error {
	return s.tracker.ProcessStreamUpdate(ctx, event.Topic.TargetID(), event.Message)
}

func (s *Scheduler) handleDropEvent(ctx context.Context, event pubsub.Event) error {
	var message dropEventMessage
	if err := json.Unmarshal(event.Message, &message); err != nil {
		return fmt.Errorf("解析掉宝 PubSub 事件失败: %w", err)
	}

	switch message.Type {
	case "drop-progress":
		s.processDropProgress(message)
	case "drop-claim":
		return s.processDropClaim(ctx, message)
	}

	return nil
}

func (s *Scheduler) handleNotificationEvent(ctx context.Context, event pubsub.Event) error {
	var message notificationEventMessage
	if err := json.Unmarshal(event.Message, &message); err != nil {
		return fmt.Errorf("解析通知 PubSub 事件失败: %w", err)
	}

	if message.Type != "create-notification" {
		return nil
	}

	notification := message.Data.Notification
	switch notification.Type {
	case "user_drop_reward_reminder_notification", "quests_viewer_reward_campaign_earned_emote":
		s.ChangeState(StateInventoryFetch)
		if strings.TrimSpace(notification.ID) == "" {
			s.logger.Warn("奖励通知缺少 id，跳过删除", "type", notification.Type)
			return nil
		}
		if err := s.deleteNotification(ctx, notification.ID); err != nil {
			return fmt.Errorf("删除奖励通知失败: %w", err)
		}
	}

	return nil
}

func (s *Scheduler) onChannelChange(before, after domain.Channel) {
	if s == nil || after.ID <= 0 {
		return
	}

	s.mu.Lock()
	if _, exists := s.channels[after.ID]; exists {
		s.channels[after.ID] = cloneChannel(after)
	}
	watchingChannelID := s.watchingChannelID
	state := s.state
	s.mu.Unlock()

	if state == StateInventoryFetch || state == StateGamesUpdate || state == StateChannelsCleanup || state == StateChannelsFetch || state == StateExit {
		return
	}

	if state == StateIdle {
		if after.Online() && s.canWatch(after) {
			s.ChangeState(StateChannelSwitch)
		}
		return
	}

	if !before.Online() {
		if after.Online() && s.canWatch(after) && s.shouldSwitch(after) {
			s.watch(after.ID)
		}
		return
	}

	if watchingChannelID != 0 && watchingChannelID == after.ID {
		if !s.canWatch(after) {
			s.ChangeState(StateChannelSwitch)
		}
		return
	}

	if after.Online() && s.canWatch(after) && s.shouldSwitch(after) {
		s.watch(after.ID)
	}
}

func (s *Scheduler) ensureUserTopics() error {
	authSnapshot := s.authState.Snapshot()
	userID := authSnapshot.UserID
	if userID <= 0 {
		return fmt.Errorf("认证状态缺少 user_id")
	}

	s.mu.Lock()
	previousUserID := s.userTopicUserID
	s.userTopicUserID = userID
	s.mu.Unlock()

	if previousUserID > 0 && previousUserID != userID {
		s.pubsub.RemoveTopics(userTopicKeys(previousUserID)...)
	}

	dropTopic, err := pubsub.UserTopic(pubsub.TopicDrops, userID, s.handleDropEvent)
	if err != nil {
		return err
	}
	notificationTopic, err := pubsub.UserTopic(pubsub.TopicNotifications, userID, s.handleNotificationEvent)
	if err != nil {
		return err
	}
	return s.pubsub.AddTopics(dropTopic, notificationTopic)
}

func userTopicKeys(userID int64) []string {
	if userID <= 0 {
		return nil
	}

	dropsKey, err := pubsub.TopicKey(pubsub.CategoryUser, pubsub.TopicDrops, userID)
	if err != nil {
		return nil
	}
	notificationsKey, err := pubsub.TopicKey(pubsub.CategoryUser, pubsub.TopicNotifications, userID)
	if err != nil {
		return []string{dropsKey}
	}
	return []string{dropsKey, notificationsKey}
}
