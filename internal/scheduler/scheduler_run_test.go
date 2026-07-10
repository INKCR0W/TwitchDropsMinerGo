package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
)

func TestRunRetriesInventoryRefreshErrorWhenSnapshotExists(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-retry", game, now.Add(-time.Hour), now.Add(time.Hour), nil))
	stopAfterRetry := errors.New("stop after retry")
	refreshErr := errors.New("temporary gql failure")
	var refreshCalls atomic.Int32

	refresher := &fakeRefresher{
		refreshFunc: func(ctx context.Context, options inventory.RefreshOptions) (inventory.Snapshot, error) {
			if refreshCalls.Add(1) == 1 {
				return snapshotFromCampaigns(campaign), nil
			}
			return inventory.Snapshot{}, refreshErr
		},
	}

	var runtimeRetrySleeps atomic.Int32
	const retryDelay = 5 * time.Millisecond
	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher: refresher,
		sleep: func(ctx context.Context, delay time.Duration) error {
			if delay == retryDelay {
				if runtimeRetrySleeps.Add(1) >= 2 {
					return stopAfterRetry
				}
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		},
		errorRetryDelay: retryDelay,
	})

	runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	reloadDone := make(chan struct{})
	go func() {
		defer close(reloadDone)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if refreshCalls.Load() == 1 && scheduler.State() == StateIdle {
					scheduler.Reload()
					return
				}
			}
		}
	}()

	err := scheduler.Run(runCtx)
	close(done)
	<-reloadDone
	if !errors.Is(err, stopAfterRetry) {
		t.Fatalf("测试应在第三次 refresh 停止，实际错误: %v", err)
	}
	if got := refreshCalls.Load(); got < 3 {
		t.Fatalf("运行中 refresh 错误后应继续重试，refreshCalls=%d", got)
	}
	if got := runtimeRetrySleeps.Load(); got != 2 {
		t.Fatalf("运行中 refresh 错误应退避重试，runtimeRetrySleeps=%d", got)
	}
}

func TestRunReturnsInventoryRefreshErrorWhenSnapshotMissing(t *testing.T) {
	t.Parallel()

	refreshErr := errors.New("temporary gql failure")
	refreshCalls := 0
	refresher := &fakeRefresher{
		refreshFunc: func(ctx context.Context, options inventory.RefreshOptions) (inventory.Snapshot, error) {
			refreshCalls++
			return inventory.Snapshot{}, refreshErr
		},
	}

	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher: refresher,
		sleep: func(ctx context.Context, delay time.Duration) error {
			t.Fatalf("首次启动无快照时不应退避重试，delay=%s", delay)
			return nil
		},
	})

	err := scheduler.Run(context.Background())
	if !errors.Is(err, refreshErr) {
		t.Fatalf("首次启动无快照时应返回 refresh 错误，实际错误: %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("首次启动无快照失败时不应重试，refreshCalls=%d", refreshCalls)
	}
}

func TestRunRetriesChannelFetchErrorWhenSnapshotExists(t *testing.T) {
	t.Parallel()

	now := testTime()
	game := domain.Game{ID: 1, Name: "Game", SlugText: "game"}
	campaign := mustCampaign(t, campaignSpec(now, "campaign-channel-fetch-retry", game, now.Add(-time.Hour), now.Add(2*time.Hour), nil))
	drop := campaign.Drop("campaign-channel-fetch-retry-drop")
	if drop == nil {
		t.Fatal("期望测试 campaign 包含 drop")
	} else {
		drop.ExtraCurrentMinutes = 1
	}
	channelFetchErr := errors.New("temporary directory failure")
	stopAfterRetry := errors.New("stop after retry")
	var directoryCalls atomic.Int32

	refresher := &fakeRefresher{
		refreshFunc: func(ctx context.Context, options inventory.RefreshOptions) (inventory.Snapshot, error) {
			return snapshotFromCampaigns(campaign), nil
		},
	}
	gqlClient := &fakeGQLClient{
		doFunc: func(ctx context.Context, operation gql.Operation) (gql.Response, error) {
			if operation.OperationName == "DropsPage_ClaimDropRewards" {
				return gql.Response{}, nil
			}
			directoryCalls.Add(1)
			return gql.Response{}, channelFetchErr
		},
	}

	var runtimeRetrySleeps atomic.Int32
	const channelRetryDelay = 5 * time.Millisecond
	scheduler := newTestScheduler(t, testSchedulerOptions{
		refresher: refresher,
		gqlClient: gqlClient,
		sleep: func(ctx context.Context, delay time.Duration) error {
			if delay == channelRetryDelay {
				if runtimeRetrySleeps.Add(1) >= 2 {
					return stopAfterRetry
				}
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		},
		settings: config.Settings{
			Priority: []string{game.Name},
		},
		errorRetryDelay: channelRetryDelay,
	})

	runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := scheduler.Run(runCtx)
	if !errors.Is(err, stopAfterRetry) {
		t.Fatalf("测试应在第二次频道拉取失败后停止，实际错误: %v", err)
	}
	if got := directoryCalls.Load(); got < 2 {
		t.Fatalf("频道拉取错误后应保留快照并继续重试，directoryCalls=%d", got)
	}
	if got := runtimeRetrySleeps.Load(); got != 2 {
		t.Fatalf("频道拉取错误应退避重试，runtimeRetrySleeps=%d", got)
	}
}
