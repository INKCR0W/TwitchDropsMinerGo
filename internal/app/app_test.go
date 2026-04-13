package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
)

func TestRunWaitsForCancellation(t *testing.T) {
	t.Parallel()

	store := &memoryStateStore{}
	application, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: store,
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run(ctx)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("Run 在取消前提前退出: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run 返回错误: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run 在取消后未及时退出")
	}

	if len(store.savedStates) != 2 {
		t.Fatalf("期望保存 2 次状态，实际为 %d", len(store.savedStates))
	}
}

func TestRunRejectsNilContext(t *testing.T) {
	t.Parallel()

	application, err := New(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	var nilCtx context.Context
	if err := application.Run(nilCtx); err == nil {
		t.Fatal("期望 nil context 返回错误")
	}
}

func TestRunPersistsLifecycleState(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 4, 11, 1, 0, 0, 0, time.UTC)
	stop := start.Add(2 * time.Minute)
	nowValues := []time.Time{start, stop}
	store := &memoryStateStore{
		state: RuntimeState{
			SchemaVersion: 1,
			RunCount:      4,
		},
	}

	application, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: store,
		Now: func() time.Time {
			value := nowValues[0]
			nowValues = nowValues[1:]
			return value
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run(ctx)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case runErr := <-errCh:
		if runErr != nil {
			t.Fatalf("Run 返回错误: %v", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Run 在取消后未及时退出")
	}

	if store.state.RunCount != 5 {
		t.Fatalf("期望 run_count 为 5，实际为 %d", store.state.RunCount)
	}
	if store.state.SchemaVersion != stateSchemaVersion {
		t.Fatalf("SchemaVersion 不匹配: %d", store.state.SchemaVersion)
	}
	if store.state.Running {
		t.Fatal("停止后 running 应为 false")
	}
	if !store.state.Healthy {
		t.Fatal("正常停止后 healthy 应保持为 true")
	}

	if !store.state.LastStartedAt.Equal(start) {
		t.Fatalf("LastStartedAt 不匹配: %v", store.state.LastStartedAt)
	}

	if !store.state.LastStoppedAt.Equal(stop) {
		t.Fatalf("LastStoppedAt 不匹配: %v", store.state.LastStoppedAt)
	}
}

func TestNewFailsWhenStateLoadFails(t *testing.T) {
	t.Parallel()

	_, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: &memoryStateStore{loadErr: errors.New("boom")},
	})
	if err == nil {
		t.Fatal("期望状态装载失败时返回错误")
	}
}

func TestNewLoadsSettingsFromStore(t *testing.T) {
	t.Parallel()

	application, err := New(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Settings: &memorySettingsStore{
			settings: config.Settings{
				Language: "简体中文",
			},
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if got := application.Settings().Language; got != "简体中文" {
		t.Fatalf("Settings 未装载外部配置: %q", got)
	}
}

func TestUpdateSettingsPersistsAndClonesValues(t *testing.T) {
	t.Parallel()

	stateStore := &memoryStateStore{}
	store := &memorySettingsStore{}
	application, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: stateStore,
		Settings:   store,
		Now: func() time.Time {
			return time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Priority = []string{"A"}
	settings.Proxy = "http://user:pass@proxy.example.com:8080"

	if err := application.UpdateSettings(settings); err != nil {
		t.Fatalf("UpdateSettings 返回错误: %v", err)
	}

	settings.Priority[0] = "B"

	current := application.Settings()
	if current.Priority[0] != "A" {
		t.Fatalf("App 持有的 Priority 不应受外部切片影响: %#v", current.Priority)
	}
	if store.settings.Priority[0] != "A" {
		t.Fatalf("Store 持有的 Priority 不应受外部切片影响: %#v", store.settings.Priority)
	}
	if stateStore.state.Settings.Proxy != "http://proxy.example.com:8080" {
		t.Fatalf("状态快照中的代理应脱敏，实际为 %q", stateStore.state.Settings.Proxy)
	}
}

func TestUpdateObservationPersistsAndClonesSnapshot(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2026, 4, 11, 11, 0, 0, 0, time.UTC)
	stateStore := &memoryStateStore{}
	application, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: stateStore,
		Now: func() time.Time {
			return stamp
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	observation := Observation{
		Healthy: true,
		Auth: AuthStatus{
			LoggedIn: true,
			UserID:   42,
		},
		Schedule: ScheduleStatus{
			State:             "CHANNEL_SWITCH",
			WantedGames:       []GameStatus{{ID: 1, Name: "Game", Slug: "game"}},
			WatchingChannelID: 9,
			ChannelCount:      1,
			Channels: []ChannelStatus{
				{
					ID:    9,
					Login: "watching",
					Stream: &StreamStatus{
						Title: "Live",
						Game:  &GameStatus{ID: 1, Name: "Game", Slug: "game"},
					},
				},
			},
			ActiveCampaign: &CampaignStatus{
				ID:               "campaign",
				Name:             "Campaign",
				RemainingMinutes: 30,
			},
			ActiveDrop: &DropStatus{
				ID:              "drop",
				Name:            "Drop",
				CurrentMinutes:  10,
				RequiredMinutes: 30,
			},
		},
	}
	observation.Settings = config.DefaultSettings()
	observation.Settings.Language = "简体中文"
	observation.Settings.Proxy = "http://user:pass@proxy.example.com:8080"

	if err := application.UpdateObservation(observation); err != nil {
		t.Fatalf("UpdateObservation 返回错误: %v", err)
	}

	observation.Schedule.Channels[0].Login = "mutated"
	observation.Schedule.WantedGames[0].Name = "Mutated"

	state := application.RuntimeState()
	if state.Auth.UserID != 42 {
		t.Fatalf("认证快照未落盘: %#v", state.Auth)
	}
	if state.Schedule.Channels[0].Login != "watching" {
		t.Fatalf("频道快照应已克隆，实际为 %#v", state.Schedule.Channels)
	}
	if state.Schedule.WantedGames[0].Name != "Game" {
		t.Fatalf("游戏快照应已克隆，实际为 %#v", state.Schedule.WantedGames)
	}
	if state.Settings.Proxy != "http://proxy.example.com:8080" {
		t.Fatalf("状态快照中的代理应脱敏，实际为 %q", state.Settings.Proxy)
	}
	if !state.UpdatedAt.Equal(stamp) {
		t.Fatalf("UpdatedAt 不匹配: %v", state.UpdatedAt)
	}
}

func TestRecordFailurePersistsLastError(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	stateStore := &memoryStateStore{}
	application, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: stateStore,
		Now: func() time.Time {
			return stamp
		},
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	if err := application.RecordFailure(errors.New("boom")); err != nil {
		t.Fatalf("RecordFailure 返回错误: %v", err)
	}

	state := application.RuntimeState()
	if state.Healthy {
		t.Fatal("失败后 healthy 应为 false")
	}
	if state.LastError != "boom" {
		t.Fatalf("LastError 不匹配: %q", state.LastError)
	}
	if state.Running {
		t.Fatal("失败后 running 应为 false")
	}
	if !state.UpdatedAt.Equal(stamp) {
		t.Fatalf("UpdatedAt 不匹配: %v", state.UpdatedAt)
	}
}

func TestStateSavesAreSerialized(t *testing.T) {
	t.Parallel()

	store := &concurrencyCheckingStateStore{}
	application, err := New(Options{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		StateStore: store,
	})
	if err != nil {
		t.Fatalf("New 返回错误: %v", err)
	}

	observation := Observation{
		Healthy: true,
	}
	observation.Settings = config.DefaultSettings()

	errCh := make(chan error, 2)
	start := make(chan struct{})
	go func() {
		<-start
		errCh <- application.UpdateObservation(observation)
	}()
	go func() {
		<-start
		errCh <- application.RecordFailure(errors.New("boom"))
	}()
	close(start)

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("并发保存返回错误: %v", err)
		}
	}
	if store.concurrent {
		t.Fatal("状态写盘应串行执行")
	}
}

type memoryStateStore struct {
	state       RuntimeState
	loadErr     error
	saveErr     error
	savedStates []RuntimeState
}

func (m *memoryStateStore) Load() (RuntimeState, error) {
	if m.loadErr != nil {
		return RuntimeState{}, m.loadErr
	}

	if m.state.SchemaVersion == 0 {
		m.state.SchemaVersion = stateSchemaVersion
	}

	return m.state.clone(), nil
}

func (m *memoryStateStore) Save(state RuntimeState) error {
	if m.saveErr != nil {
		return m.saveErr
	}

	m.state = state.clone()
	m.savedStates = append(m.savedStates, state.clone())
	return nil
}

type memorySettingsStore struct {
	settings config.Settings
	loadErr  error
	saveErr  error
}

func (m *memorySettingsStore) Load() (config.Settings, error) {
	if m.loadErr != nil {
		return config.Settings{}, m.loadErr
	}
	if m.settings.IsZero() {
		m.settings = config.DefaultSettings()
	}
	return m.settings.Clone(), nil
}

func (m *memorySettingsStore) Save(settings config.Settings) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.settings = settings.Clone()
	return nil
}

type concurrencyCheckingStateStore struct {
	mu         sync.Mutex
	active     int
	concurrent bool
	state      RuntimeState
}

func (s *concurrencyCheckingStateStore) Load() (RuntimeState, error) {
	return DefaultRuntimeState(), nil
}

func (s *concurrencyCheckingStateStore) Save(state RuntimeState) error {
	s.mu.Lock()
	s.active++
	if s.active > 1 {
		s.concurrent = true
	}
	s.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	s.mu.Lock()
	s.state = state.clone()
	s.active--
	s.mu.Unlock()
	return nil
}
