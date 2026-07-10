package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

var errWatchInterrupted = errors.New("watch 循环被中断")

func (s *Scheduler) watch(channelID int64) {
	var (
		channel domain.Channel
		ok      bool
	)

	s.mu.Lock()
	changed := s.watchingChannelID != channelID
	s.watchingChannelID = channelID
	if changed {
		s.resetProgressAnnouncementsLocked()
	}
	channel, ok = s.channels[channelID]
	s.mu.Unlock()

	if changed {
		if ok {
			s.logger.Info("切换观看频道", watchingLogAttrs(channel)...)
		} else {
			s.logger.Info("切换观看频道", "channel_id", channelID)
		}
		s.signalWatch()
	}
}

func (s *Scheduler) stopWatching() {
	var (
		channel   domain.Channel
		ok        bool
		channelID int64
	)

	s.mu.Lock()
	changed := s.watchingChannelID != 0 || !s.lastProgressAt.IsZero()
	channelID = s.watchingChannelID
	channel, ok = s.channels[channelID]
	s.watchingChannelID = 0
	s.lastProgressAt = time.Time{}
	s.mu.Unlock()

	if changed {
		if channelID != 0 {
			if ok {
				s.logger.Info("停止观看频道", watchingLogAttrs(channel)...)
			} else {
				s.logger.Info("停止观看频道", "channel_id", channelID)
			}
		}
		s.signalWatch()
	}
}

func (s *Scheduler) restartWatching() {
	s.signalWatch()
}

func (s *Scheduler) watchLoop(ctx context.Context) {
	defer s.wg.Done()

	for {
		channelID, ok := s.waitForWatchingChannel(ctx)
		if !ok {
			return
		}

		channel, exists := s.channel(channelID)
		if !exists || !channel.Online() {
			s.stopWatching()
			continue
		}

		sentAt := s.nowUTC()
		succeeded, err := s.tracker.SendWatch(ctx, channelID)
		watchReported := err == nil && succeeded
		if err != nil {
			s.logger.Warn("发送 watch 请求失败", "channel_id", channelID, "error", err)
		} else if !succeeded {
			s.logger.Warn("watch 请求未成功", "channel_id", channelID)
		}

		if err := s.sleepWithWatchSignal(ctx, s.progressDelay); err != nil {
			if errors.Is(err, errWatchInterrupted) {
				continue
			}
			return
		}

		if s.shouldResolveProgress(sentAt) {
			s.resolveProgress(ctx, channel, watchReported)
		}

		elapsed := s.nowUTC().Sub(sentAt)
		if elapsed > s.watchInterval {
			elapsed = s.watchInterval
		}
		if err := s.sleepWithWatchSignal(ctx, s.watchInterval-elapsed); err != nil {
			if errors.Is(err, errWatchInterrupted) {
				continue
			}
			return
		}
	}
}

func (s *Scheduler) resolveProgress(ctx context.Context, channel domain.Channel, watchReported bool) {
	if result := s.syncProgressFromGQL(ctx, channel); result.Updated {
		if result.RecomputeGames {
			s.ChangeState(StateGamesUpdate)
		} else {
			s.refreshWhenChannelExhausted(channel)
		}
		return
	}

	if !watchReported {
		return
	}

	now := s.nowUTC()

	completedReward, reachedLimit, updated := s.bumpActiveCampaign(now, &channel)
	if !updated {
		s.refreshWhenChannelExhausted(channel)
		return
	}
	if completedReward {
		s.ChangeState(StateInventoryFetch)
		return
	}
	if reachedLimit {
		s.ChangeState(StateChannelSwitch)
	}
}

// 观看时长打满但认领事件迟到/丢失时, 频道上不再有可推进的活动。此时立即刷新 inventory
// 去认领掉宝并重算规划, 否则 watch 循环会一直向该频道空转到下次维护重载
func (s *Scheduler) refreshWhenChannelExhausted(channel domain.Channel) {
	now := s.nowUTC()

	s.mu.RLock()
	exhausted := s.activeCampaignLocked(now, &channel) == nil &&
		s.pendingRewardCompletionCampaignLocked(now, &channel) == nil
	s.mu.RUnlock()
	if !exhausted {
		return
	}

	s.logger.Info("当前频道已无可推进的活动，刷新 inventory", watchingLogAttrs(channel)...)
	s.ChangeState(StateInventoryFetch)
}

func (s *Scheduler) preflightSpecialEventFullProgress(ctx context.Context, channel domain.Channel) bool {
	game := channel.CurrentGame()
	if game == nil || !game.IsSpecialEvents() {
		return false
	}

	dropID, currentMinutes, ok, err := s.fetchCurrentDrop(ctx, channel.ID)
	if err != nil {
		s.logger.Warn("预检查 CurrentDrop 失败，继续切台", "channel_id", channel.ID, "error", err)
		return false
	}
	if !ok {
		return false
	}

	now := s.nowUTC()
	var (
		campaignName string
		dropName     string
		required     int
		recompute    bool
	)
	s.mu.Lock()
	drop := s.snapshot.Drops[dropID]
	if drop != nil && drop.Campaign != nil && drop.Campaign.Game.IsSpecialEvents() && drop.RequiredMinutes > 0 && currentMinutes >= drop.RequiredMinutes {
		if drop.CanEarn(now, &channel, s.settings.EnableBadgesEmotes, false) {
			drop.UpdateMinutes(currentMinutes)
			recompute = drop.Campaign.NormalizeSpecialEventMilestones()
			campaignName = drop.Campaign.Name
			dropName = drop.Name
			required = drop.RequiredMinutes
		}
	}
	s.mu.Unlock()
	if !recompute {
		return false
	}

	s.logger.Info(
		"预检查发现 Special Events 掉落已满进度，重算规划",
		"channel_id", channel.ID,
		"channel_login", channel.Login,
		"campaign", campaignName,
		"drop", dropName,
		"current_minutes", currentMinutes,
		"required_minutes", required,
	)
	s.ChangeState(StateGamesUpdate)
	return true
}

func (s *Scheduler) syncProgressFromGQL(ctx context.Context, channel domain.Channel) progressApplyResult {
	dropID, currentMinutes, ok, err := s.fetchCurrentDrop(ctx, channel.ID)
	if err != nil {
		s.logger.Warn("查询 CurrentDrop 失败，回退到本地估算", "channel_id", channel.ID, "error", err)
		return progressApplyResult{}
	}
	if !ok {
		return progressApplyResult{}
	}

	return s.applyDropProgressResult(s.nowUTC(), &channel, dropID, currentMinutes)
}

func (s *Scheduler) fetchCurrentDrop(ctx context.Context, channelID int64) (string, int, bool, error) {
	operation, err := gql.MustLookup(gql.OperationCurrentDrop).WithVariables(map[string]any{
		"channelID": strconv.FormatInt(channelID, 10),
	})
	if err != nil {
		return "", 0, false, fmt.Errorf("构造 CurrentDrop 请求失败: %w", err)
	}

	response, err := s.gqlClient.Do(ctx, operation)
	if err != nil {
		return "", 0, false, err
	}

	data, err := asMap(response.Data, "data")
	if err != nil {
		return "", 0, false, err
	}
	currentUser := optionalMap(data["currentUser"])
	if len(currentUser) == 0 {
		return "", 0, false, nil
	}
	session := optionalMap(currentUser["dropCurrentSession"])
	if len(session) == 0 {
		return "", 0, false, nil
	}

	dropID := stringValue(session, "dropID")
	if dropID == "" {
		return "", 0, false, nil
	}

	return dropID, int(int64Value(session, "currentMinutesWatched")), true, nil
}

func (s *Scheduler) shouldResolveProgress(sentAt time.Time) bool {
	if s == nil {
		return false
	}

	s.mu.RLock()
	lastProgressAt := s.lastProgressAt
	s.mu.RUnlock()

	return lastProgressAt.IsZero() || lastProgressAt.Before(sentAt)
}

func (s *Scheduler) waitForWatchingChannel(ctx context.Context) (int64, bool) {
	for {
		s.mu.RLock()
		watchingChannelID := s.watchingChannelID
		s.mu.RUnlock()
		if watchingChannelID != 0 {
			return watchingChannelID, true
		}

		select {
		case <-ctx.Done():
			return 0, false
		case <-s.watchSignal:
		}
	}
}

func (s *Scheduler) sleepWithWatchSignal(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}

	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.watchSignal:
		return errWatchInterrupted
	case <-timer.C:
		return nil
	}
}

func (s *Scheduler) signalWatch() {
	select {
	case s.watchSignal <- struct{}{}:
	default:
	}
}
