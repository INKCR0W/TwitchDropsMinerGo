package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
)

type App struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *App {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &App{
		logger: logger,
	}
}

func (a *App) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("运行上下文不能为空")
	}

	a.logger.Info("服务启动")

	<-ctx.Done()

	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	a.logger.Info("服务停止")
	return nil
}
