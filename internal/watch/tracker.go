package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
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

func (t *Tracker) AddChannel(channel domain.Channel) {
	if t == nil || channel.ID <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cloned := cloneChannel(&channel)
	if existing, ok := t.channels[channel.ID]; ok && existing != nil {
		if cloned.DisplayName == "" {
			cloned.DisplayName = existing.channel.DisplayName
		}
		if cloned.Stream == nil {
			cloned.Stream = cloneStream(existing.channel.Stream)
		}
		if !cloned.PendingStream {
			cloned.PendingStream = existing.channel.PendingStream
		}
		existing.epoch = t.bumpEpochLocked()
		existing.channel = &cloned
		return
	}

	t.channels[channel.ID] = &trackedChannel{
		channel: &cloned,
		epoch:   t.bumpEpochLocked(),
	}
}

func (t *Tracker) RemoveChannel(channelID int64) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil {
		return
	}
	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
	}
	delete(t.channels, channelID)
}

func (t *Tracker) Channel(channelID int64) (domain.Channel, bool) {
	if t == nil {
		return domain.Channel{}, false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return domain.Channel{}, false
	}

	return cloneChannel(tracked.channel), true
}

func (t *Tracker) ProcessStreamState(ctx context.Context, channelID int64, message json.RawMessage) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}

	var parsed streamStateMessage
	if err := json.Unmarshal(message, &parsed); err != nil {
		return fmt.Errorf("解析 StreamState 消息失败: %w", err)
	}

	switch strings.TrimSpace(parsed.Type) {
	case "viewcount":
		channel, ok := t.Channel(channelID)
		if !ok {
			return ErrChannelNotTracked
		}
		if !channel.Online() {
			return t.CheckOnline(channelID)
		}
		t.mu.Lock()
		tracked := t.channels[channelID]
		if tracked != nil && tracked.channel != nil && tracked.channel.Stream != nil {
			tracked.channel.Stream.Viewers = parsed.Viewers
		}
		t.mu.Unlock()
		return nil
	case "stream-down":
		t.setOffline(channelID)
		return nil
	case "stream-up":
		return t.CheckOnline(channelID)
	case "commercial":
		return nil
	default:
		return nil
	}
}

func (t *Tracker) ProcessStreamUpdate(ctx context.Context, channelID int64, message json.RawMessage) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}
	if _, ok := t.Channel(channelID); !ok {
		return ErrChannelNotTracked
	}
	if len(message) > 0 && !json.Valid(message) {
		return fmt.Errorf("解析 StreamUpdate 消息失败: 无效 JSON")
	}

	return t.CheckOnline(channelID)
}

func (t *Tracker) applyFetched(channelID int64, expectedEpoch uint64, fetched fetchedChannel) {
	var (
		before  domain.Channel
		after   domain.Channel
		handler func(before, after domain.Channel)
		changed bool
	)

	t.mu.Lock()
	defer func() {
		t.mu.Unlock()
		if changed && handler != nil {
			handler(before, after)
		}
	}()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return
	}
	if tracked.epoch != expectedEpoch {
		return
	}
	before = cloneChannel(tracked.channel)

	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
		tracked.pendingCancel = nil
	}

	tracked.channel.PendingStream = false
	if fetched.DisplayName != "" {
		tracked.channel.DisplayName = fetched.DisplayName
	}
	tracked.channel.Stream = cloneStream(fetched.Stream)
	after = cloneChannel(tracked.channel)
	handler = t.onChange
	changed = !reflect.DeepEqual(before, after)
}

func (t *Tracker) setOffline(channelID int64) {
	var (
		before  domain.Channel
		after   domain.Channel
		handler func(before, after domain.Channel)
		changed bool
	)

	t.mu.Lock()
	defer func() {
		t.mu.Unlock()
		if changed && handler != nil {
			handler(before, after)
		}
	}()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return
	}
	before = cloneChannel(tracked.channel)
	tracked.epoch = t.bumpEpochLocked()
	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
		tracked.pendingCancel = nil
	}
	tracked.channel.PendingStream = false
	tracked.channel.Stream = nil
	after = cloneChannel(tracked.channel)
	handler = t.onChange
	changed = !reflect.DeepEqual(before, after)
}
