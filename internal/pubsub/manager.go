package pubsub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/httpclient"
)

const (
	defaultPingInterval = 3 * time.Minute
	defaultPingTimeout  = 10 * time.Second

	// 低于此阈值说明服务端接受连接后立即断开,退避须继续增长而非重置
	minConnectionLifetimeForBackoffReset = 5 * time.Second
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
