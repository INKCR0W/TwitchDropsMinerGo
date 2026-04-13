package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"twitchdropsminergo/internal/app"
	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/pubsub"
	"twitchdropsminergo/internal/scheduler"
)

func TestParseArgsAppliesDefaults(t *testing.T) {
	t.Parallel()

	options, err := parseArgs(nil)
	if err != nil {
		t.Fatalf("parseArgs 返回错误: %v", err)
	}
	if options.RuntimeDir == "" {
		t.Fatalf("默认参数不匹配: %#v", options)
	}
}

func TestBuildRuntimeObservationMapsSnapshots(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 11, 9, 0, 0, 0, time.UTC)
	settings := config.DefaultSettings()
	settings.Language = "简体中文"
	settings.Proxy = "http://user:pass@proxy.example.com:8080"

	campaign, err := domain.NewCampaign(domain.CampaignSpec{
		ID:       "campaign-1",
		Name:     "Campaign",
		Game:     domain.Game{ID: 1, Name: "Game", SlugText: "game"},
		Linked:   true,
		StartsAt: now.Add(-time.Hour),
		EndsAt:   now.Add(2 * time.Hour),
		Status:   "ACTIVE",
		Drops: []domain.TimedDropSpec{
			{
				ID:                 "drop-1",
				Name:               "Drop",
				Benefits:           []domain.Benefit{{ID: "benefit-1", Name: "Reward", Type: domain.BenefitTypeDirectEntitlement}},
				StartsAt:           now.Add(-time.Hour),
				EndsAt:             now.Add(time.Hour),
				ClaimID:            "claim-1",
				RealCurrentMinutes: 10,
				RequiredMinutes:    30,
			},
		},
	})
	if err != nil {
		t.Fatalf("NewCampaign 返回错误: %v", err)
	}

	observation := buildRuntimeObservation(settings, auth.Snapshot{UserID: 42}, scheduler.StatusSnapshot{
		State:             scheduler.StateChannelSwitch,
		WantedGames:       []domain.Game{{ID: 1, Name: "Game", SlugText: "game"}},
		WatchingChannelID: 9,
		Channels: []domain.Channel{
			{
				ID:          9,
				Login:       "watching",
				DisplayName: "Watching",
				Stream: &domain.Stream{
					BroadcastID:  99,
					Game:         &domain.Game{ID: 1, Name: "Game", SlugText: "game"},
					Viewers:      120,
					Title:        "Live",
					DropsEnabled: true,
				},
			},
		},
		PubSub: pubsub.Status{
			Running:    true,
			Endpoint:   "wss://pubsub.example.com",
			TopicCount: 2,
			Shards: []pubsub.ShardStatus{
				{
					Index:          0,
					State:          pubsub.ShardStateConnected,
					Connected:      true,
					TopicCount:     2,
					SubmittedCount: 2,
				},
			},
		},
	}, campaign, now)

	if !observation.Auth.LoggedIn || observation.Auth.UserID != 42 {
		t.Fatalf("认证状态映射错误: %#v", observation.Auth)
	}
	if observation.Schedule.State != string(scheduler.StateChannelSwitch) {
		t.Fatalf("调度状态映射错误: %#v", observation.Schedule)
	}
	if observation.Schedule.ChannelCount != 1 || observation.Schedule.Channels[0].Login != "watching" {
		t.Fatalf("频道状态映射错误: %#v", observation.Schedule.Channels)
	}
	if observation.Schedule.PubSub.TopicCount != 2 || !observation.Schedule.PubSub.Running {
		t.Fatalf("PubSub 状态映射错误: %#v", observation.Schedule.PubSub)
	}
	if observation.Schedule.ActiveCampaign == nil || observation.Schedule.ActiveCampaign.ID != "campaign-1" {
		t.Fatalf("活动快照映射错误: %#v", observation.Schedule.ActiveCampaign)
	}
	if observation.Schedule.ActiveDrop == nil || observation.Schedule.ActiveDrop.ID != "drop-1" || !observation.Schedule.ActiveDrop.Claimable {
		t.Fatalf("掉宝快照映射错误: %#v", observation.Schedule.ActiveDrop)
	}
	if observation.Settings.Proxy != "http://proxy.example.com:8080" {
		t.Fatalf("设置应已脱敏: %#v", observation.Settings)
	}
}

func TestLocalStateSyncPersistsObservation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	settings := config.DefaultSettings()
	settings.Language = "简体中文"
	settings.Proxy = "http://user:pass@proxy.example.com:8080"
	target := &stubLocalStateTarget{
		settings: settings,
	}
	syncer := &localStateSync{
		application: target,
		auth:        stubAuthSnapshotProvider{snapshot: auth.Snapshot{UserID: 77}},
		scheduler: stubSchedulerStateProvider{
			snapshot: scheduler.StatusSnapshot{
				State:    scheduler.StateIdle,
				Channels: []domain.Channel{{ID: 9, Login: "watching"}},
			},
		},
		interval: time.Hour,
		now: func() time.Time {
			return now
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- syncer.Run(ctx)
	}()

	deadline := time.After(time.Second)
	for {
		target.mu.Lock()
		count := len(target.observations)
		target.mu.Unlock()
		if count > 0 {
			break
		}

		select {
		case <-deadline:
			t.Fatal("本地状态同步未写入观察快照")
		case <-time.After(10 * time.Millisecond):
		}
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run 返回错误: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("本地状态同步在取消后未退出")
	}

	target.mu.Lock()
	observation := target.observations[0]
	target.mu.Unlock()

	if observation.Auth.UserID != 77 {
		t.Fatalf("认证快照未写入: %#v", observation.Auth)
	}
	if observation.Settings.Proxy != "http://proxy.example.com:8080" {
		t.Fatalf("设置快照应已脱敏: %#v", observation.Settings)
	}
}

func TestRunServiceStopsOnCancellation(t *testing.T) {
	t.Parallel()

	stopped := make(chan struct{})
	service := stubRunner{
		run: func(ctx context.Context) error {
			<-ctx.Done()
			close(stopped)
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	if err := runServiceWithTimeout(ctx, cancel, 200*time.Millisecond,
		namedRunner{name: "应用状态持久化", runner: stubRunner{}},
		namedRunner{name: "调度服务", runner: service},
	); err != nil {
		t.Fatalf("runServiceWithTimeout 返回错误: %v", err)
	}

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("服务在取消后未及时退出")
	}
}

func TestRunServiceReturnsFirstWorkerError(t *testing.T) {
	t.Parallel()

	service := stubRunner{
		run: func(context.Context) error {
			return errors.New("worker failed")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := runServiceWithTimeout(ctx, cancel, 200*time.Millisecond,
		namedRunner{name: "应用状态持久化", runner: stubRunner{}},
		namedRunner{name: "调度服务", runner: service},
	)
	if err == nil || !strings.Contains(err.Error(), "worker failed") {
		t.Fatalf("期望返回首个 worker 错误，实际为 %v", err)
	}
}

func TestRunServiceTimesOutWhenWorkerIgnoresCancellation(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	service := stubRunner{
		run: func(ctx context.Context) error {
			<-ctx.Done()
			<-release
			return nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := runServiceWithTimeout(ctx, cancel, 50*time.Millisecond,
		namedRunner{name: "应用状态持久化", runner: stubRunner{}},
		namedRunner{name: "调度服务", runner: service},
	)
	close(release)
	if !errors.Is(err, errRuntimeShutdownTimeout) {
		t.Fatalf("期望返回退出超时错误，实际为 %v", err)
	}
}

type stubRunner struct {
	run func(context.Context) error
}

func (s stubRunner) Run(ctx context.Context) error {
	if s.run == nil {
		<-ctx.Done()
		return nil
	}
	return s.run(ctx)
}

type stubLocalStateTarget struct {
	settings     config.Settings
	mu           sync.Mutex
	observations []app.Observation
}

func (s *stubLocalStateTarget) Settings() config.Settings {
	if s == nil {
		return config.DefaultSettings()
	}
	settings := s.settings.Clone()
	if settings.IsZero() {
		settings = config.DefaultSettings()
	}
	return settings
}

func (s *stubLocalStateTarget) UpdateObservation(observation app.Observation) error {
	if s == nil {
		return errors.New("状态目标未初始化")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.observations = append(s.observations, observation)
	return nil
}

type stubAuthSnapshotProvider struct {
	snapshot auth.Snapshot
}

func (s stubAuthSnapshotProvider) Snapshot() auth.Snapshot {
	return s.snapshot
}

type stubSchedulerStateProvider struct {
	snapshot       scheduler.StatusSnapshot
	activeCampaign *domain.DropsCampaign
}

func (s stubSchedulerStateProvider) StatusSnapshot() scheduler.StatusSnapshot {
	return s.snapshot
}

func (s stubSchedulerStateProvider) ActiveCampaign(*domain.Channel) *domain.DropsCampaign {
	return s.activeCampaign
}
