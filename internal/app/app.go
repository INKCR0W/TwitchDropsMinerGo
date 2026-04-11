package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"twitchdropsminergo/internal/config"
)

const stateSchemaVersion = 1

type StateStore interface {
	Load() (RuntimeState, error)
	Save(RuntimeState) error
}

type RuntimeState struct {
	SchemaVersion int       `json:"schema_version"`
	RunCount      int       `json:"run_count"`
	LastStartedAt time.Time `json:"last_started_at,omitempty"`
	LastStoppedAt time.Time `json:"last_stopped_at,omitempty"`
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
	state      RuntimeState
	current    config.Settings
	now        func() time.Time
}

func DefaultRuntimeState() RuntimeState {
	return RuntimeState{
		SchemaVersion: stateSchemaVersion,
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
			return nil, fmt.Errorf("加载运行时状态失败: %w", err)
		}

		if loadedState.SchemaVersion == 0 {
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
		return err
	}

	state := a.RuntimeState()
	a.logger.Info("服务启动", "run_count", state.RunCount)

	<-ctx.Done()

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	if err := a.markStopped(); err != nil {
		return err
	}

	state = a.RuntimeState()
	a.logger.Info("服务停止", "run_count", state.RunCount)
	return nil
}

func (a *App) markStarted() error {
	if a.stateStore == nil {
		return nil
	}

	a.mu.Lock()
	a.state.SchemaVersion = stateSchemaVersion
	a.state.RunCount++
	a.state.LastStartedAt = a.now().UTC()
	state := a.state
	a.mu.Unlock()

	if err := a.stateStore.Save(state); err != nil {
		return fmt.Errorf("保存启动状态失败: %w", err)
	}

	return nil
}

func (a *App) markStopped() error {
	if a.stateStore == nil {
		return nil
	}

	a.mu.Lock()
	a.state.LastStoppedAt = a.now().UTC()
	state := a.state
	a.mu.Unlock()

	if err := a.stateStore.Save(state); err != nil {
		return fmt.Errorf("保存停止状态失败: %w", err)
	}

	return nil
}

func (a *App) RuntimeState() RuntimeState {
	if a == nil {
		return DefaultRuntimeState()
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state
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

	a.mu.Lock()
	a.current = cloned
	a.mu.Unlock()
	return nil
}
