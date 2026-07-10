package scheduler

import (
	"strings"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/inventory"
)

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

func (s *Scheduler) applyDropProgress(now time.Time, channel *domain.Channel, dropID string, currentMinutes int) bool {
	if channel == nil || strings.TrimSpace(dropID) == "" {
		return false
	}

	campaignID, expiresAt, ok := s.observeDropProgress(now, channel, dropID, currentMinutes)
	if !ok {
		return false
	}
	s.recordObservedMinutes(campaignID, dropID, currentMinutes, expiresAt, now)
	return true
}

func (s *Scheduler) observeDropProgress(now time.Time, channel *domain.Channel, dropID string, currentMinutes int) (string, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil || drop.Campaign == nil || !drop.CanEarn(now, channel, s.settings.EnableBadgesEmotes, false) {
		return "", time.Time{}, false
	}

	campaign := drop.Campaign
	beforeReal := drop.RealCurrentMinutes
	campaign.ObserveMinutes(drop, currentMinutes)
	s.logDropOverviewLocked(campaign, campaign.FirstEarnableDrop(now, channel, s.settings.EnableBadgesEmotes, false))
	s.lastProgressAt = now.UTC()
	if drop.RealCurrentMinutes > beforeReal {
		s.lastAdvanceAt = now.UTC()
	}
	return campaign.ID, drop.EndsAt, true
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
	// 本地估算推进也算进度, 否则只靠本地计时的 reward 活动会被误判卡住
	if activeCampaign.IsRewardCampaign {
		completed := activeCampaign.BumpRewardMinutes(now, channel, s.settings.EnableBadgesEmotes, false)
		s.lastProgressAt = now.UTC()
		s.lastAdvanceAt = now.UTC()
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

func (s *Scheduler) recordObservedMinutes(campaignID string, dropID string, minutes int, expiresAt time.Time, now time.Time) {
	if s.watchProgress == nil {
		return
	}
	if err := s.watchProgress.Record(campaignID, dropID, minutes, expiresAt, now); err != nil {
		s.logger.Warn("保存观看进度失败", "campaign_id", campaignID, "drop_id", dropID, "error", err)
	}
}

func (s *Scheduler) seedObservedProgress(snapshot inventory.Snapshot) {
	if s.watchProgress == nil {
		return
	}

	seeded := 0
	for _, entry := range s.watchProgress.Snapshot() {
		campaign := snapshot.Campaigns[entry.CampaignID]
		drop := campaign.Drop(entry.DropID)
		if drop == nil {
			continue
		}
		if campaign.ObserveMinutes(drop, entry.MinutesWatched) {
			seeded++
		}
	}
	if seeded > 0 {
		s.logger.Info("已回灌本地记录的观看进度", "drop_count", seeded)
	}
}

func (s *Scheduler) pruneObservedProgress() {
	if s.watchProgress == nil {
		return
	}

	removed, err := s.watchProgress.PruneExpired(s.nowUTC())
	if err != nil {
		s.logger.Warn("清理过期观看进度失败", "error", err)
		return
	}
	if removed > 0 {
		s.logger.Info("已清理过期观看进度", "removed_count", removed)
	}
}
