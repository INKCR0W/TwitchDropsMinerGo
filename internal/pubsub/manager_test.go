package pubsub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"twitchdropsminergo/internal/auth"
)

func TestTopicHelpersMatchReferenceFormat(t *testing.T) {
	t.Parallel()

	userTopic := MustNewTopic(CategoryUser, TopicDrops, 42, nil)
	if userTopic.Key() != "user-drop-events.42" {
		t.Fatalf("用户 topic key 不匹配: %q", userTopic.Key())
	}

	channelTopic, err := ChannelTopic(TopicStreamUpdate, 99, nil)
	if err != nil {
		t.Fatalf("创建频道 topic 失败: %v", err)
	}
	if channelTopic.Key() != "broadcast-settings-update.99" {
		t.Fatalf("频道 topic key 不匹配: %q", channelTopic.Key())
	}

	prefix, targetID, ok := ParseTopicKey(channelTopic.Key())
	if !ok || prefix != "broadcast-settings-update" || targetID != 99 {
		t.Fatalf("解析 topic key 失败: prefix=%q targetID=%d ok=%v", prefix, targetID, ok)
	}
}

func TestManagerShardsAndRecyclesTopics(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Auth:            &stubAuthState{snapshot: auth.Snapshot{AccessToken: "token-1"}},
		MaxShards:       2,
		ShardTopicLimit: 2,
	})

	topic1 := MustNewTopic(CategoryUser, TopicDrops, 1, nil)
	topic2 := MustNewTopic(CategoryUser, TopicNotifications, 1, nil)
	topic3 := MustNewTopic(CategoryChannel, TopicStreamState, 10, nil)

	if err := manager.AddTopics(topic1, topic2, topic3); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	status := manager.Status()
	if status.TopicCount != 3 || len(status.Shards) != 2 {
		t.Fatalf("分片状态不匹配: %#v", status)
	}
	if status.Shards[0].TopicCount != 2 || status.Shards[1].TopicCount != 1 {
		t.Fatalf("分片分配不正确: %#v", status.Shards)
	}

	manager.RemoveTopics(topic2.Key())

	status = manager.Status()
	if status.TopicCount != 2 || len(status.Shards) != 1 {
		t.Fatalf("回收冗余分片失败: %#v", status)
	}
	if status.Shards[0].TopicCount != 2 {
		t.Fatalf("回收后 topic 数量不匹配: %#v", status.Shards)
	}
}

func TestManagerBatchesListenAndUnlistenCommands(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn}}
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-1"},
		},
		HeadersProvider: func(context.Context) (http.Header, error) {
			headers := make(http.Header)
			headers.Set("X-Device-Id", "device-1")
			return headers, nil
		},
		Dialer:          dialer,
		ListenBatchSize: 2,
		ShardTopicLimit: 10,
		ReadTimeout:     5 * time.Millisecond,
		PingInterval:    time.Hour,
	})

	topics := []Topic{
		MustNewTopic(CategoryUser, TopicDrops, 1, nil),
		MustNewTopic(CategoryUser, TopicNotifications, 1, nil),
		MustNewTopic(CategoryChannel, TopicStreamState, 10, nil),
	}
	if err := manager.AddTopics(topics...); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start 返回错误: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := manager.Stop(stopCtx, true); err != nil {
			t.Fatalf("Stop 返回错误: %v", err)
		}
	}()

	if err := manager.WaitUntilConnected(context.Background()); err != nil {
		t.Fatalf("WaitUntilConnected 返回错误: %v", err)
	}

	if dialer.CallCount() != 1 {
		t.Fatalf("Dial 次数不匹配: %d", dialer.CallCount())
	}
	if got := dialer.Header(0).Get("X-Device-Id"); got != "device-1" {
		t.Fatalf("握手请求头未透传: %q", got)
	}

	listen1 := conn.waitForType(t, "LISTEN", time.Second)
	listen2 := conn.waitForType(t, "LISTEN", time.Second)
	listenBatches := [][]string{
		topicsFromEnvelope(t, listen1),
		topicsFromEnvelope(t, listen2),
	}
	if got := len(listenBatches[0]) + len(listenBatches[1]); got != 3 {
		t.Fatalf("LISTEN topic 总数不匹配: %#v", listenBatches)
	}
	if got := len(listenBatches[0]); got != 2 && len(listenBatches[1]) != 2 {
		t.Fatalf("LISTEN 分批大小不匹配: %#v", listenBatches)
	}
	assertAuthTokenAndNonce(t, listen1, "token-1")
	assertAuthTokenAndNonce(t, listen2, "token-1")

	manager.RemoveTopics(topics[0].Key(), topics[1].Key())

	unlisten := conn.waitForType(t, "UNLISTEN", time.Second)
	if got := len(topicsFromEnvelope(t, unlisten)); got != 2 {
		t.Fatalf("UNLISTEN 分批大小不匹配: %#v", unlisten)
	}
	assertAuthTokenAndNonce(t, unlisten, "token-1")
}

func TestManagerReconnectsAfterPingTimeoutAndResubscribes(t *testing.T) {
	t.Parallel()

	conn1 := newFakeConn()
	conn2 := newFakeConn()
	dialer := &fakeDialer{connections: []*fakeConn{conn1, conn2}}
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-2"},
		},
		Dialer:          dialer,
		ReadTimeout:     5 * time.Millisecond,
		PingInterval:    10 * time.Millisecond,
		PingTimeout:     10 * time.Millisecond,
		ShardTopicLimit: 10,
	})

	topic := MustNewTopic(CategoryUser, TopicDrops, 77, nil)
	if err := manager.AddTopics(topic); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start 返回错误: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := manager.Stop(stopCtx, true); err != nil {
			t.Fatalf("Stop 返回错误: %v", err)
		}
	}()

	conn1.waitForType(t, "PING", time.Second)
	waitUntil(t, time.Second, func() bool {
		return dialer.CallCount() == 2
	})
	conn1.waitClosed(t, time.Second)

	listen := conn2.waitForType(t, "LISTEN", time.Second)
	if got := topicsFromEnvelope(t, listen); len(got) != 1 || got[0] != topic.Key() {
		t.Fatalf("重连后未重新订阅 topic: %#v", got)
	}
}

func TestHandleConnectionStopsAfterReadFailure(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-timeout"},
		},
		PingInterval: time.Hour,
		PingTimeout:  time.Hour,
		ReadTimeout:  10 * time.Millisecond,
	})

	shard := &shard{
		manager:   manager,
		topics:    make(map[string]Topic),
		submitted: make(map[string]Topic),
		wake:      make(chan struct{}, 1),
	}
	conn := &errorOnceConn{err: fakeTimeoutError{}}

	err := shard.handleConnection(context.Background(), conn)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("期望返回超时错误，实际为 %v", err)
	}
	if got := conn.reads.Load(); got != 1 {
		t.Fatalf("读失败后不应再次读取同一连接，实际读取次数为 %d", got)
	}
}

func TestManagerDispatchesIncomingMessage(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-3"},
		},
		Dialer:       &fakeDialer{connections: []*fakeConn{conn}},
		ReadTimeout:  5 * time.Millisecond,
		PingInterval: time.Hour,
	})

	events := make(chan Event, 1)
	topic := MustNewTopic(CategoryUser, TopicDrops, 42, func(_ context.Context, event Event) error {
		events <- event
		return nil
	})
	if err := manager.AddTopics(topic); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start 返回错误: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		if err := manager.Stop(stopCtx, true); err != nil {
			t.Fatalf("Stop 返回错误: %v", err)
		}
	}()

	conn.waitForType(t, "LISTEN", time.Second)

	conn.pushText(t, map[string]any{
		"type": "MESSAGE",
		"data": map[string]any{
			"topic":   topic.Key(),
			"message": `{"type":"drop-progress","current_progress_min":17}`,
		},
	})

	select {
	case event := <-events:
		if event.Topic.Key() != topic.Key() {
			t.Fatalf("分发 topic 不匹配: %q", event.Topic.Key())
		}

		var payload map[string]any
		if err := json.Unmarshal(event.Message, &payload); err != nil {
			t.Fatalf("解析事件消息失败: %v", err)
		}
		if got := payload["type"]; got != "drop-progress" {
			t.Fatalf("事件类型不匹配: %#v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 PubSub 事件分发")
	}
}

func TestManagerRejectsTopicOverflow(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Auth:            &stubAuthState{snapshot: auth.Snapshot{AccessToken: "token-4"}},
		MaxShards:       1,
		ShardTopicLimit: 1,
	})

	err := manager.AddTopics(
		MustNewTopic(CategoryUser, TopicDrops, 1, nil),
		MustNewTopic(CategoryUser, TopicNotifications, 1, nil),
	)
	if !errors.Is(err, ErrTopicLimitExceeded) {
		t.Fatalf("期望返回 ErrTopicLimitExceeded，实际为 %v", err)
	}
}

func TestWaitUntilConnectedReturnsErrorWhenManagerNotRunning(t *testing.T) {
	t.Parallel()

	conn := newFakeConn()
	manager := newTestManager(t, Options{
		Auth: &stubAuthState{
			snapshot: auth.Snapshot{AccessToken: "token-5"},
		},
		Dialer:       &fakeDialer{connections: []*fakeConn{conn}},
		ReadTimeout:  5 * time.Millisecond,
		PingInterval: time.Hour,
	})

	topic := MustNewTopic(CategoryUser, TopicDrops, 99, nil)
	if err := manager.AddTopics(topic); err != nil {
		t.Fatalf("AddTopics 返回错误: %v", err)
	}

	if err := manager.WaitUntilConnected(context.Background()); !errors.Is(err, ErrManagerNotRunning) {
		t.Fatalf("未启动时 WaitUntilConnected 应返回 ErrManagerNotRunning，实际为 %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatalf("Start 返回错误: %v", err)
	}

	conn.waitForType(t, "LISTEN", time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := manager.Stop(stopCtx, false); err != nil {
		t.Fatalf("Stop 返回错误: %v", err)
	}

	if err := manager.WaitUntilConnected(context.Background()); !errors.Is(err, ErrManagerNotRunning) {
		t.Fatalf("停止后 WaitUntilConnected 应返回 ErrManagerNotRunning，实际为 %v", err)
	}
}

func newTestManager(t *testing.T, options Options) *Manager {
	t.Helper()

	manager, err := NewManager(options)
	if err != nil {
		t.Fatalf("创建 Manager 失败: %v", err)
	}

	return manager
}

type stubAuthState struct {
	snapshot      auth.Snapshot
	validateErr   error
	validateCalls atomic.Int32
}

func (s *stubAuthState) Validate(context.Context) error {
	s.validateCalls.Add(1)
	return s.validateErr
}

func (s *stubAuthState) Snapshot() auth.Snapshot {
	return s.snapshot
}

type fakeDialer struct {
	mu          sync.Mutex
	connections []*fakeConn
	headers     []http.Header
}

func (d *fakeDialer) DialContext(_ context.Context, _ string, headers http.Header) (Connection, *http.Response, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.connections) == 0 {
		return nil, nil, errors.New("没有可用的 fake websocket 连接")
	}

	conn := d.connections[0]
	d.connections = d.connections[1:]
	d.headers = append(d.headers, headers.Clone())
	return conn, nil, nil
}

func (d *fakeDialer) CallCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.headers)
}

func (d *fakeDialer) Header(index int) http.Header {
	d.mu.Lock()
	defer d.mu.Unlock()

	if index < 0 || index >= len(d.headers) {
		return make(http.Header)
	}
	return d.headers[index].Clone()
}

type fakeConn struct {
	mu           sync.Mutex
	readDeadline time.Time
	incoming     chan fakeInbound
	outgoing     chan []byte
	closed       chan struct{}
	closeOnce    sync.Once
}

type fakeInbound struct {
	messageType int
	payload     []byte
	err         error
}

type errorOnceConn struct {
	err   error
	reads atomic.Int32
}

func (c *errorOnceConn) ReadMessage() (int, []byte, error) {
	c.reads.Add(1)
	return 0, nil, c.err
}

func (c *errorOnceConn) WriteJSON(any) error {
	return nil
}

func (c *errorOnceConn) Close() error {
	return nil
}

func (c *errorOnceConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *errorOnceConn) SetWriteDeadline(time.Time) error {
	return nil
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		incoming: make(chan fakeInbound, 32),
		outgoing: make(chan []byte, 32),
		closed:   make(chan struct{}),
	}
}

func (c *fakeConn) ReadMessage() (int, []byte, error) {
	c.mu.Lock()
	deadline := c.readDeadline
	c.mu.Unlock()

	var timer *time.Timer
	var timerCh <-chan time.Time
	if !deadline.IsZero() {
		waitFor := time.Until(deadline)
		if waitFor <= 0 {
			return 0, nil, fakeTimeoutError{}
		}
		timer = time.NewTimer(waitFor)
		timerCh = timer.C
	}
	defer stopTimer(timer)

	select {
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case inbound := <-c.incoming:
		return inbound.messageType, inbound.payload, inbound.err
	case <-timerCh:
		return 0, nil, fakeTimeoutError{}
	}
}

func (c *fakeConn) WriteJSON(value any) error {
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}

	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}

	select {
	case <-c.closed:
		return net.ErrClosed
	case c.outgoing <- payload:
		return nil
	}
}

func (c *fakeConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
	})
	return nil
}

func (c *fakeConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.readDeadline = deadline
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (c *fakeConn) pushText(t *testing.T, payload any) {
	t.Helper()

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("编码 fake websocket 消息失败: %v", err)
	}
	c.incoming <- fakeInbound{messageType: textMessageType, payload: encoded}
}

func (c *fakeConn) waitForType(t *testing.T, want string, timeout time.Duration) map[string]any {
	t.Helper()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		select {
		case raw := <-c.outgoing:
			var envelope map[string]any
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatalf("解析发送消息失败: %v", err)
			}
			if envelope["type"] == want {
				return envelope
			}
		case <-deadline.C:
			t.Fatalf("在 %s 内未收到 %s 消息", timeout, want)
		}
	}
}

func (c *fakeConn) waitClosed(t *testing.T, timeout time.Duration) {
	t.Helper()

	select {
	case <-c.closed:
	case <-time.After(timeout):
		t.Fatalf("连接在 %s 内未关闭", timeout)
	}
}

type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string   { return "timeout" }
func (fakeTimeoutError) Timeout() bool   { return true }
func (fakeTimeoutError) Temporary() bool { return true }

func topicsFromEnvelope(t *testing.T, envelope map[string]any) []string {
	t.Helper()

	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("消息 data 类型不匹配: %#v", envelope)
	}

	values, ok := data["topics"].([]any)
	if !ok {
		t.Fatalf("消息 topics 类型不匹配: %#v", data)
	}

	topics := make([]string, 0, len(values))
	for _, value := range values {
		topic, ok := value.(string)
		if !ok {
			t.Fatalf("topic 类型不匹配: %#v", value)
		}
		topics = append(topics, topic)
	}

	return topics
}

func assertAuthTokenAndNonce(t *testing.T, envelope map[string]any, wantToken string) {
	t.Helper()

	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("消息 data 类型不匹配: %#v", envelope)
	}
	if got := data["auth_token"]; got != wantToken {
		t.Fatalf("auth_token 不匹配: %#v", data)
	}

	nonce, ok := envelope["nonce"].(string)
	if !ok || len(nonce) != defaultNonceLength {
		t.Fatalf("nonce 不匹配: %#v", envelope)
	}
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("在 %s 内条件未满足", timeout)
}
