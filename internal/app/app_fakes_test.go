package app

import (
	"sync"
	"time"

	"twitchdropsminergo/internal/config"
)

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
