package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"twitchdropsminergo/internal/httpclient"
)

type validateResponse struct {
	ClientID string `json:"client_id"`
	UserID   string `json:"user_id"`
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
