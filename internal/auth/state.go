package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

const (
	defaultDeviceEndpoint   = "https://id.twitch.tv/oauth2/device"
	defaultTokenEndpoint    = "https://id.twitch.tv/oauth2/token"
	defaultValidateEndpoint = "https://id.twitch.tv/oauth2/validate"
	deviceGrantType         = "urn:ietf:params:oauth:grant-type:device_code"
	sessionIDLength         = 16
)

type HeadersOptions struct {
	UserAgent string
	GQL       bool
}

type Snapshot struct {
	UserID        int64
	DeviceID      string
	SessionID     string
	AccessToken   string
	ClientVersion string
}

type DeviceCode struct {
	UserCode        string
	VerificationURI string
	Interval        time.Duration
	ExpiresAt       time.Time
}

type DeviceCodeHandler func(context.Context, DeviceCode) error

type Options struct {
	HTTPClient         *httpclient.Client
	ClientInfo         httpclient.ClientInfo
	DeviceEndpoint     string
	TokenEndpoint      string
	ValidateEndpoint   string
	Clock              func() time.Time
	Sleep              func(context.Context, time.Duration) error
	SessionIDGenerator func() (string, error)
	DeviceCodeHandler  DeviceCodeHandler
}

type State struct {
	httpClient       *httpclient.Client
	clientInfo       httpclient.ClientInfo
	clientURL        *url.URL
	deviceEndpoint   string
	tokenEndpoint    string
	validateEndpoint string
	now              func() time.Time
	sleep            func(context.Context, time.Duration) error
	generateSession  func() (string, error)
	onDeviceCode     DeviceCodeHandler

	mu            sync.Mutex
	userID        int64
	deviceID      string
	sessionID     string
	accessToken   string
	clientVersion string
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	Interval        int    `json:"interval"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

type validateResponse struct {
	ClientID string `json:"client_id"`
	UserID   string `json:"user_id"`
}

type tokenErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func New(options Options) (*State, error) {
	if options.HTTPClient == nil {
		return nil, fmt.Errorf("认证 HTTP 客户端不能为空")
	}

	clientInfo := options.ClientInfo
	if clientInfo == (httpclient.ClientInfo{}) {
		clientInfo = httpclient.AndroidAppClient
	}
	if strings.TrimSpace(clientInfo.ClientURL) == "" {
		return nil, fmt.Errorf("认证客户端 URL 不能为空")
	}
	clientURL, err := url.Parse(clientInfo.ClientURL)
	if err != nil {
		return nil, fmt.Errorf("解析认证客户端 URL 失败: %w", err)
	}
	if clientURL.Scheme == "" || clientURL.Host == "" {
		return nil, fmt.Errorf("认证客户端 URL 必须包含协议和主机")
	}

	now := options.Clock
	if now == nil {
		now = time.Now
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	generateSession := options.SessionIDGenerator
	if generateSession == nil {
		generateSession = generateSessionID
	}

	deviceEndpoint := strings.TrimSpace(options.DeviceEndpoint)
	if deviceEndpoint == "" {
		deviceEndpoint = defaultDeviceEndpoint
	}

	tokenEndpoint := strings.TrimSpace(options.TokenEndpoint)
	if tokenEndpoint == "" {
		tokenEndpoint = defaultTokenEndpoint
	}

	validateEndpoint := strings.TrimSpace(options.ValidateEndpoint)
	if validateEndpoint == "" {
		validateEndpoint = defaultValidateEndpoint
	}

	return &State{
		httpClient:       options.HTTPClient,
		clientInfo:       clientInfo,
		clientURL:        clientURL,
		deviceEndpoint:   deviceEndpoint,
		tokenEndpoint:    tokenEndpoint,
		validateEndpoint: validateEndpoint,
		now:              now,
		sleep:            sleep,
		generateSession:  generateSession,
		onDeviceCode:     options.DeviceCodeHandler,
	}, nil
}

func (s *State) Validate(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("认证状态未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessionID == "" {
		sessionID, err := s.generateSession()
		if err != nil {
			return fmt.Errorf("生成 session_id 失败: %w", err)
		}
		if sessionID == "" {
			return fmt.Errorf("生成 session_id 失败: 结果为空")
		}
		s.sessionID = sessionID
	}

	if s.deviceID == "" {
		if err := s.ensureDeviceID(ctx); err != nil {
			return err
		}
	}

	if s.accessToken != "" && s.userID != 0 {
		return nil
	}

	return s.ensureAuthenticated(ctx)
}

func (s *State) Headers(options HeadersOptions) http.Header {
	if s == nil {
		return make(http.Header)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headersLocked(options)
}

func (s *State) HeadersProvider(options HeadersOptions) func(context.Context) (http.Header, error) {
	return func(ctx context.Context) (http.Header, error) {
		if err := s.Validate(ctx); err != nil {
			return nil, err
		}
		return s.Headers(options), nil
	}
}

func (s *State) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return Snapshot{
		UserID:        s.userID,
		DeviceID:      s.deviceID,
		SessionID:     s.sessionID,
		AccessToken:   s.accessToken,
		ClientVersion: s.clientVersion,
	}
}

func (s *State) Invalidate() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessToken = ""
}

func (s *State) Clear() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.userID = 0
	s.deviceID = ""
	s.sessionID = ""
	s.accessToken = ""
	s.clientVersion = ""
}

func (s *State) ensureDeviceID(ctx context.Context) error {
	_, err := s.httpClient.Do(ctx, httpclient.Request{
		Method:  http.MethodGet,
		URL:     s.clientURL.String(),
		Headers: s.headersLocked(HeadersOptions{}),
	})
	if err != nil {
		return fmt.Errorf("获取 device_id 失败: %w", err)
	}

	deviceID := s.cookieValueLocked("unique_id")
	if deviceID == "" {
		return fmt.Errorf("获取 device_id 失败: 响应中缺少 unique_id cookie")
	}

	s.deviceID = deviceID
	return nil
}

func (s *State) ensureAuthenticated(ctx context.Context) error {
	var validated validateResponse

	for clientMismatchAttempt := 0; clientMismatchAttempt < 2; clientMismatchAttempt++ {
		for invalidTokenAttempt := 0; invalidTokenAttempt < 2; invalidTokenAttempt++ {
			if s.accessToken == "" {
				accessToken, err := s.restoreAccessTokenLocked(ctx)
				if err != nil {
					return err
				}
				s.accessToken = accessToken
			}

			response, statusCode, err := s.validateAccessToken(ctx)
			if err != nil {
				return err
			}
			if statusCode == http.StatusUnauthorized {
				s.accessToken = ""
				s.userID = 0
				if err := s.httpClient.CookieJar().ClearDomain(s.clientURL.Hostname()); err != nil {
					return fmt.Errorf("清理失效认证 Cookie 失败: %w", err)
				}
				if err := s.httpClient.CookieJar().Save(); err != nil {
					return fmt.Errorf("保存认证 Cookie 失败: %w", err)
				}
				continue
			}
			if statusCode != http.StatusOK {
				return fmt.Errorf("验证 access token 失败: 状态码 %d", statusCode)
			}

			validated = response
			break
		}

		if validated.ClientID == s.clientInfo.ClientID {
			break
		}
		if validated.ClientID == "" {
			continue
		}

		s.accessToken = ""
		s.userID = 0
		if err := s.httpClient.CookieJar().Clear(); err != nil {
			return fmt.Errorf("清空认证 Cookie 失败: %w", err)
		}
		if err := s.httpClient.CookieJar().Save(); err != nil {
			return fmt.Errorf("保存认证 Cookie 失败: %w", err)
		}
		validated = validateResponse{}
	}

	if validated.ClientID != s.clientInfo.ClientID {
		return fmt.Errorf("验证 access token 失败: client_id 不匹配")
	}
	if strings.TrimSpace(validated.UserID) == "" {
		return fmt.Errorf("验证 access token 失败: 缺少 user_id")
	}

	userID, err := strconv.ParseInt(validated.UserID, 10, 64)
	if err != nil {
		return fmt.Errorf("解析 user_id 失败: %w", err)
	}

	s.userID = userID
	s.updatePersistentCookiesLocked(validated.UserID)
	if err := s.httpClient.CookieJar().Save(); err != nil {
		return fmt.Errorf("保存认证 Cookie 失败: %w", err)
	}

	return nil
}

func (s *State) restoreAccessTokenLocked(ctx context.Context) (string, error) {
	if accessToken := s.cookieValueLocked("auth-token"); accessToken != "" {
		return accessToken, nil
	}

	accessToken, err := s.oauthLoginLocked(ctx)
	if err != nil {
		return "", err
	}

	return accessToken, nil
}

func (s *State) oauthLoginLocked(ctx context.Context) (string, error) {
	if s.onDeviceCode == nil {
		return "", fmt.Errorf("缺少 device code 处理回调")
	}

	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("Accept-Language", "en-US")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Pragma", "no-cache")
	headers.Set("Client-Id", s.clientInfo.ClientID)
	headers.Set("Content-Type", "application/x-www-form-urlencoded")
	headers.Set("Origin", s.clientInfo.ClientURL)
	headers.Set("Referer", s.clientInfo.ClientURL)
	headers.Set("User-Agent", s.clientInfo.UserAgent)
	headers.Set("X-Device-Id", s.deviceID)

	for {
		issuedAt := s.now().UTC()
		response, err := s.httpClient.Do(ctx, httpclient.Request{
			Method:  http.MethodPost,
			URL:     s.deviceEndpoint,
			Headers: headers,
			Body:    []byte(url.Values{"client_id": {s.clientInfo.ClientID}, "scopes": {""}}.Encode()),
		})
		if err != nil {
			return "", fmt.Errorf("请求 device code 失败: %w", err)
		}
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("请求 device code 失败: 状态码 %d", response.StatusCode)
		}

		var deviceCodeResponse deviceCodeResponse
		if err := response.DecodeJSON(&deviceCodeResponse); err != nil {
			return "", fmt.Errorf("解析 device code 响应失败: %w", err)
		}
		if deviceCodeResponse.DeviceCode == "" || deviceCodeResponse.UserCode == "" || deviceCodeResponse.VerificationURI == "" {
			return "", fmt.Errorf("device code 响应不完整")
		}

		interval := time.Duration(deviceCodeResponse.Interval) * time.Second
		if interval <= 0 {
			interval = 5 * time.Second
		}
		expiresAt := issuedAt.Add(time.Duration(deviceCodeResponse.ExpiresIn) * time.Second)
		if deviceCodeResponse.ExpiresIn <= 0 {
			expiresAt = issuedAt.Add(30 * time.Minute)
		}

		if err := s.onDeviceCode(ctx, DeviceCode{
			UserCode:        deviceCodeResponse.UserCode,
			VerificationURI: deviceCodeResponse.VerificationURI,
			Interval:        interval,
			ExpiresAt:       expiresAt,
		}); err != nil {
			return "", fmt.Errorf("处理 device code 失败: %w", err)
		}

		currentInterval := interval
		for {
			if err := s.sleep(ctx, currentInterval); err != nil {
				return "", err
			}

			response, err := s.httpClient.Do(ctx, httpclient.Request{
				Method:          http.MethodPost,
				URL:             s.tokenEndpoint,
				Headers:         headers,
				InvalidateAfter: expiresAt,
				Body: []byte(url.Values{
					"client_id":   {s.clientInfo.ClientID},
					"device_code": {deviceCodeResponse.DeviceCode},
					"grant_type":  {deviceGrantType},
				}.Encode()),
			})
			if errors.Is(err, httpclient.ErrRequestInvalid) {
				break
			}
			if err != nil {
				return "", fmt.Errorf("轮询 access token 失败: %w", err)
			}

			requestNewCode := false
			switch response.StatusCode {
			case http.StatusOK:
				var tokenResponse tokenResponse
				if err := response.DecodeJSON(&tokenResponse); err != nil {
					return "", fmt.Errorf("解析 access token 响应失败: %w", err)
				}
				if tokenResponse.AccessToken == "" {
					return "", fmt.Errorf("access token 响应不完整")
				}
				return tokenResponse.AccessToken, nil
			case http.StatusBadRequest:
				var tokenError tokenErrorResponse
				if err := json.Unmarshal(response.Body, &tokenError); err == nil {
					switch firstNonEmpty(tokenError.Error, tokenError.Message) {
					case "authorization_pending", "":
						continue
					case "slow_down":
						currentInterval += 5 * time.Second
						continue
					case "expired_token":
						requestNewCode = true
					default:
						return "", fmt.Errorf("轮询 access token 失败: %s", firstNonEmpty(tokenError.Error, tokenError.Message))
					}
				}
				if requestNewCode {
					break
				}
				continue
			default:
				return "", fmt.Errorf("轮询 access token 失败: 状态码 %d", response.StatusCode)
			}

			break
		}
	}
}

func (s *State) validateAccessToken(ctx context.Context) (validateResponse, int, error) {
	headers := make(http.Header)
	headers.Set("Authorization", "OAuth "+s.accessToken)

	response, err := s.httpClient.Do(ctx, httpclient.Request{
		Method:  http.MethodGet,
		URL:     s.validateEndpoint,
		Headers: headers,
	})
	if err != nil {
		return validateResponse{}, 0, fmt.Errorf("请求 token validate 失败: %w", err)
	}
	if response.StatusCode == http.StatusUnauthorized {
		return validateResponse{}, http.StatusUnauthorized, nil
	}
	if response.StatusCode != http.StatusOK {
		return validateResponse{}, response.StatusCode, nil
	}

	var parsed validateResponse
	if err := response.DecodeJSON(&parsed); err != nil {
		return validateResponse{}, 0, fmt.Errorf("解析 token validate 响应失败: %w", err)
	}

	return parsed, response.StatusCode, nil
}

func (s *State) headersLocked(options HeadersOptions) http.Header {
	headers := make(http.Header)
	headers.Set("Accept", "*/*")
	headers.Set("Accept-Language", "en-US")
	headers.Set("Pragma", "no-cache")
	headers.Set("Cache-Control", "no-cache")
	if s.clientInfo.ClientID != "" {
		headers.Set("Client-Id", s.clientInfo.ClientID)
	}
	if options.UserAgent != "" {
		headers.Set("User-Agent", options.UserAgent)
	}
	if s.sessionID != "" {
		headers.Set("Client-Session-Id", s.sessionID)
	}
	if s.deviceID != "" {
		headers.Set("X-Device-Id", s.deviceID)
	}
	if options.GQL {
		headers.Set("Origin", s.clientInfo.ClientURL)
		headers.Set("Referer", s.clientInfo.ClientURL)
		if s.accessToken != "" {
			headers.Set("Authorization", "OAuth "+s.accessToken)
		}
	}

	return headers
}

func (s *State) cookieValueLocked(name string) string {
	cookies := s.httpClient.CookieJar().Cookies(s.clientURL)
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name != name {
			continue
		}
		return cookie.Value
	}

	return ""
}

func (s *State) updatePersistentCookiesLocked(userID string) {
	s.httpClient.CookieJar().SetCookies(s.clientURL, []*http.Cookie{
		{
			Name:  "auth-token",
			Value: s.accessToken,
			Path:  "/",
		},
		{
			Name:  "persistent",
			Value: userID,
			Path:  "/",
		},
	})
}

func generateSessionID() (string, error) {
	bytes := make([]byte, sessionIDLength/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return hex.EncodeToString(bytes), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}

	return ""
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
