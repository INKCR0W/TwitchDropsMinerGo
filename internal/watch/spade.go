package watch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/httpclient"
)

var (
	settingsPattern = regexp.MustCompile(`src="(https://[\w.]+/config/settings\.[0-9a-f]{32}\.js)"`)
	spadePattern    = regexp.MustCompile(`"(?:spade_?url|spadeUrl)"\s*:\s*"([^"]+)"`)
)

type watchEvent struct {
	Event      string          `json:"event"`
	Properties watchProperties `json:"properties"`
}

type watchProperties struct {
	BroadcastID string `json:"broadcast_id"`
	ChannelID   string `json:"channel_id"`
	Channel     string `json:"channel"`
	Hidden      bool   `json:"hidden"`
	Live        bool   `json:"live"`
	Location    string `json:"location"`
	LoggedIn    bool   `json:"logged_in"`
	Muted       bool   `json:"muted"`
	Player      string `json:"player"`
	UserID      int64  `json:"user_id"`
}

func (t *Tracker) GetSpadeURL(ctx context.Context, channelID int64) (string, error) {
	if t == nil {
		return "", fmt.Errorf("watch 跟踪器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	spec, cached, err := t.lookupChannelForSpade(channelID)
	if err != nil {
		return "", err
	}
	if cached != "" {
		return cached, nil
	}

	channelURL := strings.TrimRight(t.clientInfo.ClientURL, "/") + "/" + spec.Login
	headers := t.authState.Headers(auth.HeadersOptions{UserAgent: t.clientInfo.UserAgent})

	response, err := t.httpClient.Do(ctx, httpclient.Request{
		Method:  http.MethodGet,
		URL:     channelURL,
		Headers: headers,
	})
	if err != nil {
		return "", fmt.Errorf("请求频道页面失败: %w", err)
	}

	spadeURL, err := extractSpadeURLFromDocument(response.Text())
	if err == nil {
		t.storeSpadeURL(channelID, spadeURL)
		return spadeURL, nil
	}

	settingsMatch := settingsPattern.FindStringSubmatch(response.Text())
	if len(settingsMatch) != 2 {
		return "", fmt.Errorf("提取 spade_url 失败: 页面中缺少 settings.js")
	}

	settingsResponse, err := t.httpClient.Do(ctx, httpclient.Request{
		Method:  http.MethodGet,
		URL:     settingsMatch[1],
		Headers: headers,
	})
	if err != nil {
		return "", fmt.Errorf("请求 settings.js 失败: %w", err)
	}

	spadeURL, err = extractSpadeURLFromDocument(settingsResponse.Text())
	if err != nil {
		return "", fmt.Errorf("提取 spade_url 失败: settings.js 中缺少字段")
	}

	t.storeSpadeURL(channelID, spadeURL)
	return spadeURL, nil
}

func (t *Tracker) SendWatch(ctx context.Context, channelID int64) (bool, error) {
	if t == nil {
		return false, fmt.Errorf("watch 跟踪器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := t.authState.Validate(ctx); err != nil {
		return false, fmt.Errorf("校验 watch 认证状态失败: %w", err)
	}

	channel, ok := t.Channel(channelID)
	if !ok {
		return false, ErrChannelNotTracked
	}
	if !channel.Online() {
		return false, nil
	}

	authSnapshot := t.authState.Snapshot()
	payload, err := BuildWatchPayload(&channel, authSnapshot.UserID)
	if err != nil {
		return false, err
	}

	spadeURL, err := t.GetSpadeURL(ctx, channelID)
	if err != nil {
		return false, err
	}

	headers := t.authState.Headers(auth.HeadersOptions{UserAgent: t.clientInfo.UserAgent})
	headers.Set("Content-Type", "application/x-www-form-urlencoded")

	response, err := t.httpClient.Do(ctx, httpclient.Request{
		Method:  http.MethodPost,
		URL:     spadeURL,
		Headers: headers,
		Body:    []byte(payload.Encode()),
	})
	if err != nil {
		return false, fmt.Errorf("发送 minute-watched 失败: %w", err)
	}

	return response.StatusCode == http.StatusNoContent, nil
}

func BuildWatchPayload(channel *domain.Channel, userID int64) (url.Values, error) {
	if channel == nil {
		return nil, fmt.Errorf("频道不能为空")
	}
	if userID <= 0 {
		return nil, fmt.Errorf("watch payload 缺少 user_id")
	}
	if strings.TrimSpace(channel.Login) == "" {
		return nil, fmt.Errorf("watch payload 缺少 channel login")
	}
	if channel.ID <= 0 {
		return nil, fmt.Errorf("watch payload 缺少 channel id")
	}
	if channel.Stream == nil {
		return nil, fmt.Errorf("watch payload 缺少 stream 信息")
	}
	if channel.Stream.BroadcastID <= 0 {
		return nil, fmt.Errorf("watch payload 缺少 broadcast_id")
	}

	body, err := json.Marshal([]watchEvent{
		{
			Event: "minute-watched",
			Properties: watchProperties{
				BroadcastID: strconv.FormatInt(channel.Stream.BroadcastID, 10),
				ChannelID:   strconv.FormatInt(channel.ID, 10),
				Channel:     channel.Login,
				Hidden:      false,
				Live:        true,
				Location:    "channel",
				LoggedIn:    true,
				Muted:       false,
				Player:      "site",
				UserID:      userID,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("序列化 watch payload 失败: %w", err)
	}

	values := make(url.Values, 1)
	values.Set("data", base64.StdEncoding.EncodeToString(body))
	return values, nil
}

func (t *Tracker) lookupChannelForSpade(channelID int64) (channelSpec, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return channelSpec{}, "", ErrChannelNotTracked
	}

	return channelSpec{
		ID:          tracked.channel.ID,
		Login:       tracked.channel.Login,
		DisplayName: tracked.channel.DisplayName,
		ACLBased:    tracked.channel.ACLBased,
	}, tracked.spadeURL, nil
}

func (t *Tracker) storeSpadeURL(channelID int64, spadeURL string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil {
		return
	}
	tracked.spadeURL = spadeURL
}

func extractSpadeURLFromDocument(document string) (string, error) {
	match := spadePattern.FindStringSubmatch(document)
	if len(match) != 2 {
		return "", fmt.Errorf("文档中缺少 spade_url")
	}

	decoded, err := decodeJSONString(match[1])
	if err != nil {
		return "", err
	}
	return decoded, nil
}

func decodeJSONString(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("spade_url 为空")
	}

	var decoded string
	if err := json.Unmarshal([]byte(`"`+value+`"`), &decoded); err != nil {
		return strings.ReplaceAll(value, `\/`, `/`), nil
	}
	return decoded, nil
}
