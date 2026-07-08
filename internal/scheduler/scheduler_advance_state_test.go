package scheduler

import (
	"io"
	"log/slog"
	"testing"
)

func newStateTestScheduler(state State) *Scheduler {
	return &Scheduler{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		stateChanged: make(chan struct{}, 1),
		state:        state,
	}
}

func TestAdvanceStateAdvancesWhenStateMatches(t *testing.T) {
	t.Parallel()

	s := newStateTestScheduler(StateChannelsFetch)
	s.advanceState(StateChannelsFetch, StateChannelSwitch)
	if got := s.State(); got != StateChannelSwitch {
		t.Fatalf("常规推进失败: %s", got)
	}
}

func TestAdvanceStateDoesNotClobberEventTransition(t *testing.T) {
	t.Parallel()

	s := newStateTestScheduler(StateChannelsFetch)

	// 模拟 handler 执行期间，事件 goroutine 把状态改成 InventoryFetch（如领取掉宝/奖励通知）
	s.mu.Lock()
	s.state = StateInventoryFetch
	s.mu.Unlock()

	// handler 结束时的常规推进不应覆盖事件请求的 InventoryFetch
	s.advanceState(StateChannelsFetch, StateChannelSwitch)
	if got := s.State(); got != StateInventoryFetch {
		t.Fatalf("advanceState 覆盖了事件请求的状态，实际 %s", got)
	}
}

func TestAdvanceStateDoesNotResurrectExitState(t *testing.T) {
	t.Parallel()

	s := newStateTestScheduler(StateExit)
	s.advanceState(StateChannelsFetch, StateChannelSwitch)
	if got := s.State(); got != StateExit {
		t.Fatalf("Exit 状态应保持终态，实际 %s", got)
	}
}
