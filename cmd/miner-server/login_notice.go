package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"twitchdropsminergo/internal/auth"
)

func deviceCodeNotifier(logger *slog.Logger, out io.Writer, pendingLoginFile string) auth.DeviceCodeHandler {
	return func(_ context.Context, deviceCode auth.DeviceCode) error {
		logger.Info(
			"需要完成 Twitch Device Code 授权",
			"user_code", deviceCode.UserCode,
			"verification_uri", deviceCode.VerificationURI,
			"expires_at", deviceCode.ExpiresAt.UTC(),
			"interval", deviceCode.Interval.String(),
		)

		notice := loginNotice(deviceCode)
		fmt.Fprint(out, notice)
		// 写文件失败不阻断登录, banner 与日志仍可见
		if err := os.WriteFile(pendingLoginFile, []byte(notice), 0o644); err != nil {
			logger.Warn("写入待登录提示文件失败", "path", pendingLoginFile, "error", err)
		}
		return nil
	}
}

func clearPendingLogin(logger *slog.Logger, pendingLoginFile string) func() {
	return func() {
		err := os.Remove(pendingLoginFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.Warn("删除待登录提示文件失败", "path", pendingLoginFile, "error", err)
		}
	}
}

func loginNotice(deviceCode auth.DeviceCode) string {
	return fmt.Sprintf(`
==================================================
  需要登录 Twitch
  请在任意设备的浏览器打开: %s
  输入代码: %s
  有效期至: %s
==================================================
`,
		deviceCode.VerificationURI,
		deviceCode.UserCode,
		deviceCode.ExpiresAt.Local().Format("2006-01-02 15:04:05 MST"),
	)
}
