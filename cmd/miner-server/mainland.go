package main

import (
	"context"
	"log/slog"
	"net"
	"strings"

	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/mainland"
)

// newMainlandDialer 在设置开启时构造大陆模式 dialer, 关闭时返回 nil
func newMainlandDialer(settings config.Settings, logger *slog.Logger) *mainland.Dialer {
	if !settings.MainlandEnabled() {
		return nil
	}
	if strings.TrimSpace(settings.Proxy) != "" {
		logger.Warn("大陆模式已开启, 配置的代理将被忽略")
	}
	logger.Info("大陆模式已开启: 通过 DoH + 良性 SNI 直连 Twitch")
	return mainland.New(logger, settings.ConnectionQuality)
}

// mainlandDialerTLS 保证 dialer 为 nil 时返回 nil 函数值, 而非包裹 nil 接收者的非 nil 函数
func mainlandDialerTLS(d *mainland.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if d == nil {
		return nil
	}
	return d.DialTLSContext
}
