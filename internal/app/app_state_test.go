package app

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"twitchdropsminergo/internal/config"
)

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
