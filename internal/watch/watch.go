package watch

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

const watchGQLMutation = "\n mutation SendEvents($input: SendSpadeEventsInput!) " +
	"{\n sendSpadeEvents(input: $input) {\n statusCode\n}\n}\n"

type watchEvent struct {
	Event      string          `json:"event"`
	Properties watchProperties `json:"properties"`
}

type watchProperties struct {
	BroadcastID   string `json:"broadcast_id"`
	ChannelID     string `json:"channel_id"`
	Channel       string `json:"channel"`
	ClientTime    string `json:"client_time"`
	Game          string `json:"game"`
	GameID        string `json:"game_id"`
	Hidden        bool   `json:"hidden"`
	IsLive        bool   `json:"is_live"`
	Live          bool   `json:"live"`
	LoggedIn      bool   `json:"logged_in"`
	MinutesLogged int    `json:"minutes_logged"`
	Muted         bool   `json:"muted"`
	UserID        int64  `json:"user_id"`
}

func BuildWatchQuery(channel *domain.Channel, userID int64, now time.Time) (gql.RawQuery, error) {
	if channel == nil {
		return gql.RawQuery{}, fmt.Errorf("频道不能为空")
	}
	if userID <= 0 {
		return gql.RawQuery{}, fmt.Errorf("watch payload 缺少 user_id")
	}
	if strings.TrimSpace(channel.Login) == "" {
		return gql.RawQuery{}, fmt.Errorf("watch payload 缺少 channel login")
	}
	if channel.ID <= 0 {
		return gql.RawQuery{}, fmt.Errorf("watch payload 缺少 channel id")
	}
	if channel.Stream == nil {
		return gql.RawQuery{}, fmt.Errorf("watch payload 缺少 stream 信息")
	}
	if channel.Stream.BroadcastID <= 0 {
		return gql.RawQuery{}, fmt.Errorf("watch payload 缺少 broadcast_id")
	}

	gameName := ""
	gameID := ""
	if channel.Stream.Game != nil {
		gameName = channel.Stream.Game.Name
		gameID = strconv.FormatInt(channel.Stream.Game.ID, 10)
	}

	payload := []watchEvent{
		{
			Event: "minute-watched",
			Properties: watchProperties{
				BroadcastID:   strconv.FormatInt(channel.Stream.BroadcastID, 10),
				ChannelID:     strconv.FormatInt(channel.ID, 10),
				Channel:       channel.Login,
				ClientTime:    now.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
				Game:          gameName,
				GameID:        gameID,
				Hidden:        false,
				IsLive:        true,
				Live:          true,
				LoggedIn:      true,
				MinutesLogged: 1,
				Muted:         false,
				UserID:        userID,
			},
		},
	}

	body, err := marshalCompactNoEscape(payload)
	if err != nil {
		return gql.RawQuery{}, fmt.Errorf("序列化 watch payload 失败: %w", err)
	}

	compressed, err := gzipBytes(body)
	if err != nil {
		return gql.RawQuery{}, err
	}

	return gql.RawQuery{
		Query: watchGQLMutation,
		Variables: map[string]any{
			"input": map[string]any{
				"data":       base64.StdEncoding.EncodeToString(compressed),
				"repository": "twilight",
				"encoding":   "GZIP_B64",
			},
		},
	}, nil
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

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("gzip 压缩 watch payload 失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("gzip 关闭失败: %w", err)
	}

	return buf.Bytes(), nil
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

	query, err := BuildWatchQuery(&channel, t.authState.Snapshot().UserID, t.now())
	if err != nil {
		return false, err
	}

	response, err := t.gqlClient.DoRaw(ctx, query)
	if err != nil {
		return false, fmt.Errorf("发送 minute-watched 失败: %w", err)
	}

	code, ok := watchStatusCode(response)
	return ok && code == 204, nil
}

func watchStatusCode(response gql.Response) (int, bool) {
	data, ok := response.Data.(map[string]any)
	if !ok {
		return 0, false
	}
	events, ok := data["sendSpadeEvents"].(map[string]any)
	if !ok {
		return 0, false
	}

	switch value := events["statusCode"].(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	case int64:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}
