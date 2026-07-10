package pubsub

import (
	"context"
	"fmt"
)

func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("PubSub 管理器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	m.ctx = runCtx
	m.cancel = cancel
	m.running = true
	shards := append([]*shard(nil), m.shards...)
	m.mu.Unlock()

	for _, shard := range shards {
		shard.start(runCtx)
	}

	m.signalChanged()
	return nil
}

func (m *Manager) WaitUntilConnected(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("PubSub 管理器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	for {
		status := m.Status()
		if !status.Running {
			return ErrManagerNotRunning
		}
		if status.TopicCount == 0 {
			return nil
		}

		allConnected := true
		for _, shard := range status.Shards {
			if shard.TopicCount == 0 {
				continue
			}
			if !shard.Connected || shard.SubmittedCount != shard.TopicCount {
				allConnected = false
				break
			}
		}
		if allConnected {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.changed:
		}
	}
}

func (m *Manager) Stop(ctx context.Context, clearTopics bool) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	cancel := m.cancel
	shards := append([]*shard(nil), m.shards...)
	m.cancel = nil
	m.ctx = nil
	m.running = false
	if clearTopics {
		m.shards = nil
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	for _, shard := range shards {
		shard.stop(clearTopics)
	}

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}

	if !clearTopics {
		m.trimEmptyShards()
	}
	m.signalChanged()
	return nil
}

func (m *Manager) Status() Status {
	if m == nil {
		return Status{}
	}

	m.mu.Lock()
	running := m.running
	endpoint := m.endpoint
	shards := append([]*shard(nil), m.shards...)
	m.mu.Unlock()

	status := Status{
		Running:  running,
		Endpoint: endpoint,
		Shards:   make([]ShardStatus, 0, len(shards)),
	}
	for _, shard := range shards {
		shardStatus := shard.status()
		status.TopicCount += shardStatus.TopicCount
		status.Shards = append(status.Shards, shardStatus)
	}

	return status
}

func (m *Manager) signalChanged() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}
