package watch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/httpclient"
)

// minute-watched 是行为遥测事件, 必须直接打到 spade。Twitch 已不再把 GQL sendSpadeEvents 计入有效观看
const spadeTrackURL = "https://spade.twitch.tv/track"

type spadeEvent struct {
	Event      string          `json:"event"`
	Properties spadeProperties `json:"properties"`
}

type spadeProperties struct {
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

func BuildWatchBody(channel *domain.Channel, userID int64) ([]byte, error) {
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

	payload := []spadeEvent{
		{
			Event: "minute-watched",
			Properties: spadeProperties{
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
	}

	body, err := marshalCompactNoEscape(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化 watch payload 失败: %w", err)
	}
	return []byte("data=" + base64.StdEncoding.EncodeToString(body)), nil
}

func marshalCompactNoEscape(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
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

	body, err := BuildWatchBody(&channel, t.authState.Snapshot().UserID)
	if err != nil {
		return false, err
	}

	headers, err := t.watchHeaders(ctx)
	if err != nil {
		return false, fmt.Errorf("获取 watch 请求头失败: %w", err)
	}
	headers = headers.Clone()
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")

	response, err := t.spadeClient.Do(ctx, httpclient.Request{
		Method:  http.MethodPost,
		URL:     spadeTrackURL,
		Headers: headers,
		Body:    body,
	})
	if err != nil {
		return false, fmt.Errorf("发送 minute-watched 失败: %w", err)
	}
	return response.StatusCode == http.StatusNoContent, nil
}
