package pubsub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/httpclient"
)

const (
	defaultPingInterval = 3 * time.Minute
	defaultPingTimeout  = 10 * time.Second
	defaultNonceLength  = 30
	textMessageType     = 1
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

type shard struct {
	manager *Manager
	index   int

	mu        sync.Mutex
	state     ShardState
	topics    map[string]Topic
	submitted map[string]Topic
	connected bool
	conn      Connection
	cancel    context.CancelFunc
	done      chan struct{}
	started   bool
}

type outboundEnvelope struct {
	Type  string        `json:"type"`
	Nonce string        `json:"nonce,omitempty"`
	Data  *outboundData `json:"data,omitempty"`
}

type outboundData struct {
	Topics    []string `json:"topics"`
	AuthToken string   `json:"auth_token"`
}

type inboundEnvelope struct {
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type inboundMessage struct {
	Topic   string `json:"topic"`
	Message string `json:"message"`
}

type gorillaDialer struct {
	dialer websocket.Dialer
}

type gorillaConnection struct {
	conn *websocket.Conn
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
			if !shard.Connected {
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

		shard := &shard{
			manager:   m,
			index:     len(m.shards),
			state:     ShardStateDisconnected,
			topics:    make(map[string]Topic),
			submitted: make(map[string]Topic),
		}
		if !shard.addTopic(topic, m.shardTopicLimit) {
			m.mu.Unlock()
			return fmt.Errorf("无法向新分片添加 topic %s", topic.Key())
		}

		m.shards = append(m.shards, shard)
		newShards = append(newShards, shard)
	}
	m.mu.Unlock()

	if running {
		for _, shard := range newShards {
			shard.start(runCtx)
		}
	}

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
		shard.index = len(filtered)
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
	s.started = true
	s.mu.Unlock()

	s.manager.wg.Add(1)
	go s.run(ctx)
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

func (s *shard) run(ctx context.Context) {
	defer s.manager.wg.Done()

	s.setState(ShardStateDisconnected, false)
	backoff, _ := httpclient.NewExponentialBackoff(s.manager.backoff)
	for {
		if err := ctx.Err(); err != nil {
			s.finishRun()
			return
		}

		if s.topicCount() == 0 {
			s.setState(ShardStateDisconnected, false)
			if err := s.manager.sleep(ctx, s.manager.readTimeout); err != nil {
				s.finishRun()
				return
			}
			continue
		}

		s.setState(ShardStateConnecting, false)
		if err := s.manager.auth.Validate(ctx); err != nil {
			s.manager.logger.Warn("验证 PubSub 认证失败", "shard", s.index, "error", err)
			if err := s.manager.sleep(ctx, backoff.Next()); err != nil {
				s.finishRun()
				return
			}
			continue
		}

		headers, err := s.manager.buildHeaders(ctx)
		if err != nil {
			s.manager.logger.Warn("构造 PubSub 握手请求头失败", "shard", s.index, "error", err)
			if err := s.manager.sleep(ctx, backoff.Next()); err != nil {
				s.finishRun()
				return
			}
			continue
		}

		conn, _, err := s.manager.dialer.DialContext(ctx, s.manager.endpoint, headers)
		if err != nil {
			s.manager.logger.Warn("连接 PubSub 失败", "shard", s.index, "error", err)
			if err := s.manager.sleep(ctx, backoff.Next()); err != nil {
				s.finishRun()
				return
			}
			continue
		}

		backoff.Reset()
		s.setConn(conn)
		s.setState(ShardStateConnected, true)
		if err := s.handleConnection(ctx, conn); err != nil && ctx.Err() == nil {
			s.manager.logger.Warn("PubSub 连接断开，准备重连", "shard", s.index, "error", err)
		}

		_ = conn.Close()
		s.clearConn()
		if ctx.Err() != nil {
			s.finishRun()
			return
		}

		if s.topicCount() == 0 {
			s.setState(ShardStateDisconnected, false)
			continue
		}

		s.setState(ShardStateReconnecting, false)
	}
}

func (s *shard) finishRun() {
	s.mu.Lock()
	done := s.done
	s.done = nil
	s.cancel = nil
	s.conn = nil
	s.started = false
	s.connected = false
	s.submitted = make(map[string]Topic)
	s.state = ShardStateDisconnected
	s.mu.Unlock()

	s.manager.signalChanged()
	if done != nil {
		close(done)
	}
}

func (s *shard) handleConnection(ctx context.Context, conn Connection) error {
	nextPing := s.manager.now()
	pongDeadline := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		now := s.manager.now()
		if !pongDeadline.IsZero() && !now.Before(pongDeadline) {
			return fmt.Errorf("等待 PONG 超时")
		}
		if !now.Before(nextPing) {
			if err := s.send(conn, outboundEnvelope{Type: "PING"}); err != nil {
				return err
			}
			nextPing = now.Add(s.manager.pingInterval)
			pongDeadline = now.Add(s.manager.pingTimeout)
		}

		if err := s.syncTopics(ctx, conn); err != nil {
			return err
		}

		pongReceived, reconnect, err := s.receiveOnce(ctx, conn)
		if err != nil {
			if isTimeoutError(err) {
				continue
			}
			return err
		}
		if pongReceived {
			pongDeadline = time.Time{}
		}
		if reconnect {
			return fmt.Errorf("服务器请求重连")
		}
	}
}

func (s *shard) syncTopics(ctx context.Context, conn Connection) error {
	currentTopics, submittedTopics := s.snapshotTopics()

	removed := make([]string, 0)
	for key := range submittedTopics {
		if _, ok := currentTopics[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)

	added := make([]Topic, 0)
	for key, topic := range currentTopics {
		if _, ok := submittedTopics[key]; ok {
			continue
		}
		added = append(added, topic)
	}
	sort.Slice(added, func(i, j int) bool {
		return added[i].Key() < added[j].Key()
	})

	if len(removed) == 0 && len(added) == 0 {
		return nil
	}

	if err := s.manager.auth.Validate(ctx); err != nil {
		return err
	}

	accessToken := strings.TrimSpace(s.manager.auth.Snapshot().AccessToken)
	if accessToken == "" {
		return fmt.Errorf("PubSub access token 为空")
	}

	for _, batch := range chunkStrings(removed, s.manager.listenBatchSize) {
		if err := s.send(conn, outboundEnvelope{
			Type: "UNLISTEN",
			Data: &outboundData{
				Topics:    batch,
				AuthToken: accessToken,
			},
		}); err != nil {
			return err
		}
		s.markUnsubmitted(batch)
	}

	if len(added) == 0 {
		return nil
	}

	addedKeys := make([]string, 0, len(added))
	for _, topic := range added {
		addedKeys = append(addedKeys, topic.Key())
	}
	for _, batch := range chunkStrings(addedKeys, s.manager.listenBatchSize) {
		if err := s.send(conn, outboundEnvelope{
			Type: "LISTEN",
			Data: &outboundData{
				Topics:    batch,
				AuthToken: accessToken,
			},
		}); err != nil {
			return err
		}
		s.markSubmitted(batch, currentTopics)
	}

	return nil
}

func (s *shard) receiveOnce(ctx context.Context, conn Connection) (pongReceived bool, reconnect bool, err error) {
	if err := conn.SetReadDeadline(s.manager.now().Add(s.manager.readTimeout)); err != nil {
		return false, false, err
	}

	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return false, false, err
	}
	if messageType != textMessageType {
		return false, false, nil
	}

	var envelope inboundEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return false, false, fmt.Errorf("解析 PubSub 消息失败: %w", err)
	}

	switch envelope.Type {
	case "MESSAGE":
		if err := s.dispatchMessage(ctx, envelope.Data); err != nil {
			return false, false, err
		}
	case "PONG":
		return true, false, nil
	case "RESPONSE":
		if envelope.Error != "" {
			s.manager.logger.Warn("PubSub 返回响应错误", "shard", s.index, "error", envelope.Error)
		}
	case "RECONNECT":
		return false, true, nil
	default:
		s.manager.logger.Warn("收到未知 PubSub 消息类型", "shard", s.index, "type", envelope.Type)
	}

	return false, false, nil
}

func (s *shard) dispatchMessage(ctx context.Context, payload json.RawMessage) error {
	var message inboundMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return fmt.Errorf("解析 PubSub 事件失败: %w", err)
	}

	topic, ok := s.lookupTopic(message.Topic)
	if !ok || topic.Handler() == nil {
		return nil
	}

	raw := json.RawMessage(message.Message)
	if !json.Valid(raw) {
		quoted, err := json.Marshal(message.Message)
		if err != nil {
			return fmt.Errorf("编码 PubSub 字符串消息失败: %w", err)
		}
		raw = quoted
	}

	event := Event{
		Topic:      topic,
		Message:    raw,
		ReceivedAt: s.manager.now().UTC(),
	}

	s.manager.wg.Add(1)
	go func() {
		defer s.manager.wg.Done()
		if err := topic.Handler()(ctx, event); err != nil && ctx.Err() == nil {
			s.manager.logger.Warn("处理 PubSub 事件失败", "shard", s.index, "topic", topic.Key(), "error", err)
		}
	}()

	return nil
}

func (s *shard) send(conn Connection, envelope outboundEnvelope) error {
	if conn == nil {
		return fmt.Errorf("PubSub 连接不存在")
	}
	if envelope.Type != "PING" {
		nonce, err := s.manager.nonceGenerator()
		if err != nil {
			return err
		}
		envelope.Nonce = nonce
	}
	if err := conn.SetWriteDeadline(s.manager.now().Add(s.manager.pingTimeout)); err != nil {
		return err
	}
	if err := conn.WriteJSON(envelope); err != nil {
		return err
	}

	return nil
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

func (s *shard) addTopic(topic Topic, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.topics == nil {
		s.topics = make(map[string]Topic)
	}
	if s.submitted == nil {
		s.submitted = make(map[string]Topic)
	}
	if len(s.topics) >= limit {
		return false
	}
	s.topics[topic.Key()] = topic
	return true
}

func (s *shard) removeTopics(keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.topics) == 0 {
		return
	}
	for _, key := range keys {
		delete(s.topics, key)
	}
}

func (s *shard) clearAndDrainTopics() []Topic {
	s.mu.Lock()
	defer s.mu.Unlock()

	drained := make([]Topic, 0, len(s.topics))
	for _, topic := range s.topics {
		drained = append(drained, topic)
	}
	s.topics = make(map[string]Topic)
	s.submitted = make(map[string]Topic)
	s.connected = false
	return drained
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
	s.mu.Unlock()
}

func (s *shard) clearConn() {
	s.mu.Lock()
	s.conn = nil
	s.connected = false
	s.submitted = make(map[string]Topic)
	s.mu.Unlock()
	s.manager.signalChanged()
}

func (s *shard) snapshotTopics() (map[string]Topic, map[string]Topic) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := make(map[string]Topic, len(s.topics))
	for key, topic := range s.topics {
		current[key] = topic
	}
	submitted := make(map[string]Topic, len(s.submitted))
	for key, topic := range s.submitted {
		submitted[key] = topic
	}

	return current, submitted
}

func (s *shard) lookupTopic(key string) (Topic, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	topic, ok := s.topics[key]
	return topic, ok
}

func (s *shard) markUnsubmitted(keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, key := range keys {
		delete(s.submitted, key)
	}
}

func (s *shard) markSubmitted(keys []string, current map[string]Topic) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.submitted == nil {
		s.submitted = make(map[string]Topic)
	}
	for _, key := range keys {
		topic, ok := current[key]
		if !ok {
			continue
		}
		if _, exists := s.topics[key]; !exists {
			continue
		}
		s.submitted[key] = topic
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

func newGorillaDialer(proxyURL string) (Dialer, error) {
	dialer := *websocket.DefaultDialer
	if strings.TrimSpace(proxyURL) != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("解析 PubSub 代理地址失败: %w", err)
		}
		dialer.Proxy = http.ProxyURL(parsedURL)
	}

	return &gorillaDialer{dialer: dialer}, nil
}

func (d *gorillaDialer) DialContext(ctx context.Context, endpoint string, headers http.Header) (Connection, *http.Response, error) {
	conn, response, err := d.dialer.DialContext(ctx, endpoint, headers)
	if err != nil {
		return nil, response, err
	}

	return &gorillaConnection{conn: conn}, response, nil
}

func (c *gorillaConnection) ReadMessage() (int, []byte, error) {
	return c.conn.ReadMessage()
}

func (c *gorillaConnection) WriteJSON(value any) error {
	return c.conn.WriteJSON(value)
}

func (c *gorillaConnection) Close() error {
	return c.conn.Close()
}

func (c *gorillaConnection) SetReadDeadline(deadline time.Time) error {
	return c.conn.SetReadDeadline(deadline)
}

func (c *gorillaConnection) SetWriteDeadline(deadline time.Time) error {
	return c.conn.SetWriteDeadline(deadline)
}

func uniqueTopics(topics []Topic) []Topic {
	if len(topics) == 0 {
		return []Topic{}
	}

	seen := make(map[string]struct{}, len(topics))
	unique := make([]Topic, 0, len(topics))
	for _, topic := range topics {
		if topic.Key() == "" {
			continue
		}
		if _, exists := seen[topic.Key()]; exists {
			continue
		}
		seen[topic.Key()] = struct{}{}
		unique = append(unique, topic)
	}

	sort.Slice(unique, func(i, j int) bool {
		return unique[i].Key() < unique[j].Key()
	})
	return unique
}

func chunkStrings(values []string, size int) [][]string {
	if len(values) == 0 {
		return nil
	}
	if size <= 0 {
		size = len(values)
	}

	chunks := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunk := append([]string(nil), values[start:end]...)
		chunks = append(chunks, chunk)
	}

	return chunks
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	var netError net.Error
	return errors.As(err, &netError) && netError.Timeout()
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func generateNonce() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	random := make([]byte, defaultNonceLength)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("生成 PubSub nonce 失败: %w", err)
	}

	nonce := make([]byte, defaultNonceLength)
	for index, value := range random {
		nonce[index] = alphabet[int(value)%len(alphabet)]
	}

	return string(nonce), nil
}
