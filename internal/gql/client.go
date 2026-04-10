package gql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

const (
	defaultEndpoint   = "https://gql.twitch.tv/gql"
	forcedRetryDelay  = 5 * time.Second
	defaultBackoffMax = 60 * time.Second
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
}

type Client struct {
	httpClient      *httpclient.Client
	clientInfo      httpclient.ClientInfo
	headersProvider HeadersProvider
	limiter         Limiter
	endpoint        string
	backoff         httpclient.BackoffConfig
	sleep           func(context.Context, time.Duration) error
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

func (e *RequestError) Error() string {
	if e == nil {
		return ""
	}
	if e.Operation != "" {
		return fmt.Sprintf("%s: %s", e.Operation, e.Message)
	}

	return e.Message
}

func DefaultBackoffConfig() httpclient.BackoffConfig {
	config := httpclient.DefaultBackoffConfig()
	config.Maximum = defaultBackoffMax
	return config
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

	return &Client{
		httpClient:      options.HTTPClient,
		clientInfo:      clientInfo,
		headersProvider: headersProvider,
		limiter:         limiter,
		endpoint:        endpoint,
		backoff:         backoff,
		sleep:           sleep,
	}, nil
}

func (c *Client) Do(ctx context.Context, operation Operation) (Response, error) {
	responses, err := c.do(ctx, operation, true)
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

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化 GQL 请求失败: %w", err)
	}

	backoff, err := httpclient.NewExponentialBackoff(c.backoff)
	if err != nil {
		return nil, err
	}

	allowSingleRetry := true
	for {
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
		}

		retry, consumeSingleRetry, minimumDelay, err := handleResponses(responses, allowSingleRetry)
		if err != nil {
			return nil, err
		}
		if !retry {
			return responses, nil
		}
		if consumeSingleRetry {
			allowSingleRetry = false
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

func handleResponses(responses []Response, allowSingleRetry bool) (retry bool, consumeSingleRetry bool, minimumDelay time.Duration, err error) {
	for index := range responses {
		response := &responses[index]
		if len(response.Errors) > 0 {
			handled := false
			for _, responseError := range response.Errors {
				switch responseError.Message {
				case "service error", "PersistedQueryNotFound":
					if allowSingleRetry {
						return true, true, forcedRetryDelay, nil
					}
				case "server error":
					if err := nullifyPath(response, responseError.Path); err != nil {
						return false, false, 0, err
					}
					handled = true
				case "service timeout", "service unavailable", "context deadline exceeded":
					return true, false, 0, nil
				}
				if handled {
					break
				}
			}
			if !handled {
				return false, false, 0, newRequestError(*response)
			}
		}
		if response.Error != "" {
			return false, false, 0, &RequestError{
				Operation: operationName(*response),
				Message:   fmt.Sprintf("%s: %s", response.Error, response.Message),
			}
		}
	}

	return false, false, 0, nil
}

func newRequestError(response Response) error {
	message := "GQL 请求返回错误"
	if len(response.Errors) > 0 {
		message = response.Errors[0].Message
	}

	return &RequestError{
		Operation: operationName(response),
		Message:   message,
		Errors:    append([]ResponseError(nil), response.Errors...),
	}
}

func operationName(response Response) string {
	if response.Extensions == nil {
		return ""
	}

	value, ok := response.Extensions["operationName"]
	if !ok {
		return ""
	}

	name, _ := value.(string)
	return name
}

func (r *Response) decode(body []byte) error {
	if err := json.Unmarshal(body, r); err != nil {
		return fmt.Errorf("解析 GQL 响应失败: %w", err)
	}

	return nil
}

func nullifyPath(response *Response, path []any) error {
	if response == nil {
		return fmt.Errorf("GQL 响应为空")
	}
	if len(path) == 0 {
		return fmt.Errorf("server error 缺少错误路径")
	}

	current := response.Data
	for _, step := range path[:len(path)-1] {
		next, err := navigate(current, step)
		if err != nil {
			return err
		}
		current = next
	}

	return assignNil(current, path[len(path)-1])
}

func navigate(current any, step any) (any, error) {
	switch typed := current.(type) {
	case map[string]any:
		key, ok := step.(string)
		if !ok {
			return nil, fmt.Errorf("错误路径包含非法对象键: %v", step)
		}
		next, ok := typed[key]
		if !ok {
			return nil, fmt.Errorf("错误路径中的对象键不存在: %s", key)
		}
		return next, nil
	case []any:
		index, ok := pathIndex(step)
		if !ok {
			return nil, fmt.Errorf("错误路径包含非法数组索引: %v", step)
		}
		if index < 0 || index >= len(typed) {
			return nil, fmt.Errorf("错误路径中的数组索引越界: %d", index)
		}
		return typed[index], nil
	default:
		return nil, fmt.Errorf("错误路径无法继续下钻，当前节点类型为 %T", current)
	}
}

func assignNil(current any, step any) error {
	switch typed := current.(type) {
	case map[string]any:
		key, ok := step.(string)
		if !ok {
			return fmt.Errorf("错误路径包含非法对象键: %v", step)
		}
		typed[key] = nil
		return nil
	case []any:
		index, ok := pathIndex(step)
		if !ok {
			return fmt.Errorf("错误路径包含非法数组索引: %v", step)
		}
		if index < 0 || index >= len(typed) {
			return fmt.Errorf("错误路径中的数组索引越界: %d", index)
		}
		typed[index] = nil
		return nil
	default:
		return fmt.Errorf("错误路径无法写入 nil，当前节点类型为 %T", current)
	}
}

func pathIndex(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		if math.Trunc(typed) != typed {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
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

func IsRequestError(err error) bool {
	var requestError *RequestError
	return errors.As(err, &requestError)
}
