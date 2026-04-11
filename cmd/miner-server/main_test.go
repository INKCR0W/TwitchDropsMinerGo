package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
	if options.RuntimeDir == "" || options.ListenAddr != defaultListenAddr {
		t.Fatalf("默认参数不匹配: %#v", options)
	}
}

func TestValidateHotUpdatableSettingsRejectsTransportChanges(t *testing.T) {
	t.Parallel()

	current := config.DefaultSettings()
	next := current.Clone()
	next.Proxy = "http://proxy.example.com:8080"

	if err := validateHotUpdatableSettings(current, next); err == nil {
		t.Fatal("Proxy 变更应被拒绝")
	}
}

func TestBuildStatusResponseMapsSnapshots(t *testing.T) {
	t.Parallel()

	application, err := app.New(app.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("app.New 返回错误: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Language = "简体中文"
	if err := application.UpdateSettings(settings); err != nil {
		t.Fatalf("UpdateSettings 返回错误: %v", err)
	}

	response := buildStatusResponse(application, auth.Snapshot{UserID: 42}, scheduler.StatusSnapshot{
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
	})

	if !response.Auth.LoggedIn || response.Auth.UserID != 42 {
		t.Fatalf("认证状态映射错误: %#v", response.Auth)
	}
	if response.Schedule.State != string(scheduler.StateChannelSwitch) {
		t.Fatalf("调度状态映射错误: %#v", response.Schedule)
	}
	if response.Schedule.ChannelCount != 1 || response.Schedule.Channels[0].Login != "watching" {
		t.Fatalf("频道状态映射错误: %#v", response.Schedule.Channels)
	}
	if response.Schedule.PubSub.TopicCount != 2 || !response.Schedule.PubSub.Running {
		t.Fatalf("PubSub 状态映射错误: %#v", response.Schedule.PubSub)
	}
	if response.Settings.Language != "简体中文" {
		t.Fatalf("设置映射错误: %#v", response.Settings)
	}
}

func TestApplyRuntimeSettingsRollsBackWhenUpdaterFails(t *testing.T) {
	t.Parallel()

	current := config.DefaultSettings()
	next := current.Clone()
	next.Language = "简体中文"

	application := &stubRuntimeSettingsController{
		current: current,
	}
	updater := &stubSettingsUpdater{
		err: errors.New("scheduler update failed"),
	}

	_, err := applyRuntimeSettings(application, updater, next, testLogger())
	if err == nil || !strings.Contains(err.Error(), "scheduler update failed") {
		t.Fatalf("期望返回热更新错误，实际为 %v", err)
	}
	if got := application.Settings().Language; got != current.Language {
		t.Fatalf("热更新失败后应回滚配置，实际 language=%q", got)
	}
	if len(application.saved) != 2 {
		t.Fatalf("期望先保存新配置再回滚，实际保存次数为 %d", len(application.saved))
	}
}

func TestApplyRuntimeSettingsSurfacesRollbackFailure(t *testing.T) {
	t.Parallel()

	current := config.DefaultSettings()
	next := current.Clone()
	next.Language = "简体中文"

	application := &stubRuntimeSettingsController{
		current:    current,
		updateErrs: []error{nil, errors.New("rollback failed")},
	}
	updater := &stubSettingsUpdater{
		err: errors.New("scheduler update failed"),
	}

	_, err := applyRuntimeSettings(application, updater, next, testLogger())
	if err == nil {
		t.Fatal("期望返回错误")
	}
	if !strings.Contains(err.Error(), "scheduler update failed") || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("错误应同时暴露热更新失败与回滚失败，实际为 %v", err)
	}
	if got := application.Settings().Language; got != next.Language {
		t.Fatalf("回滚失败后应保留失败前状态，实际 language=%q", got)
	}
}

func TestRunServiceStopsOnCancellation(t *testing.T) {
	t.Parallel()

	application := newTestApplication(t)
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

	if err := runServiceWithTimeout(ctx, cancel, application, service, newTestServer(), testLogger(), 200*time.Millisecond); err != nil {
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

	application := newTestApplication(t)
	service := stubRunner{
		run: func(context.Context) error {
			return errors.New("worker failed")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := runServiceWithTimeout(ctx, cancel, application, service, newTestServer(), testLogger(), 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "worker failed") {
		t.Fatalf("期望返回首个 worker 错误，实际为 %v", err)
	}
}

func TestRunServiceTimesOutWhenWorkerIgnoresCancellation(t *testing.T) {
	t.Parallel()

	application := newTestApplication(t)
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

	err := runServiceWithTimeout(ctx, cancel, application, service, newTestServer(), testLogger(), 50*time.Millisecond)
	close(release)
	if !errors.Is(err, errRuntimeShutdownTimeout) {
		t.Fatalf("期望返回退出超时错误，实际为 %v", err)
	}
}

func newTestApplication(t *testing.T) *app.App {
	t.Helper()

	application, err := app.New(app.Options{
		Logger: testLogger(),
	})
	if err != nil {
		t.Fatalf("app.New 返回错误: %v", err)
	}
	return application
}

func newTestServer() *http.Server {
	return &http.Server{
		Addr: "127.0.0.1:0",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		ReadHeaderTimeout: time.Second,
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type stubRunner struct {
	run func(context.Context) error
}

func (s stubRunner) Run(ctx context.Context) error {
	if s.run == nil {
		return nil
	}
	return s.run(ctx)
}

type stubRuntimeSettingsController struct {
	current    config.Settings
	updateErrs []error
	saved      []config.Settings
}

func (s *stubRuntimeSettingsController) Settings() config.Settings {
	if s == nil {
		return config.DefaultSettings()
	}
	if s.current.IsZero() {
		s.current = config.DefaultSettings()
	}
	return s.current.Clone()
}

func (s *stubRuntimeSettingsController) UpdateSettings(settings config.Settings) error {
	if s == nil {
		return errors.New("controller 未初始化")
	}

	if len(s.updateErrs) > 0 {
		err := s.updateErrs[0]
		s.updateErrs = s.updateErrs[1:]
		if err != nil {
			return err
		}
	}

	s.current = settings.Clone()
	s.saved = append(s.saved, settings.Clone())
	return nil
}

type stubSettingsUpdater struct {
	err      error
	received []config.Settings
}

func (s *stubSettingsUpdater) UpdateSettings(settings config.Settings) error {
	if s == nil {
		return errors.New("updater 未初始化")
	}

	s.received = append(s.received, settings.Clone())
	return s.err
}
