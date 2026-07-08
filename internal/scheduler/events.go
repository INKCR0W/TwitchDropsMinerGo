package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
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

func (s *Scheduler) claimReadyDrops(ctx context.Context, candidates []claimCandidate) claimSweepResult {
	result := claimSweepResult{Total: len(candidates)}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			result.TimedOut = true
			return result
		}

		ok, err := s.claimDropRequest(ctx, candidate.ClaimID)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				result.TimedOut = true
				return result
			}
			result.Failed++
			s.logger.Warn("认领掉宝失败", "campaign_id", candidate.CampaignID, "drop_id", candidate.DropID, "error", err)
			continue
		}
		if ok {
			result.Claimed++
			s.markDropClaimed(candidate.DropID)
			s.logger.Info("认领掉宝成功", "campaign_id", candidate.CampaignID, "drop_id", candidate.DropID)
		}
	}

	return result
}

func (s *Scheduler) claimDropRequest(ctx context.Context, claimID string) (bool, error) {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return false, nil
	}

	operation, err := gql.MustLookup(gql.OperationClaimDrop).WithVariables(map[string]any{
		"input": map[string]any{
			"dropInstanceID": claimID,
		},
	})
	if err != nil {
		return false, fmt.Errorf("构造 ClaimDrop 请求失败: %w", err)
	}

	response, err := s.gqlClient.Do(ctx, operation)
	if err != nil {
		return false, err
	}

	data, err := asMap(response.Data, "data")
	if err != nil {
		return false, err
	}
	claimData := optionalMap(data["claimDropRewards"])
	switch stringValue(claimData, "status") {
	case "ELIGIBLE_FOR_ALL", "DROP_INSTANCE_ALREADY_CLAIMED":
		return true, nil
	default:
		return false, nil
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

func (s *Scheduler) processDropProgress(message dropEventMessage) {
	if message.Data.DropID == "" {
		return
	}

	watchingChannel := s.currentWatchingChannel()
	if !s.applyDropProgress(s.nowUTC(), watchingChannel, message.Data.DropID, message.Data.CurrentProgressMin) {
		return
	}

	attrs := []any{
		"drop_id", message.Data.DropID,
		"current_minutes", message.Data.CurrentProgressMin,
		"required_minutes", message.Data.RequiredProgressMin,
	}
	attrs = append(attrs, s.watchProgressAttrs(message.Data.DropID)...)
	s.logger.Info("收到掉宝进度更新", attrs...)
}

func (s *Scheduler) processDropClaim(ctx context.Context, message dropEventMessage) error {
	dropID := strings.TrimSpace(message.Data.DropID)
	claimID := strings.TrimSpace(message.Data.DropInstanceID)
	if dropID == "" {
		return nil
	}

	campaignID, effectiveClaimID, ok := s.updateDropClaim(dropID, claimID)
	if !ok {
		s.logger.Warn("收到未知掉宝的认领事件", "drop_id", dropID, "claim_id", claimID)
		return nil
	}

	claimed, err := s.claimDropRequest(ctx, effectiveClaimID)
	if err != nil {
		return fmt.Errorf("认领 websocket 掉宝失败: %w", err)
	}
	if claimed {
		s.markDropClaimed(dropID)
	}

	watchingChannel := s.currentWatchingChannel()
	if watchingChannel != nil {
		if err := s.sleep(ctx, 4*time.Second); err != nil {
			return err
		}
		for attempt := 0; attempt < 8; attempt++ {
			currentDropID, _, ok, err := s.fetchCurrentDrop(ctx, watchingChannel.ID)
			if err != nil {
				return fmt.Errorf("轮询 CurrentDrop 失败: %w", err)
			}
			if !ok || currentDropID != dropID {
				break
			}
			if err := s.sleep(ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}

	if s.campaignCanEarn(campaignID, watchingChannel) {
		s.restartWatching()
		return nil
	}

	s.ChangeState(StateInventoryFetch)
	return nil
}

func (s *Scheduler) deleteNotification(ctx context.Context, notificationID string) error {
	operation, err := gql.MustLookup(gql.OperationNotificationsDelete).WithVariables(map[string]any{
		"input": map[string]any{
			"id": notificationID,
		},
	})
	if err != nil {
		return fmt.Errorf("构造 NotificationsDelete 请求失败: %w", err)
	}

	if _, err := s.gqlClient.Do(ctx, operation); err != nil {
		return err
	}
	return nil
}

func (s *Scheduler) readyDrops(now time.Time) []claimCandidate {
	snapshot := s.snapshotCopy()
	ready := make([]claimCandidate, 0)
	for _, campaign := range snapshot.Inventory {
		if campaign == nil || campaign.UpcomingAt(now) {
			continue
		}
		for _, drop := range campaign.Drops() {
			if !drop.CanClaim(now) {
				continue
			}
			ready = append(ready, claimCandidate{
				CampaignID: campaign.ID,
				DropID:     drop.ID,
				ClaimID:    drop.ClaimID,
			})
		}
	}
	return ready
}

func (s *Scheduler) applyDropProgress(now time.Time, channel *domain.Channel, dropID string, currentMinutes int) bool {
	if channel == nil || strings.TrimSpace(dropID) == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil || !drop.CanEarn(now, channel, s.settings.EnableBadgesEmotes, false) {
		return false
	}

	drop.UpdateMinutes(currentMinutes)
	s.logDropOverviewLocked(drop.Campaign, drop)
	s.lastProgressAt = now.UTC()
	return true
}

func (s *Scheduler) bumpActiveCampaign(now time.Time, channel *domain.Channel) (bool, bool, bool) {
	if channel == nil {
		return false, false, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	activeCampaign := s.activeCampaignLocked(now, channel)
	if activeCampaign == nil {
		activeCampaign = s.pendingRewardCompletionCampaignLocked(now, channel)
		if activeCampaign == nil {
			return false, false, false
		}
	}
	if activeCampaign.IsRewardCampaign {
		completed := activeCampaign.BumpRewardMinutes(now, channel, s.settings.EnableBadgesEmotes, false)
		s.lastProgressAt = now.UTC()
		s.logDropOverviewLocked(activeCampaign, activeCampaign.FirstEarnableDrop(now, channel, s.settings.EnableBadgesEmotes, false))
		if completed {
			completed = s.recordRewardCompletedLocked(activeCampaign, now)
		}
		return completed, false, true
	}

	reachedLimit := activeCampaign.BumpMinutes(now, channel, s.settings.EnableBadgesEmotes, false)
	s.lastProgressAt = now.UTC()
	s.logDropOverviewLocked(activeCampaign, activeCampaign.FirstEarnableDrop(now, channel, s.settings.EnableBadgesEmotes, false))
	return false, reachedLimit, true
}

func (s *Scheduler) recordRewardCompletedLocked(campaign *domain.DropsCampaign, now time.Time) bool {
	if s == nil || s.rewardProgress == nil || campaign == nil || !campaign.IsRewardCampaign {
		return false
	}

	var saved bool
	for _, drop := range campaign.Drops() {
		if drop == nil || drop.CurrentMinutes() < drop.RequiredMinutes {
			continue
		}
		if _, err := s.rewardProgress.RecordCompletion(campaign.ID, drop.ID, drop.CurrentMinutes(), now, campaign.EndsAt); err != nil {
			s.logger.Warn("保存 reward campaign 完成状态失败", "campaign_id", campaign.ID, "drop_id", drop.ID, "error", err)
			continue
		}
		saved = true
		drop.MarkClaimed()
		s.logger.Info(
			"reward campaign 已达到所需观看时间，请到兑换页面领取",
			"campaign_id", campaign.ID,
			"drop_id", drop.ID,
			"reward", drop.Name,
			"redeem_url", campaign.LinkURL,
		)
	}
	if !saved {
		return false
	}
	if aware, ok := s.refresher.(rewardProgressAwareRefresher); ok {
		aware.UpdateRewardProgress(s.rewardProgress.Snapshot())
	}
	return true
}

func (s *Scheduler) updateDropClaim(dropID string, claimID string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil {
		return "", "", false
	}
	if claimID != "" {
		drop.UpdateClaim(claimID)
	}

	campaignID := ""
	if drop.Campaign != nil {
		campaignID = drop.Campaign.ID
	}
	return campaignID, drop.ClaimID, true
}

func (s *Scheduler) markDropClaimed(dropID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil {
		return false
	}
	return drop.MarkClaimed()
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
