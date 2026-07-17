package pubsub

import (
	"context"
	"net"
	"testing"
)

func TestNewGorillaDialerWiresNetDialTLS(t *testing.T) {
	t.Parallel()
	called := make(chan struct{}, 1)
	stub := func(ctx context.Context, network, addr string) (net.Conn, error) {
		select {
		case called <- struct{}{}:
		default:
		}
		return nil, context.Canceled
	}
	d, err := newGorillaDialer("", stub)
	if err != nil {
		t.Fatal(err)
	}
	// 触发一次拨号, gorilla 应经由 NetDialTLSContext.
	_, _, _ = d.DialContext(context.Background(), "wss://pubsub-edge.twitch.tv/v1", nil)
	select {
	case <-called:
	default:
		t.Fatal("NetDialTLSContext 未被 gorilla 使用")
	}
}

func TestNewGorillaDialerNilNetDialTLSStillWorks(t *testing.T) {
	t.Parallel()
	if _, err := newGorillaDialer("", nil); err != nil {
		t.Fatalf("nil netDialTLS 应可构造: %v", err)
	}
}
