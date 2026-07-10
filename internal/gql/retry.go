package gql

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

func DefaultBackoffConfig() httpclient.BackoffConfig {
	config := httpclient.DefaultBackoffConfig()
	config.Maximum = defaultBackoffMax
	return config
}

func retryAfterDelay(header http.Header) time.Duration {
	value := header.Get("Retry-After")
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

func retryExhaustedError(responses []Response, attempts int) error {
	var responseErrors []ResponseError
	operation := ""
	for index := range responses {
		if operation == "" {
			operation = operationName(responses[index])
		}
		if len(responses[index].Errors) > 0 {
			responseErrors = responses[index].Errors
			break
		}
	}
	return &RequestError{
		Operation: operation,
		Message:   fmt.Sprintf("GQL 请求重试 %d 次仍未成功", attempts),
		Errors:    append([]ResponseError(nil), responseErrors...),
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
