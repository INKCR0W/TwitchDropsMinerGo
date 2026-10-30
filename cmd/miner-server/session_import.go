package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/runtime"
)

func runSessionImport(layout runtime.Layout, settings config.Settings, logger *slog.Logger, path string, stdin io.Reader, stdout io.Writer) int {
	input := stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			logger.Error("打开会话文件失败", "error", err)
			return 1
		}
		defer func() {
			if err := file.Close(); err != nil {
				logger.Warn("关闭会话文件失败", "error", err)
			}
		}()
		input = file
	}
	session, err := auth.ReadSession(input)
	if err != nil {
		logger.Error("读取会话失败", "error", err)
		return 1
	}
	dialer := newMainlandDialer(settings, logger)
	client, err := httpclient.New(httpclient.Options{
		Settings: settings, CookiesPath: layout.CookiesFile,
		ClientInfo: httpclient.AndroidAppClient, DialTLSContext: mainlandDialerTLS(dialer),
	})
	if err != nil {
		logger.Error("初始化会话导入客户端失败", "error", err)
		return 1
	}
	defer func() {
		if err := client.Close(); err != nil {
			logger.Warn("关闭会话导入客户端失败", "error", err)
		}
	}()
	state, err := auth.New(auth.Options{HTTPClient: client, ClientInfo: httpclient.AndroidAppClient})
	if err != nil {
		logger.Error("初始化会话导入认证失败", "error", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := state.ImportSession(ctx, session); err != nil {
		logger.Error("导入会话失败", "error", err)
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "Android 会话已导入，user_id=%d\n", state.Snapshot().UserID); err != nil {
		logger.Error("输出会话导入结果失败", "error", err)
		return 1
	}
	return 0
}
