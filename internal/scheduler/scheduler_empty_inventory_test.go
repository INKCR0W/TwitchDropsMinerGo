package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/inventory"
)

func TestRunRetriesAfterEmptySuccessfulInventory(t *testing.T) {
	for _, initiallyNonempty := range []bool{false, true} {
		name := "empty-first"
		if initiallyNonempty {
			name = "nonempty-then-empty"
		}
		t.Run(name, func(t *testing.T) {
			now := testTime()
			var calls, retries atomic.Int32
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			const retryDelay = 7 * time.Millisecond
			finalCall := int32(3)
			if initiallyNonempty {
				finalCall++
			}
			refresher := &fakeRefresher{refreshFunc: func(context.Context, inventory.RefreshOptions) (inventory.Snapshot, error) {
				call := calls.Add(1)
				if initiallyNonempty && call == 1 {
					campaign := mustCampaign(t, campaignSpec(now, "completed", domain.Game{ID: 1, Name: "Game"}, now.Add(-time.Hour), now.Add(time.Hour), nil))
					campaign.Drop("completed-drop").MarkClaimed()
					return snapshotFromCampaigns(campaign), nil
				}
				if call == finalCall-1 {
					return inventory.Snapshot{}, errors.New("temporary outage")
				}
				return snapshotFromCampaigns(), nil
			}}
			scheduler := newTestScheduler(t, testSchedulerOptions{
				refresher:       refresher,
				errorRetryDelay: retryDelay,
				sleep: func(ctx context.Context, delay time.Duration) error {
					if delay == retryDelay {
						retries.Add(1)
						return nil
					}
					<-ctx.Done()
					return ctx.Err()
				},
			})
			reloadDone := make(chan struct{})
			go func() {
				defer close(reloadDone)
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				var lastReload int32
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						call := calls.Load()
						if scheduler.State() != StateIdle {
							continue
						}
						if call == finalCall {
							cancel()
							return
						}
						if call > lastReload {
							lastReload = call
							scheduler.Reload()
						}
					}
				}
			}()
			err := scheduler.Run(ctx)
			cancel()
			<-reloadDone
			if err != nil {
				t.Fatalf("成功装载空库存后应重试恢复: %v", err)
			}
			if calls.Load() != finalCall || retries.Load() != 1 {
				t.Fatalf("未恢复: calls=%d retries=%d", calls.Load(), retries.Load())
			}
		})
	}
}
