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

const progressPushTimeout = 120 * time.Second

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
		now := s.nowUTC()
		s.lastAdvanceAt = now
		s.lastProgressAt = now
		s.progressPushDeadline = now.Add(progressPushTimeout)
		s.lastProgressPollAt = time.Time{}
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
	s.progressPushDeadline = time.Time{}
	s.lastProgressPollAt = time.Time{}
	s.lastAdvanceAt = time.Time{}
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

		delay := max(s.progressDelay-s.nowUTC().Sub(sentAt), 0)
		if err := s.sleepWithWatchSignal(ctx, delay); err != nil {
			if errors.Is(err, errWatchInterrupted) {
				continue
			}
			return
		}

		stateBefore := s.State()
		s.resolveProgress(ctx, channel, watchReported)
		if s.State() == stateBefore {
			s.checkWatchStall(channelID, watchReported)
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
	// 统一用进入本轮的时刻打进度戳, 否则 GQL 往返耗时会吃掉下一轮兜底的余量
	now := s.nowUTC()

	if s.beginProgressPoll(now) && s.syncProgressFromGQL(ctx, now, channel) {
		s.refreshWhenChannelExhausted(now, channel)
		return
	}

	if !watchReported || !s.shouldEstimateProgress() {
		return
	}

	completedReward, reachedLimit, updated := s.bumpActiveCampaign(now, &channel)
	if !updated {
		s.refreshWhenChannelExhausted(now, channel)
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
func (s *Scheduler) refreshWhenChannelExhausted(now time.Time, channel domain.Channel) {
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

func (s *Scheduler) syncProgressFromGQL(ctx context.Context, now time.Time, channel domain.Channel) bool {
	dropID, currentMinutes, ok, err := s.fetchCurrentDrop(ctx, channel.ID)
	if err != nil {
		s.logger.Warn("查询 CurrentDrop 失败，回退到本地估算", "channel_id", channel.ID, "error", err)
		return false
	}
	if !ok {
		return false
	}

	if !s.applyDropProgress(now, &channel, dropID, currentMinutes) {
		return false
	}

	attrs := []any{"drop_id", dropID, "current_minutes", currentMinutes}
	attrs = append(attrs, s.watchProgressAttrs(dropID)...)
	s.logger.Info("轮询到掉宝进度", attrs...)
	return true
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

// 推送超时或即将判定频道卡住时查询进度，查询失败同样遵守一分钟间隔
func (s *Scheduler) beginProgressPoll(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	stallTimeout := time.Duration(s.settings.WatchStallMinutes) * time.Minute
	stallDue := stallTimeout > 0 && !s.lastAdvanceAt.IsZero() && now.Sub(s.lastAdvanceAt) >= stallTimeout
	if !stallDue && now.Before(s.progressPushDeadline) {
		return false
	}
	if !s.lastProgressPollAt.IsZero() && now.Sub(s.lastProgressPollAt) < time.Minute {
		return false
	}
	s.lastProgressPollAt = now
	return true
}

// 本地补分钟独立于推送等待和轮询间隔
func (s *Scheduler) shouldEstimateProgress() bool {
	if s == nil {
		return false
	}

	s.mu.RLock()
	lastProgressAt := s.lastProgressAt
	s.mu.RUnlock()

	return lastProgressAt.IsZero() || s.nowUTC().Sub(lastProgressAt) >= time.Minute
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
