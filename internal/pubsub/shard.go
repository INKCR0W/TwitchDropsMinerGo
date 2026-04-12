package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

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
	wake      chan struct{}
	started   bool
}

func newShard(manager *Manager, index int) *shard {
	return &shard{
		manager:   manager,
		index:     index,
		state:     ShardStateDisconnected,
		topics:    make(map[string]Topic),
		submitted: make(map[string]Topic),
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
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()

	results := make(chan readResult, 1)
	go s.readLoop(readCtx, conn, results)

	nextPing := s.manager.now()
	pongDeadline := time.Time{}
	wake := s.wakeChan()
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

		timer := time.NewTimer(s.nextWaitDelay(nextPing, pongDeadline))
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return ctx.Err()
		case <-wake:
			stopTimer(timer)
		case result, ok := <-results:
			stopTimer(timer)
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				return fmt.Errorf("PubSub 读循环意外结束")
			}
			if result.err != nil {
				return result.err
			}

			pongReceived, reconnect, err := s.handleInbound(ctx, result.messageType, result.payload)
			if err != nil {
				return err
			}
			if pongReceived {
				pongDeadline = time.Time{}
			}
			if reconnect {
				return fmt.Errorf("服务器请求重连")
			}
		case <-timer.C:
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

func (s *shard) readLoop(ctx context.Context, conn Connection, results chan<- readResult) {
	defer close(results)

	for {
		messageType, payload, err := conn.ReadMessage()
		select {
		case results <- readResult{messageType: messageType, payload: payload, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *shard) handleInbound(ctx context.Context, messageType int, payload []byte) (pongReceived bool, reconnect bool, err error) {
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
	var wake chan struct{}

	s.mu.Lock()
	if s.topics == nil {
		s.topics = make(map[string]Topic)
	}
	if s.submitted == nil {
		s.submitted = make(map[string]Topic)
	}
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	if len(s.topics) >= limit {
		s.mu.Unlock()
		return false
	}
	s.topics[topic.Key()] = topic
	wake = s.wake
	s.mu.Unlock()

	s.signalWake(wake)
	return true
}

func (s *shard) removeTopics(keys []string) {
	var wake chan struct{}
	var changed bool

	s.mu.Lock()
	if len(s.topics) == 0 {
		s.mu.Unlock()
		return
	}
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	for _, key := range keys {
		if _, exists := s.topics[key]; exists {
			changed = true
		}
		delete(s.topics, key)
	}
	if changed {
		wake = s.wake
	}
	s.mu.Unlock()

	s.signalWake(wake)
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
