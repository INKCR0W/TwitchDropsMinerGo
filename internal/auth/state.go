package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

const (
	defaultDeviceEndpoint   = "https://id.twitch.tv/oauth2/device"
	defaultTokenEndpoint    = "https://id.twitch.tv/oauth2/token"
	defaultValidateEndpoint = "https://id.twitch.tv/oauth2/validate"
	sessionIDLength         = 16
)

type Snapshot struct {
	UserID        int64
	DeviceID      string
	SessionID     string
	AccessToken   string
	ClientVersion string
}

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

func generateSessionID() (string, error) {
	bytes := make([]byte, sessionIDLength/2)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}

	return hex.EncodeToString(bytes), nil
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
