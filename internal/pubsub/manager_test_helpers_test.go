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
