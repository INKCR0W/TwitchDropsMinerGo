package gql

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

const (
	defaultEndpoint    = "https://gql.twitch.tv/gql"
	forcedRetryDelay   = 5 * time.Second
	defaultBackoffMax  = 60 * time.Second
	defaultMaxAttempts = 5
)

type Limiter interface {
	Wait(context.Context) error
}

type HeadersProvider func(context.Context) (http.Header, error)

type ClientOptions struct {
	HTTPClient      *httpclient.Client
	ClientInfo      httpclient.ClientInfo
	HeadersProvider HeadersProvider
	Limiter         Limiter
	Endpoint        string
	Backoff         httpclient.BackoffConfig
	Sleep           func(context.Context, time.Duration) error
	Logger          *slog.Logger
	MaxAttempts     int
}

type Client struct {
	httpClient      *httpclient.Client
	clientInfo      httpclient.ClientInfo
	headersProvider HeadersProvider
	limiter         Limiter
	endpoint        string
	backoff         httpclient.BackoffConfig
	sleep           func(context.Context, time.Duration) error
	logger          *slog.Logger
	maxAttempts     int
}

type Response struct {
	Data       any             `json:"data,omitempty"`
	Errors     []ResponseError `json:"errors,omitempty"`
	Error      string          `json:"error,omitempty"`
	Message    string          `json:"message,omitempty"`
	Extensions map[string]any  `json:"extensions,omitempty"`
}

type ResponseError struct {
	Message string `json:"message"`
	Path    []any  `json:"path,omitempty"`
}

type RequestError struct {
	Operation string
	Message   string
	Errors    []ResponseError
}

func NewClient(options ClientOptions) (*Client, error) {
	if options.HTTPClient == nil {
		return nil, fmt.Errorf("GQL HTTP 客户端不能为空")
	}

	clientInfo := options.ClientInfo
	if clientInfo == (httpclient.ClientInfo{}) {
		clientInfo = httpclient.AndroidAppClient
	}

	headersProvider := options.HeadersProvider
	if headersProvider == nil {
		headersProvider = func(context.Context) (http.Header, error) {
			return make(http.Header), nil
		}
	}

	limiter := options.Limiter
	if limiter == nil {
		limiter = httpclient.NewGQLLimiter()
	}

	endpoint := options.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	backoff := options.Backoff
	if backoff == (httpclient.BackoffConfig{}) {
		backoff = DefaultBackoffConfig()
	}
	if _, err := httpclient.NewExponentialBackoff(backoff); err != nil {
		return nil, err
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	maxAttempts := options.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}

	return &Client{
		httpClient:      options.HTTPClient,
		clientInfo:      clientInfo,
		headersProvider: headersProvider,
		limiter:         limiter,
		endpoint:        endpoint,
		backoff:         backoff,
		sleep:           sleep,
		logger:          logger,
		maxAttempts:     maxAttempts,
	}, nil
}

func (c *Client) Do(ctx context.Context, operation Operation) (Response, error) {
	responses, err := c.do(ctx, operation, true)
	if err != nil {
		return Response{}, err
	}

	return responses[0], nil
}

func (c *Client) DoRaw(ctx context.Context, query RawQuery) (Response, error) {
	responses, err := c.do(ctx, query, true)
	if err != nil {
		return Response{}, err
	}

	return responses[0], nil
}

func (c *Client) DoBatch(ctx context.Context, operations []Operation) ([]Response, error) {
	if len(operations) == 0 {
		return []Response{}, nil
	}

	return c.do(ctx, operations, false)
}

func (c *Client) do(ctx context.Context, payload any, single bool) ([]Response, error) {
	if c == nil {
		return nil, fmt.Errorf("GQL 客户端未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var operations []Operation
	if !single {
		var ok bool
		operations, ok = payload.([]Operation)
		if !ok {
			return nil, fmt.Errorf("GQL batch 请求类型不正确: %T", payload)
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化 GQL 请求失败: %w", err)
	}

	backoff, err := httpclient.NewExponentialBackoff(c.backoff)
	if err != nil {
		return nil, err
	}

	allowSingleRetry := true
	attempt := 0
	for {
		attempt++
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}

		headers, err := c.buildHeaders(ctx)
		if err != nil {
			return nil, err
		}

		response, err := c.httpClient.Do(ctx, httpclient.Request{
			Method:  http.MethodPost,
			URL:     c.endpoint,
			Headers: headers,
			Body:    body,
		})
		if err != nil {
			return nil, err
		}

		if response.StatusCode == http.StatusTooManyRequests {
			if attempt >= c.maxAttempts {
				return nil, &RequestError{Message: fmt.Sprintf("GQL 请求被限流(429)，重试 %d 次仍失败", c.maxAttempts)}
			}
			delay := backoff.Next()
			if retryAfter := retryAfterDelay(response.Header); retryAfter > delay {
				delay = retryAfter
			}
			if err := c.sleep(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}

		var responses []Response
		if single {
			var parsed Response
			if err := parsed.decode(response.Body); err != nil {
				return nil, err
			}
			responses = []Response{parsed}
		} else {
			if err := json.Unmarshal(response.Body, &responses); err != nil {
				return nil, fmt.Errorf("解析 GQL 响应失败: %w", err)
			}
			if err := validateBatchResponses(operations, responses); err != nil {
				return nil, err
			}
		}

		retry, consumeSingleRetry, minimumDelay, err := c.handleResponses(responses, allowSingleRetry)
		if err != nil {
			return nil, err
		}
		if !retry {
			return responses, nil
		}
		if consumeSingleRetry {
			allowSingleRetry = false
		}
		if attempt >= c.maxAttempts {
			return nil, retryExhaustedError(responses, c.maxAttempts)
		}

		delay := backoff.Next()
		if minimumDelay > delay {
			delay = minimumDelay
		}
		if err := c.sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (c *Client) buildHeaders(ctx context.Context) (http.Header, error) {
	headers := make(http.Header)
	headers.Set("Accept", "*/*")
	// 交给 net/http 自动协商并解压 gzip，手动设置会关闭透明解压。
	headers.Set("Accept-Language", "en-US")
	headers.Set("Pragma", "no-cache")
	headers.Set("Cache-Control", "no-cache")
	if c.clientInfo.ClientID != "" {
		headers.Set("Client-Id", c.clientInfo.ClientID)
	}
	if c.clientInfo.ClientURL != "" {
		headers.Set("Origin", c.clientInfo.ClientURL)
		headers.Set("Referer", c.clientInfo.ClientURL)
	}
	if c.clientInfo.UserAgent != "" {
		headers.Set("User-Agent", c.clientInfo.UserAgent)
	}

	extraHeaders, err := c.headersProvider(ctx)
	if err != nil {
		return nil, fmt.Errorf("构造 GQL 请求头失败: %w", err)
	}
	for key, values := range extraHeaders {
		headers.Del(key)
		for _, value := range values {
			headers.Add(key, value)
		}
	}

	return headers, nil
}
