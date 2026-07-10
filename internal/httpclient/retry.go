package httpclient

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

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

func (c *Client) shouldInvalidate(invalidateAfter time.Time) bool {
	if invalidateAfter.IsZero() {
		return false
	}

	return !c.now().UTC().Add(c.requestTimeout).Before(invalidateAfter.UTC())
}

func (c *Client) retryExhausted(attempt int) bool {
	if c == nil || c.maxAttempts <= 0 {
		return false
	}

	return attempt >= c.maxAttempts
}

func isRetryableRequestError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var certificateInvalidError x509.CertificateInvalidError
	if errors.As(err, &certificateInvalidError) {
		return false
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return false
	}
	var unknownAuthorityError x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityError) {
		return false
	}

	var netError net.Error
	return errors.As(err, &netError)
}

func readResponse(httpResponse *http.Response) (Response, error) {
	if httpResponse == nil {
		return Response{}, fmt.Errorf("响应为空")
	}
	defer func() {
		_ = httpResponse.Body.Close()
	}()

	body, err := io.ReadAll(httpResponse.Body)
	if err != nil {
		return Response{}, err
	}

	return Response{
		StatusCode: httpResponse.StatusCode,
		Header:     httpResponse.Header.Clone(),
		Body:       body,
	}, nil
}

func (c *Client) logRetry(method string, rawURL string, attempt int, delay time.Duration, statusCode int, err error) {
	if c == nil || c.logger == nil {
		return
	}

	attrs := []any{
		"method", method,
		"url", sanitizeURL(rawURL),
		"attempt", attempt,
		"retry_in", delay.String(),
	}
	if statusCode > 0 {
		attrs = append(attrs, "status_code", statusCode)
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}

	c.logger.Warn("HTTP 请求失败，准备退避重试", attrs...)
}

func sanitizeURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return rawURL
	}

	sanitized := parsedURL.Scheme + "://" + parsedURL.Host + parsedURL.EscapedPath()
	if sanitized == "" {
		return rawURL
	}
	return sanitized
}
