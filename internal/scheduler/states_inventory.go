package scheduler

import (
	"context"
	"fmt"

	"twitchdropsminergo/internal/inventory"
)

func (s *Scheduler) handleInventoryFetch(ctx context.Context) error {
	s.logger.Info("开始刷新 inventory")

	s.syncRewardProgressToRefresher()
	s.pruneObservedProgress()
	if err := s.pubsub.Start(ctx); err != nil {
		return fmt.Errorf("启动 PubSub 失败: %w", err)
	}
	s.logger.Info("PubSub 已启动，开始校验认证并拉取 inventory")

	snapshot, err := s.refresher.Refresh(ctx, inventory.RefreshOptions{
		EnableBadgesEmotes: s.settingsCopy().EnableBadgesEmotes,
	})
	if err != nil {
		return fmt.Errorf("刷新 inventory 失败: %w", err)
	}
	s.logger.Info(
		"inventory 刷新完成",
		"campaign_count", len(snapshot.Campaigns),
		"drop_count", len(snapshot.Drops),
		"maintenance_trigger_count", len(snapshot.MaintenanceTriggers),
	)
	s.seedObservedProgress(snapshot)

	s.mu.Lock()
	s.snapshot = snapshot
	s.mu.Unlock()

	if err := s.ensureUserTopics(); err != nil {
		return fmt.Errorf("订阅用户 PubSub topic 失败: %w", err)
	}
	s.logger.Info("inventory 已装载，准备进入游戏筛选阶段")
	s.clearRuntimeError()
	s.restartMaintenance(ctx, snapshot.MaintenanceTriggers)
	s.advanceState(StateInventoryFetch, StateGamesUpdate)
	return nil
}

func (s *Scheduler) syncRewardProgressToRefresher() {
	if s == nil || s.rewardProgress == nil {
		return
	}
	if removed, err := s.rewardProgress.PruneExpired(s.nowUTC(), s.rewardPruneGrace); err != nil {
		s.logger.Warn("清理过期 reward campaign 完成状态失败", "error", err)
	} else if removed > 0 {
		s.logger.Info("已清理过期 reward campaign 完成状态", "removed_count", removed)
	}
	aware, ok := s.refresher.(rewardProgressAwareRefresher)
	if !ok {
		return
	}
	aware.UpdateRewardProgress(s.rewardProgress.Snapshot())
}

func (s *Scheduler) handleGamesUpdate(ctx context.Context) error {
	now := s.nowUTC()
	ready := s.readyDrops(now)
	s.logger.Info("开始处理游戏筛选阶段", "ready_drop_count", len(ready), "claim_timeout", s.claimSweepTimeout.String())

	claimCtx, cancel := context.WithTimeout(ctx, s.claimSweepTimeout)
	claimResult := s.claimReadyDrops(claimCtx, ready)
	cancel()
	s.logger.Info(
		"待认领掉宝处理完成",
		"ready_drop_count", claimResult.Total,
		"claimed_count", claimResult.Claimed,
		"failed_count", claimResult.Failed,
		"timed_out", claimResult.TimedOut,
	)

	previousWantedGames := s.WantedGames()
	wantedGames := s.computeWantedGames(now)
	s.mu.Lock()
	s.wantedGames = wantedGames
	s.fullCleanup = true
	s.mu.Unlock()
	s.logWantedGamesUpdate(previousWantedGames, wantedGames)
	s.logger.Info("游戏筛选完成", "wanted_game_count", len(wantedGames))

	s.restartWatching()
	s.clearRuntimeError()
	s.advanceState(StateGamesUpdate, StateChannelsCleanup)
	return nil
}
