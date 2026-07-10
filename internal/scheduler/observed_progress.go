package scheduler

import (
	"time"

	"twitchdropsminergo/internal/inventory"
)

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
