package httpclient

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"twitchdropsminergo/internal/config"
)

var ErrRequestInvalid = errors.New("请求已失效")

type ClientInfo struct {
	ClientURL string
	ClientID  string
	UserAgent string
}

var AndroidAppClient = ClientInfo{
	ClientURL: "https://www.twitch.tv",
	ClientID:  "kd1unb4b3q4t58fwlpcbzcbnm76a8fp",
	UserAgent: "Dalvik/2.1.0 (Linux; U; Android 16; SM-S911B Build/TP1A.220624.014) tv.twitch.android.app/25.3.0/2503006",
}

type Options struct {
	Settings    config.Settings
	CookiesPath string
	ClientInfo  ClientInfo
	Backoff     BackoffConfig
	Clock       func() time.Time
	Sleep       func(context.Context, time.Duration) error
}

type Client struct {
	httpClient     *http.Client
	jar            *PersistentJar
	info           ClientInfo
	connectTimeout time.Duration
	requestTimeout time.Duration
	backoff        BackoffConfig
	now            func() time.Time
	sleep          func(context.Context, time.Duration) error
}

type Request struct {
	Method          string
	URL             string
	Headers         http.Header
	Body            []byte
	InvalidateAfter time.Time
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func (r Response) DecodeJSON(target any) error {
	if err := json.Unmarshal(r.Body, target); err != nil {
		return fmt.Errorf("解析响应 JSON 失败: %w", err)
	}

	return nil
}

func (r Response) Text() string {
	return string(r.Body)
}

func New(options Options) (*Client, error) {
	now := options.Clock
	if now == nil {
		now = time.Now
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	info := options.ClientInfo
	if info == (ClientInfo{}) {
		info = AndroidAppClient
	}

	backoff := options.Backoff
	if backoff == (BackoffConfig{}) {
		backoff = DefaultBackoffConfig()
	}
	if _, err := NewExponentialBackoff(backoff); err != nil {
		return nil, err
	}

	quality := clampConnectionQuality(options.Settings.ConnectionQuality)
	connectTimeout := time.Duration(5*quality) * time.Second
	requestTimeout := time.Duration(10*quality) * time.Second

	jar, err := NewPersistentJar(options.CookiesPath)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{
		Timeout:   connectTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	transport.MaxIdleConns = 50
	transport.MaxIdleConnsPerHost = 50
	transport.MaxConnsPerHost = 50

	if proxy := strings.TrimSpace(options.Settings.Proxy); proxy != "" {
		proxyURL, parseErr := url.Parse(proxy)
		if parseErr != nil {
			return nil, fmt.Errorf("解析代理地址失败: %w", parseErr)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}

	httpClient := &http.Client{
		Timeout:   requestTimeout,
		Transport: transport,
		Jar:       jar,
	}

	return &Client{
		httpClient:     httpClient,
		jar:            jar,
		info:           info,
		connectTimeout: connectTimeout,
		requestTimeout: requestTimeout,
		backoff:        backoff,
		now:            now,
		sleep:          sleep,
	}, nil
}

func (c *Client) Close() error {
	if c == nil {
		return nil
	}

	if transport, ok := c.httpClient.Transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}

	return c.jar.Save()
}

func (c *Client) Do(ctx context.Context, request Request) (Response, error) {
	if c == nil {
		return Response{}, fmt.Errorf("HTTP 客户端未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodGet
	}

	headers := request.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	if headers.Get("User-Agent") == "" && c.info.UserAgent != "" {
		headers.Set("User-Agent", c.info.UserAgent)
	}

	backoff, err := NewExponentialBackoff(c.backoff)
	if err != nil {
		return Response{}, err
	}

	for {
		if c.shouldInvalidate(request.InvalidateAfter) {
			return Response{}, ErrRequestInvalid
		}

		httpRequest, err := http.NewRequestWithContext(ctx, method, request.URL, bytes.NewReader(request.Body))
		if err != nil {
			return Response{}, fmt.Errorf("创建请求失败: %w", err)
		}
		httpRequest.Header = headers.Clone()

		httpResponse, err := c.httpClient.Do(httpRequest)
		if err != nil {
			if !isRetryableRequestError(err) {
				return Response{}, fmt.Errorf("请求失败: %w", err)
			}
		} else {
			response, readErr := readResponse(httpResponse)
			if readErr == nil {
				if response.StatusCode < 500 {
					return response, nil
				}
			} else if !isRetryableRequestError(readErr) {
				return Response{}, fmt.Errorf("读取响应失败: %w", readErr)
			}
		}

		if err := c.sleep(ctx, backoff.Next()); err != nil {
			return Response{}, err
		}
	}
}

func (c *Client) CookieJar() *PersistentJar {
	if c == nil {
		return nil
	}

	return c.jar
}

func (c *Client) ConnectTimeout() time.Duration {
	if c == nil {
		return 0
	}

	return c.connectTimeout
}

func (c *Client) RequestTimeout() time.Duration {
	if c == nil {
		return 0
	}

	return c.requestTimeout
}

func clampConnectionQuality(value int) int {
	switch {
	case value < 1:
		return 1
	case value > 6:
		return 6
	default:
		return value
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

func (c *Client) shouldInvalidate(invalidateAfter time.Time) bool {
	if invalidateAfter.IsZero() {
		return false
	}

	return !c.now().UTC().Add(c.requestTimeout).Before(invalidateAfter.UTC())
}

func isRetryableRequestError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
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
	defer httpResponse.Body.Close()

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
