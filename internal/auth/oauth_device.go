package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

type DeviceCode struct {
	UserCode        string
	VerificationURI string
	Interval        time.Duration
	ExpiresAt       time.Time
}

type DeviceCodeHandler func(context.Context, DeviceCode) error

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

type tokenErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
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

		var issuedCode deviceCodeResponse
		if err := response.DecodeJSON(&issuedCode); err != nil {
			return "", fmt.Errorf("解析 device code 响应失败: %w", err)
		}
		if issuedCode.DeviceCode == "" || issuedCode.UserCode == "" || issuedCode.VerificationURI == "" {
			return "", fmt.Errorf("device code 响应不完整")
		}

		interval := time.Duration(issuedCode.Interval) * time.Second
		if interval <= 0 {
			interval = 5 * time.Second
		}
		expiresAt := issuedAt.Add(time.Duration(issuedCode.ExpiresIn) * time.Second)
		if issuedCode.ExpiresIn <= 0 {
			expiresAt = issuedAt.Add(30 * time.Minute)
		}

		if err := s.onDeviceCode(ctx, DeviceCode{
			UserCode:        issuedCode.UserCode,
			VerificationURI: issuedCode.VerificationURI,
			Interval:        interval,
			ExpiresAt:       expiresAt,
		}); err != nil {
			return "", fmt.Errorf("处理 device code 失败: %w", err)
		}

		accessToken, retryWithNewCode, err := s.pollAccessTokenLocked(ctx, headers, issuedCode.DeviceCode, expiresAt, interval)
		if err != nil {
			return "", err
		}
		if retryWithNewCode {
			continue
		}
		return accessToken, nil
	}
}

func (s *State) pollAccessTokenLocked(ctx context.Context, headers http.Header, deviceCode string, expiresAt time.Time, interval time.Duration) (string, bool, error) {
	currentInterval := interval
	for {
		if err := s.sleep(ctx, currentInterval); err != nil {
			return "", false, err
		}

		response, err := s.httpClient.Do(ctx, httpclient.Request{
			Method:          http.MethodPost,
			URL:             s.tokenEndpoint,
			Headers:         headers,
			InvalidateAfter: expiresAt,
			Body: []byte(url.Values{
				"client_id":   {s.clientInfo.ClientID},
				"device_code": {deviceCode},
				"grant_type":  {deviceGrantType},
			}.Encode()),
		})
		if errors.Is(err, httpclient.ErrRequestInvalid) {
			return "", true, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("轮询 access token 失败: %w", err)
		}

		switch response.StatusCode {
		case http.StatusOK:
			var issuedToken tokenResponse
			if err := response.DecodeJSON(&issuedToken); err != nil {
				return "", false, fmt.Errorf("解析 access token 响应失败: %w", err)
			}
			if issuedToken.AccessToken == "" {
				return "", false, fmt.Errorf("access token 响应不完整")
			}
			return issuedToken.AccessToken, false, nil
		case http.StatusBadRequest:
			nextInterval, retryWithNewCode, err := parseTokenPollingResponse(response.Body, currentInterval)
			if err != nil {
				return "", false, err
			}
			if retryWithNewCode {
				return "", true, nil
			}
			currentInterval = nextInterval
			continue
		default:
			return "", false, fmt.Errorf("轮询 access token 失败: 状态码 %d", response.StatusCode)
		}
	}
}

func parseTokenPollingResponse(body []byte, currentInterval time.Duration) (time.Duration, bool, error) {
	var tokenError tokenErrorResponse
	if err := json.Unmarshal(body, &tokenError); err != nil {
		return currentInterval, false, nil
	}

	switch firstNonEmpty(tokenError.Error, tokenError.Message) {
	case "authorization_pending", "":
		return currentInterval, false, nil
	case "slow_down":
		return currentInterval + 5*time.Second, false, nil
	case "expired_token":
		return currentInterval, true, nil
	default:
		return currentInterval, false, fmt.Errorf("轮询 access token 失败: %s", firstNonEmpty(tokenError.Error, tokenError.Message))
	}
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
