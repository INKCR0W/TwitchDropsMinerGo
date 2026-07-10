package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const defaultRuntimeShutdownTimeout = 10 * time.Second

var errRuntimeShutdownTimeout = errors.New("运行组件退出超时")

type runner interface {
	Run(context.Context) error
}

type namedRunner struct {
	name   string
	runner runner
}

type runtimeResult struct {
	name string
	err  error
}

func runService(ctx context.Context, stop context.CancelFunc, workers ...namedRunner) error {
	return runServiceWithTimeout(ctx, stop, defaultRuntimeShutdownTimeout, workers...)
}

func runServiceWithTimeout(ctx context.Context, stop context.CancelFunc, shutdownTimeout time.Duration, workers ...namedRunner) error {
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultRuntimeShutdownTimeout
	}

	activeWorkers := make([]namedRunner, 0, len(workers))
	for _, worker := range workers {
		if worker.runner == nil {
			continue
		}
		if strings.TrimSpace(worker.name) == "" {
			worker.name = "未命名组件"
		}
		activeWorkers = append(activeWorkers, worker)
	}
	if len(activeWorkers) == 0 {
		return nil
	}

	resultCh := make(chan runtimeResult, len(activeWorkers))
	for _, worker := range activeWorkers {
		go func(worker namedRunner) {
			defer func() {
				if r := recover(); r != nil {
					resultCh <- runtimeResult{name: worker.name, err: fmt.Errorf("组件 %s 崩溃: %v", worker.name, r)}
				}
			}()
			resultCh <- runtimeResult{name: worker.name, err: worker.runner.Run(ctx)}
		}(worker)
	}

	results := make(map[string]error, len(activeWorkers))
	pending := make(map[string]struct{}, len(activeWorkers))
	for _, worker := range activeWorkers {
		pending[worker.name] = struct{}{}
	}

	select {
	case result := <-resultCh:
		results[result.name] = result.err
		delete(pending, result.name)
	case <-ctx.Done():
	}

	if stop != nil {
		stop()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	for len(pending) > 0 {
		select {
		case result := <-resultCh:
			if _, exists := pending[result.name]; !exists {
				continue
			}
			results[result.name] = result.err
			delete(pending, result.name)
		case <-shutdownCtx.Done():
			for name := range pending {
				results[name] = fmt.Errorf("%w: %s", errRuntimeShutdownTimeout, name)
			}
			pending = map[string]struct{}{}
		}
	}

	orderedErrors := make([]error, 0, len(activeWorkers))
	for _, worker := range activeWorkers {
		orderedErrors = append(orderedErrors, results[worker.name])
	}
	return firstRuntimeError(orderedErrors...)
}

func firstRuntimeError(errorsToCheck ...error) error {
	for _, err := range errorsToCheck {
		if err == nil {
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		return err
	}
	return nil
}
