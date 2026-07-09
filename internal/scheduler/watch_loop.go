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
	if s.syncProgressFromGQL(ctx, channel) {
		return
	}

	if !watchReported {
		return
	}

	now := s.nowUTC()

	completedReward, reachedLimit, updated := s.bumpActiveCampaign(now, &channel)
	if !updated {
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

func (s *Scheduler) syncProgressFromGQL(ctx context.Context, channel domain.Channel) bool {
	dropID, currentMinutes, ok, err := s.fetchCurrentDrop(ctx, channel.ID)
	if err != nil {
		s.logger.Warn("查询 CurrentDrop 失败，回退到本地估算", "channel_id", channel.ID, "error", err)
		return false
	}
	if !ok {
		return false
	}

	return s.applyDropProgress(s.nowUTC(), &channel, dropID, currentMinutes)
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
