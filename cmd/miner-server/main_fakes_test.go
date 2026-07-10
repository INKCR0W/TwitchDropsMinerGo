package main

import (
	"errors"
	"sync"

	"twitchdropsminergo/internal/app"
	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/scheduler"
)

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

type flakyStateTarget struct {
	settings config.Settings
	mu       sync.Mutex
	calls    int
}

func (f *flakyStateTarget) Settings() config.Settings {
	if f == nil {
		return config.DefaultSettings()
	}
	settings := f.settings.Clone()
	if settings.IsZero() {
		settings = config.DefaultSettings()
	}
	return settings
}

func (f *flakyStateTarget) UpdateObservation(app.Observation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == 1 {
		return errors.New("disk full")
	}
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
