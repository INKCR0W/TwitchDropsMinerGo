package pubsub

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type gorillaDialer struct {
	dialer websocket.Dialer
}

type gorillaConnection struct {
	conn *websocket.Conn
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
