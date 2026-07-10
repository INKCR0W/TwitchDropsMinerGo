package scheduler

import "time"

const watchStallCooldown = 30 * time.Minute

func (s *Scheduler) channelStalled(channelID int64, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	until, ok := s.stalledChannels[channelID]
	return ok && now.Before(until)
}

func (s *Scheduler) checkWatchStall(channelID int64) {
	timeout := time.Duration(s.settingsCopy().WatchStallMinutes) * time.Minute
	if timeout <= 0 {
		return
	}
	now := s.nowUTC()

	s.mu.Lock()
	for id, until := range s.stalledChannels {
		if !now.Before(until) {
			delete(s.stalledChannels, id)
		}
	}
	stalled := channelID != 0 &&
		s.watchingChannelID == channelID &&
		!s.lastAdvanceAt.IsZero() &&
		now.Sub(s.lastAdvanceAt) >= timeout
	if stalled {
		s.stalledChannels[channelID] = now.Add(watchStallCooldown)
		s.lastAdvanceAt = now
	}
	s.mu.Unlock()

	if !stalled {
		return
	}
	s.logger.Info(
		"频道长时间无进度, 回避并切换",
		"channel_id", channelID,
		"stall_minutes", int(timeout.Minutes()),
		"cooldown_minutes", int(watchStallCooldown.Minutes()),
	)
	s.ChangeState(StateChannelSwitch)
}
