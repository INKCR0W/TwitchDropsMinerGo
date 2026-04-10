package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"twitchdropsminergo/internal/app"
)

func main() {
	os.Exit(run())
}

func run() int {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application := app.New(logger)
	if err := application.Run(ctx); err != nil {
		logger.Error("服务退出失败", "error", err)
		return 1
	}

	return 0
}
