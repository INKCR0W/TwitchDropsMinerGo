package pubsub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/httpclient"
)

const (
	defaultPingInterval = 3 * time.Minute
	defaultPingTimeout  = 10 * time.Second
)

var (
	ErrTopicLimitExceeded = errors.New("PubSub topic 数量超过分片上限")
	ErrManagerNotRunning  = errors.New("PubSub 管理器未启动")
)

type Authenticator interface {
	Validate(context.Context) error
	Snapshot() auth.Snapshot
}

type HeadersProvider func(context.Context) (http.Header, error)

type Connection interface {
	ReadMessage() (int, []byte, error)
	WriteJSON(any) error
	Close() error
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
}

type Dialer interface {
	DialContext(context.Context, string, http.Header) (Connection, *http.Response, error)
}

type Options struct {
	Logger          *slog.Logger
	Auth            Authenticator
	HeadersProvider HeadersProvider
	Dialer          Dialer
	Endpoint        string
	ProxyURL        string
	PingInterval    time.Duration
	PingTimeout     time.Duration
	ReadTimeout     time.Duration
	ListenBatchSize int
	MaxShards       int
	ShardTopicLimit int
	Backoff         httpclient.BackoffConfig
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
	NonceGenerator  func() (string, error)
}

type Status struct {
	Running    bool
	Endpoint   string
	TopicCount int
	Shards     []ShardStatus
}

type ShardState string

const (
	ShardStateDisconnected ShardState = "disconnected"
	ShardStateConnecting   ShardState = "connecting"
	ShardStateConnected    ShardState = "connected"
	ShardStateReconnecting ShardState = "reconnecting"
)

type ShardStatus struct {
	Index          int
	State          ShardState
	Connected      bool
	TopicCount     int
	SubmittedCount int
}

type Manager struct {
	logger          *slog.Logger
	auth            Authenticator
	headersProvider HeadersProvider
	dialer          Dialer
	endpoint        string
	pingInterval    time.Duration
	pingTimeout     time.Duration
	readTimeout     time.Duration
	listenBatchSize int
	maxShards       int
	shardTopicLimit int
	backoff         httpclient.BackoffConfig
	now             func() time.Time
	sleep           func(context.Context, time.Duration) error
	nonceGenerator  func() (string, error)

	mu      sync.Mutex
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
	shards  []*shard
	wg      sync.WaitGroup
	changed chan struct{}
}

func NewManager(options Options) (*Manager, error) {
	if options.Auth == nil {
		return nil, fmt.Errorf("PubSub 认证状态不能为空")
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	endpoint := strings.TrimSpace(options.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}

	pingInterval := options.PingInterval
	if pingInterval <= 0 {
		pingInterval = defaultPingInterval
	}

	pingTimeout := options.PingTimeout
	if pingTimeout <= 0 {
		pingTimeout = defaultPingTimeout
	}

	readTimeout := options.ReadTimeout
	if readTimeout <= 0 {
		readTimeout = DefaultReadTimeout
	}

	listenBatchSize := options.ListenBatchSize
	if listenBatchSize <= 0 {
		listenBatchSize = DefaultListenBatchSize
	}

	maxShards := options.MaxShards
	if maxShards <= 0 {
		maxShards = DefaultMaxShards
	}

	shardTopicLimit := options.ShardTopicLimit
	if shardTopicLimit <= 0 {
		shardTopicLimit = DefaultShardTopicLimit
	}

	backoff := options.Backoff
	if backoff == (httpclient.BackoffConfig{}) {
		backoff = httpclient.DefaultBackoffConfig()
	}
	if _, err := httpclient.NewExponentialBackoff(backoff); err != nil {
		return nil, err
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	nonceGenerator := options.NonceGenerator
	if nonceGenerator == nil {
		nonceGenerator = generateNonce
	}

	dialer := options.Dialer
	if dialer == nil {
		defaultDialer, err := newGorillaDialer(options.ProxyURL)
		if err != nil {
			return nil, err
		}
		dialer = defaultDialer
	}

	return &Manager{
		logger:          logger,
		auth:            options.Auth,
		headersProvider: options.HeadersProvider,
		dialer:          dialer,
		endpoint:        endpoint,
		pingInterval:    pingInterval,
		pingTimeout:     pingTimeout,
		readTimeout:     readTimeout,
		listenBatchSize: listenBatchSize,
		maxShards:       maxShards,
		shardTopicLimit: shardTopicLimit,
		backoff:         backoff,
		now:             now,
		sleep:           sleep,
		nonceGenerator:  nonceGenerator,
		changed:         make(chan struct{}, 1),
	}, nil
}

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

func (m *Manager) AddTopics(topics ...Topic) error {
	if m == nil {
		return fmt.Errorf("PubSub 管理器未初始化")
	}

	filtered := uniqueTopics(topics)
	if len(filtered) == 0 {
		return nil
	}

	m.mu.Lock()
	currentTotal := 0
	existing := make(map[string]struct{})
	for _, shard := range m.shards {
		shard.mu.Lock()
		currentTotal += len(shard.topics)
		for key := range shard.topics {
			existing[key] = struct{}{}
		}
		shard.mu.Unlock()
	}

	pending := make([]Topic, 0, len(filtered))
	for _, topic := range filtered {
		if _, ok := existing[topic.Key()]; ok {
			continue
		}
		pending = append(pending, topic)
	}
	if len(pending) == 0 {
		m.mu.Unlock()
		return nil
	}

	capacity := (m.maxShards * m.shardTopicLimit) - currentTotal
	if len(pending) > capacity {
		m.mu.Unlock()
		return ErrTopicLimitExceeded
	}

	running := m.running
	runCtx := m.ctx
	newShards := make([]*shard, 0)
	for _, topic := range pending {
		var assigned bool
		for _, shard := range m.shards {
			if shard.addTopic(topic, m.shardTopicLimit) {
				assigned = true
				break
			}
		}
		if assigned {
			continue
		}

		shard := newShard(m, len(m.shards))
		if !shard.addTopic(topic, m.shardTopicLimit) {
			m.mu.Unlock()
			return fmt.Errorf("无法向新分片添加 topic %s", topic.Key())
		}

		m.shards = append(m.shards, shard)
		newShards = append(newShards, shard)
	}

	if running {
		for _, shard := range newShards {
			shard.start(runCtx)
		}
	}
	m.mu.Unlock()

	m.signalChanged()
	return nil
}

func (m *Manager) RemoveTopics(keys ...string) {
	if m == nil {
		return
	}

	keys = NormalizeTopicKeys(keys)
	if len(keys) == 0 {
		return
	}

	m.mu.Lock()
	for _, shard := range m.shards {
		shard.removeTopics(keys)
	}

	totalTopics := 0
	for _, shard := range m.shards {
		totalTopics += shard.topicCount()
	}

	requiredShards := 0
	if totalTopics > 0 {
		requiredShards = (totalTopics + m.shardTopicLimit - 1) / m.shardTopicLimit
	}

	removedShards := make([]*shard, 0)
	recycled := make([]Topic, 0)
	for len(m.shards) > requiredShards {
		last := m.shards[len(m.shards)-1]
		m.shards = m.shards[:len(m.shards)-1]
		recycled = append(recycled, last.clearAndDrainTopics()...)
		removedShards = append(removedShards, last)
	}

	if len(recycled) > 0 {
		sort.Slice(recycled, func(i, j int) bool {
			return recycled[i].Key() < recycled[j].Key()
		})
		for _, topic := range recycled {
			for _, shard := range m.shards {
				if shard.addTopic(topic, m.shardTopicLimit) {
					break
				}
			}
		}
	}
	m.mu.Unlock()

	for _, shard := range removedShards {
		shard.stop(true)
	}

	m.signalChanged()
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

func (m *Manager) trimEmptyShards() {
	m.mu.Lock()
	defer m.mu.Unlock()

	filtered := m.shards[:0]
	for _, shard := range m.shards {
		if shard.topicCount() == 0 {
			continue
		}
		shard.setIndex(len(filtered))
		filtered = append(filtered, shard)
	}
	m.shards = filtered
}

func (m *Manager) signalChanged() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

func (m *Manager) buildHeaders(ctx context.Context) (http.Header, error) {
	if m.headersProvider == nil {
		return make(http.Header), nil
	}

	headers, err := m.headersProvider(ctx)
	if err != nil {
		return nil, err
	}
	if headers == nil {
		return make(http.Header), nil
	}

	return headers.Clone(), nil
}
