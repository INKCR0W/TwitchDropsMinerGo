package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/inventory"
)

const (
	DefaultOnlineDelay = 120 * time.Second
	defaultBatchSize   = 20
)

var (
	ErrChannelNotTracked = errors.New("watch 频道未纳入跟踪")
)

type GQLClient interface {
	Do(context.Context, gql.Operation) (gql.Response, error)
	DoRaw(context.Context, gql.RawQuery) (gql.Response, error)
	DoBatch(context.Context, []gql.Operation) ([]gql.Response, error)
}

type AuthState interface {
	Validate(context.Context) error
	Snapshot() auth.Snapshot
}

type Options struct {
	GQLClient   GQLClient
	AuthState   AuthState
	OnlineDelay time.Duration
	BatchSize   int
	Clock       func() time.Time
	Sleep       func(context.Context, time.Duration) error
	Logger      *slog.Logger
}

type Tracker struct {
	gqlClient   GQLClient
	authState   AuthState
	logger      *slog.Logger
	onlineDelay time.Duration
	batchSize   int
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error

	ctx    context.Context
	cancel context.CancelFunc

	mu           sync.Mutex
	settings     config.Settings
	inventory    inventory.Snapshot
	channels     map[int64]*trackedChannel
	epochCounter uint64
	onChange     func(before, after domain.Channel)
	wg           sync.WaitGroup
}

type trackedChannel struct {
	channel       *domain.Channel
	epoch         uint64
	pendingSeq    uint64
	pendingCancel context.CancelFunc
}

type channelSpec struct {
	ID          int64
	Login       string
	DisplayName string
	ACLBased    bool
	Epoch       uint64
}

type fetchedChannel struct {
	DisplayName string
	Stream      *domain.Stream
}

type streamStateMessage struct {
	Type    string `json:"type"`
	Viewers int    `json:"viewers"`
}

func NewTracker(options Options) (*Tracker, error) {
	if options.GQLClient == nil {
		return nil, fmt.Errorf("watch GQL 客户端不能为空")
	}
	if options.AuthState == nil {
		return nil, fmt.Errorf("watch 认证状态不能为空")
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	onlineDelay := options.OnlineDelay
	if onlineDelay <= 0 {
		onlineDelay = DefaultOnlineDelay
	}

	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	now := options.Clock
	if now == nil {
		now = time.Now
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Tracker{
		gqlClient:   options.GQLClient,
		authState:   options.AuthState,
		logger:      logger,
		onlineDelay: onlineDelay,
		batchSize:   batchSize,
		now:         now,
		sleep:       sleep,
		ctx:         ctx,
		cancel:      cancel,
		settings:    config.DefaultSettings(),
		channels:    make(map[int64]*trackedChannel),
	}, nil
}

func (t *Tracker) bumpEpochLocked() uint64 {
	t.epochCounter++
	return t.epochCounter
}

func (t *Tracker) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	t.cancel()

	t.mu.Lock()
	for _, tracked := range t.channels {
		if tracked == nil || tracked.pendingCancel == nil {
			continue
		}
		tracked.pendingCancel()
		tracked.pendingCancel = nil
		if tracked.channel != nil {
			tracked.channel.PendingStream = false
		}
	}
	t.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		t.wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (t *Tracker) Configure(settings config.Settings, snapshot inventory.Snapshot) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.settings = settings
	t.inventory = snapshot
}

func (t *Tracker) SetChannelChangeHandler(handler func(before, after domain.Channel)) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.onChange = handler
}
