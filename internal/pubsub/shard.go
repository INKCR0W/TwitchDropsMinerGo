package pubsub

import (
	"context"
	"sync"
	"time"
)

type shard struct {
	manager *Manager
	index   int

	mu        sync.Mutex
	state     ShardState
	topics    map[string]Topic
	submitted map[string]Topic
	pending   map[string]pendingSubmission
	connected bool
	conn      Connection
	cancel    context.CancelFunc
	done      chan struct{}
	wake      chan struct{}
	events    chan Event
	started   bool
}

const eventBufferSize = 64

type pendingSubmission struct {
	action string
	topics []string
}

func newShard(manager *Manager, index int) *shard {
	return &shard{
		manager:   manager,
		index:     index,
		state:     ShardStateDisconnected,
		topics:    make(map[string]Topic),
		submitted: make(map[string]Topic),
		pending:   make(map[string]pendingSubmission),
		wake:      make(chan struct{}, 1),
	}
}

func (s *shard) start(parent context.Context) {
	if s == nil || parent == nil {
		return
	}

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}

	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	events := make(chan Event, eventBufferSize)
	s.events = events
	s.started = true
	s.mu.Unlock()

	s.manager.wg.Add(1)
	go s.run(ctx)

	s.manager.wg.Add(1)
	go s.dispatchLoop(ctx, events)
}

func (s *shard) dispatchLoop(ctx context.Context, events <-chan Event) {
	defer s.manager.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			handler := event.Topic.Handler()
			if handler == nil {
				continue
			}
			if err := handler(ctx, event); err != nil && ctx.Err() == nil {
				s.manager.logger.Warn("处理 PubSub 事件失败", "shard", s.index, "topic", event.Topic.Key(), "error", err)
			}
		}
	}
}

func (s *shard) eventsChan() chan Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events
}

func (s *shard) stop(clearTopics bool) {
	if s == nil {
		return
	}

	s.mu.Lock()
	cancel := s.cancel
	conn := s.conn
	if clearTopics {
		s.topics = make(map[string]Topic)
		s.submitted = make(map[string]Topic)
		s.pending = make(map[string]pendingSubmission)
		s.connected = false
		s.state = ShardStateDisconnected
	}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

func (s *shard) status() ShardStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	return ShardStatus{
		Index:          s.index,
		State:          s.state,
		Connected:      s.connected,
		TopicCount:     len(s.topics),
		SubmittedCount: len(s.submitted),
	}
}

func (s *shard) topicCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.topics)
}

func (s *shard) setIndex(index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index = index
}

func (s *shard) setState(state ShardState, connected bool) {
	s.mu.Lock()
	s.state = state
	s.connected = connected
	s.mu.Unlock()
	s.manager.signalChanged()
}

func (s *shard) setConn(conn Connection) {
	s.mu.Lock()
	s.conn = conn
	s.submitted = make(map[string]Topic)
	s.pending = make(map[string]pendingSubmission)
	s.mu.Unlock()
}

func (s *shard) clearConn() {
	s.mu.Lock()
	s.conn = nil
	s.connected = false
	s.submitted = make(map[string]Topic)
	s.pending = make(map[string]pendingSubmission)
	s.mu.Unlock()
	s.manager.signalChanged()
}

func (s *shard) refreshConnectedState() {
	s.mu.Lock()
	connected := s.conn != nil
	if connected && len(s.topics) != len(s.submitted) {
		connected = false
	}
	s.connected = connected
	if connected {
		s.state = ShardStateConnected
	} else if s.conn != nil {
		s.state = ShardStateConnecting
	}
	s.mu.Unlock()
	s.manager.signalChanged()
}

func (s *shard) wakeChan() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}

	return s.wake
}

func (s *shard) signalWake(wake chan struct{}) {
	if wake == nil {
		return
	}

	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *shard) nextWaitDelay(nextPing time.Time, pongDeadline time.Time) time.Duration {
	wait := s.manager.readTimeout
	if wait <= 0 {
		wait = time.Second
	}

	now := s.manager.now()
	if untilPing := nextPing.Sub(now); untilPing < wait {
		wait = untilPing
	}
	if !pongDeadline.IsZero() {
		if untilPong := pongDeadline.Sub(now); untilPong < wait {
			wait = untilPong
		}
	}
	if wait < 0 {
		return 0
	}
	return wait
}
