package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/storage"
)

const stateSchemaVersion = 2

type StateStore interface {
	Load() (RuntimeState, error)
	Save(RuntimeState) error
}

type RuntimeState struct {
	SchemaVersion int             `json:"schema_version"`
	RunCount      int             `json:"run_count"`
	Running       bool            `json:"running"`
	Healthy       bool            `json:"healthy"`
	LastError     string          `json:"last_error,omitempty"`
	LastStartedAt time.Time       `json:"last_started_at,omitempty"`
	LastStoppedAt time.Time       `json:"last_stopped_at,omitempty"`
	UpdatedAt     time.Time       `json:"updated_at,omitempty"`
	HeartbeatAt   time.Time       `json:"heartbeat_at,omitempty"`
	Auth          AuthStatus      `json:"auth"`
	Schedule      ScheduleStatus  `json:"schedule"`
	Settings      config.Settings `json:"settings"`
}

type Options struct {
	Logger     *slog.Logger
	StateStore StateStore
	Settings   config.Store
	Now        func() time.Time
}

type App struct {
	logger     *slog.Logger
	stateStore StateStore
	settings   config.Store
	mu         sync.RWMutex
	saveMu     sync.Mutex
	state      RuntimeState
	current    config.Settings
	now        func() time.Time
}

func DefaultRuntimeState() RuntimeState {
	return RuntimeState{
		SchemaVersion: stateSchemaVersion,
		Settings:      config.DefaultSettings().Sanitized(),
	}
}

func New(options Options) (*App, error) {
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	if options.Now == nil {
		options.Now = time.Now
	}

	state := DefaultRuntimeState()
	if options.StateStore != nil {
		loadedState, err := options.StateStore.Load()
		if err != nil {
			if !errors.Is(err, storage.ErrCorrupt) {
				return nil, fmt.Errorf("加载运行时状态失败: %w", err)
			}
			options.Logger.Warn("运行时状态文件损坏，使用默认状态继续", "error", err)
			loadedState = DefaultRuntimeState()
		}

		if loadedState.SchemaVersion < stateSchemaVersion {
			loadedState.SchemaVersion = stateSchemaVersion
		}
		state = loadedState
	}

	settings := config.DefaultSettings()
	if options.Settings != nil {
		loadedSettings, err := options.Settings.Load()
		if err != nil {
			return nil, fmt.Errorf("加载运行配置失败: %w", err)
		}
		settings = loadedSettings.Clone()
	}
	state.Settings = settings.Sanitized()

	return &App{
		logger:     options.Logger,
		stateStore: options.StateStore,
		settings:   options.Settings,
		state:      state,
		current:    settings,
		now:        options.Now,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("运行上下文不能为空")
	}

	if err := a.markStarted(); err != nil {
		a.logger.Warn("保存启动状态失败，继续运行", "error", err)
	}

	state := a.RuntimeState()
	a.logger.Info("服务启动", "run_count", state.RunCount)

	<-ctx.Done()

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	if err := a.markStopped(); err != nil {
		a.logger.Warn("保存停止状态失败", "error", err)
	}

	state = a.RuntimeState()
	a.logger.Info("服务停止", "run_count", state.RunCount)
	return nil
}

func (a *App) markStarted() error {
	a.mu.Lock()
	stamp := a.now().UTC()
	a.state.SchemaVersion = stateSchemaVersion
	a.state.RunCount++
	a.state.Running = true
	a.state.Healthy = true
	a.state.LastError = ""
	a.state.LastStartedAt = stamp
	a.state.UpdatedAt = stamp
	a.state.Settings = a.current.Sanitized()
	state := a.state.clone()
	a.mu.Unlock()

	return a.saveState(state, "保存启动状态失败")
}

func (a *App) markStopped() error {
	a.mu.Lock()
	stamp := a.now().UTC()
	a.state.Running = false
	if strings.TrimSpace(a.state.LastError) == "" {
		a.state.Healthy = true
	}
	a.state.LastStoppedAt = stamp
	a.state.UpdatedAt = stamp
	state := a.state.clone()
	a.mu.Unlock()

	return a.saveState(state, "保存停止状态失败")
}

func (a *App) RuntimeState() RuntimeState {
	if a == nil {
		return DefaultRuntimeState()
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state.clone()
}

func (a *App) Settings() config.Settings {
	if a == nil {
		return config.DefaultSettings()
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.current.Clone()
}

func (a *App) UpdateSettings(settings config.Settings) error {
	if a == nil {
		return fmt.Errorf("应用未初始化")
	}
	if err := settings.Validate(); err != nil {
		return err
	}

	cloned := settings.Clone()
	if a.settings != nil {
		if err := a.settings.Save(cloned); err != nil {
			return fmt.Errorf("保存运行配置失败: %w", err)
		}
	}

	var (
		stateToSave RuntimeState
		shouldSave  bool
	)
	a.mu.Lock()
	a.current = cloned
	next := a.state.clone()
	next.SchemaVersion = stateSchemaVersion
	next.Settings = cloned.Sanitized()
	if !runtimeStateEqual(a.state, next) {
		next.UpdatedAt = a.now().UTC()
		shouldSave = true
	}
	a.state = next
	stateToSave = next.clone()
	a.mu.Unlock()

	if !shouldSave {
		return nil
	}
	return a.saveState(stateToSave, "保存运行时配置快照失败")
}

func (a *App) UpdateObservation(observation Observation) error {
	if a == nil {
		return fmt.Errorf("应用未初始化")
	}

	normalized := observation.normalized(a.Settings())
	var (
		stateToSave RuntimeState
		shouldSave  bool
	)

	a.mu.Lock()
	next := a.state.clone()
	next.SchemaVersion = stateSchemaVersion
	next.Healthy = normalized.Healthy
	next.LastError = strings.TrimSpace(normalized.LastError)
	next.HeartbeatAt = normalized.Heartbeat
	next.Auth = normalized.Auth
	next.Schedule = normalized.Schedule
	next.Settings = normalized.Settings
	if !runtimeStateEqual(a.state, next) {
		next.UpdatedAt = a.now().UTC()
		shouldSave = true
	}
	a.state = next
	stateToSave = next.clone()
	a.mu.Unlock()

	if !shouldSave {
		return nil
	}
	return a.saveState(stateToSave, "保存运行时观察快照失败")
}

func (a *App) RecordFailure(err error) error {
	if a == nil {
		return fmt.Errorf("应用未初始化")
	}
	if err == nil {
		return nil
	}

	a.mu.Lock()
	a.state.SchemaVersion = stateSchemaVersion
	a.state.Running = false
	a.state.Healthy = false
	a.state.LastError = strings.TrimSpace(err.Error())
	a.state.UpdatedAt = a.now().UTC()
	a.state.Settings = a.current.Sanitized()
	state := a.state.clone()
	a.mu.Unlock()

	return a.saveState(state, "保存失败状态失败")
}

func (a *App) saveState(state RuntimeState, action string) error {
	if a.stateStore == nil {
		return nil
	}
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	if err := a.stateStore.Save(state); err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}

func runtimeStateEqual(current RuntimeState, next RuntimeState) bool {
	current.UpdatedAt = time.Time{}
	next.UpdatedAt = time.Time{}
	return reflect.DeepEqual(current, next)
}
