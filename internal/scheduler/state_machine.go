package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *Scheduler) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("scheduler 未初始化")
	}
	if ctx == nil {
		return fmt.Errorf("scheduler 运行上下文不能为空")
	}

	watchCtx, cancelWatch := context.WithCancel(ctx)
	s.wg.Add(1)
	go s.watchLoop(watchCtx)

	defer func() {
		cancelWatch()
		s.cancelMaintenance()
		s.clearStateChange()
		s.signalWatch()
		s.wg.Wait()
		if err := s.pubsub.Stop(context.Background(), true); err != nil {
			s.logger.Warn("停止 PubSub 失败", "error", err)
		}
		if err := s.tracker.Close(context.Background()); err != nil {
			s.logger.Warn("关闭 watch 跟踪器失败", "error", err)
		}
	}()

	s.ChangeState(StateInventoryFetch)
	for {
		if err := ctx.Err(); err != nil {
			s.ChangeState(StateExit)
		}

		switch s.State() {
		case StateIdle:
			s.handleIdle()
		case StateInventoryFetch:
			if err := s.handleInventoryFetch(ctx); err != nil {
				if retryErr := s.handleRuntimeError(ctx, StateInventoryFetch, err); retryErr != nil {
					return retryErr
				}
			}
		case StateGamesUpdate:
			if err := s.handleGamesUpdate(ctx); err != nil {
				if retryErr := s.handleRuntimeError(ctx, StateGamesUpdate, err); retryErr != nil {
					return retryErr
				}
			}
		case StateChannelsCleanup:
			s.handleChannelsCleanup()
		case StateChannelsFetch:
			if err := s.handleChannelsFetch(ctx); err != nil {
				if retryErr := s.handleRuntimeError(ctx, StateChannelsFetch, err); retryErr != nil {
					return retryErr
				}
			}
		case StateChannelSwitch:
			s.handleChannelSwitch()
		case StateExit:
			return nil
		default:
			return fmt.Errorf("未知 scheduler 状态: %s", s.State())
		}

		if s.State() == StateExit {
			return nil
		}
		if err := s.waitForStateChange(ctx); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				s.ChangeState(StateExit)
				continue
			}
			return err
		}
	}
}

func (s *Scheduler) handleIdle() {
	s.stopWatching()
}

func (s *Scheduler) hasInventorySnapshot() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.snapshot.Inventory) > 0 || len(s.snapshot.Campaigns) > 0 || len(s.snapshot.Drops) > 0
}

func (s *Scheduler) handleRuntimeError(ctx context.Context, state State, err error) error {
	if err == nil {
		return nil
	}
	if !s.hasInventorySnapshot() && state == StateInventoryFetch {
		return err
	}

	s.logger.Warn(
		"调度步骤失败，保留当前状态并退避重试",
		"state", state,
		"error", err,
		"retry_delay", s.errorRetryDelay.String(),
	)
	s.mu.Lock()
	if s.lastRuntimeError == nil {
		s.runtimeErrorSince = s.now().UTC()
	}
	s.lastRuntimeError = err
	s.mu.Unlock()

	if sleepErr := s.sleep(ctx, s.errorRetryDelay); sleepErr != nil {
		return sleepErr
	}
	s.ChangeState(state)
	return nil
}

func (s *Scheduler) clearRuntimeError() {
	s.mu.Lock()
	s.lastRuntimeError = nil
	s.runtimeErrorSince = time.Time{}
	s.mu.Unlock()
}

func (s *Scheduler) waitForStateChange(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stateChanged:
		return nil
	}
}

func (s *Scheduler) clearStateChange() {
	for {
		select {
		case <-s.stateChanged:
		default:
			return
		}
	}
}

func (s *Scheduler) signalStateChange() {
	select {
	case s.stateChanged <- struct{}{}:
	default:
	}
}
