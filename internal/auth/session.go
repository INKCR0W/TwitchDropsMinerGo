package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"twitchdropsminergo/internal/secure"
)

const SessionVersion = 1
const maxSessionBytes = 64 * 1024

var ErrLoginRequired = errors.New("需要浏览器登录：请先停止服务，再运行 miner-server -login（使用相同的 -runtime-dir）；Docker 使用 docker compose run --rm -it miner -login")

type Session struct {
	Version     int    `json:"version"`
	ClientID    string `json:"client_id"`
	AccessToken string `json:"access_token"`
}

func (Session) String() string   { return "Session{凭据已隐藏}" }
func (Session) GoString() string { return "auth.Session{凭据已隐藏}" }

func (s Session) validate() error {
	if s.Version != SessionVersion {
		return fmt.Errorf("不支持的会话版本")
	}
	if strings.TrimSpace(s.ClientID) == "" || s.ClientID != strings.TrimSpace(s.ClientID) {
		return fmt.Errorf("会话缺少有效 Client-ID")
	}
	if s.AccessToken == "" || len(s.AccessToken) > 4096 {
		return fmt.Errorf("会话缺少有效 access token")
	}
	for _, r := range s.AccessToken {
		if r < '!' || r > '~' {
			return fmt.Errorf("会话 access token 包含无效字符")
		}
	}
	return nil
}

func ReadSession(reader io.Reader) (Session, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxSessionBytes+1))
	if err != nil {
		return Session{}, fmt.Errorf("读取登录会话失败")
	}
	if len(data) > maxSessionBytes {
		return Session{}, fmt.Errorf("登录会话文件过大")
	}
	var session Session
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&session); err != nil {
		return Session{}, fmt.Errorf("解析登录会话失败，文件格式无效")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Session{}, fmt.Errorf("登录会话包含多余或无效内容")
	}
	if err := session.validate(); err != nil {
		return Session{}, err
	}
	return session, nil
}

func WriteSession(path string, session Session) (resultErr error) {
	if err := session.validate(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}
	file, err := os.CreateTemp(directory, ".login-session-*")
	if err != nil {
		return fmt.Errorf("创建会话文件失败: %w", err)
	}
	temporary := file.Name()
	defer func() {
		if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("清理临时会话失败: %w", err))
		}
	}()
	if err := secure.HardenFile(temporary); err != nil {
		return errors.Join(fmt.Errorf("保护会话文件失败: %w", err), file.Close())
	}
	if err := json.NewEncoder(file).Encode(session); err != nil {
		return errors.Join(fmt.Errorf("写入会话失败: %w", err), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("同步会话失败: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭会话文件失败: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("保存会话失败: %w", err)
	}
	return nil
}

func (s *State) ImportSession(ctx context.Context, session Session) error {
	if s == nil {
		return fmt.Errorf("认证状态未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := session.validate(); err != nil {
		return err
	}
	if session.ClientID != s.clientInfo.ClientID {
		return fmt.Errorf("登录会话 Client-ID 与当前客户端不匹配")
	}
	s.validateMu.Lock()
	defer s.validateMu.Unlock()
	validated, status, err := s.validateAccessToken(ctx, session.AccessToken)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("导入会话校验失败: HTTP %d", status)
	}
	userID, err := validatedUserID(validated, s.clientInfo.ClientID)
	if err != nil {
		return err
	}
	// 校验成功前不覆盖已有会话，输错文件也能继续使用原来的登录
	s.updatePersistentCookies(session.AccessToken, validated.UserID)
	if err := s.httpClient.CookieJar().Save(); err != nil {
		return fmt.Errorf("保存认证 Cookie 失败: %w", err)
	}
	s.setAccessToken(session.AccessToken)
	s.setValidatedAccessState(userID)
	if s.onAuthenticated != nil {
		s.onAuthenticated()
	}
	return nil
}

func validatedUserID(response validateResponse, clientID string) (int64, error) {
	if response.ClientID != clientID {
		return 0, fmt.Errorf("验证 access token 失败: client_id 不匹配")
	}
	userID, err := strconv.ParseInt(response.UserID, 10, 64)
	if err != nil || userID <= 0 {
		return 0, fmt.Errorf("验证 access token 失败: user_id 无效")
	}
	return userID, nil
}

func (s *State) ensureBrowserAuthenticated(ctx context.Context) error {
	token := s.currentAccessToken()
	if token == "" {
		token = s.cookieValue("auth-token")
	}
	if token == "" {
		return ErrLoginRequired
	}
	response, status, err := s.validateAccessToken(ctx, token)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized {
		s.clearAccessState()
		return ErrLoginRequired
	}
	if status != http.StatusOK {
		return fmt.Errorf("验证 access token 失败: HTTP %d", status)
	}
	userID, err := validatedUserID(response, s.clientInfo.ClientID)
	if err != nil {
		s.clearAccessState()
		return fmt.Errorf("%w；%v", ErrLoginRequired, err)
	}
	s.updatePersistentCookies(token, response.UserID)
	if err := s.httpClient.CookieJar().Save(); err != nil {
		return fmt.Errorf("保存认证 Cookie 失败: %w", err)
	}
	s.setAccessToken(token)
	s.setValidatedAccessState(userID)
	if s.onAuthenticated != nil {
		s.onAuthenticated()
	}
	return nil
}
