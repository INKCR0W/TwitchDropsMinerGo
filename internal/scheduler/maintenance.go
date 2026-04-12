package scheduler

import (
	"context"
	"sort"
	"time"
)

func (s *Scheduler) restartMaintenance(ctx context.Context, triggers []time.Time) {
	s.cancelMaintenance()

	maintenanceCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.maintenanceCancel = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	go s.maintenanceLoop(maintenanceCtx, append([]time.Time(nil), triggers...))
}

func (s *Scheduler) maintenanceLoop(ctx context.Context, triggers []time.Time) {
	defer s.wg.Done()

	sort.Slice(triggers, func(i, j int) bool {
		return triggers[i].Before(triggers[j])
	})

	nextReload := s.nowUTC().Add(s.maintenanceReload)
	index := 0
	for {
		now := s.nowUTC()
		if !now.Before(nextReload) {
			break
		}

		nextTrigger := nextReload
		for index < len(triggers) && !triggers[index].After(now) {
			index++
		}
		if index < len(triggers) && triggers[index].Before(nextTrigger) {
			nextTrigger = triggers[index]
			index++
		}

		if err := s.sleep(ctx, nextTrigger.Sub(now)); err != nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if nextTrigger.Equal(nextReload) {
			break
		}
		s.ChangeState(StateChannelsCleanup)
	}

	s.ChangeState(StateInventoryFetch)
}

func (s *Scheduler) cancelMaintenance() {
	s.mu.Lock()
	cancel := s.maintenanceCancel
	s.maintenanceCancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}
