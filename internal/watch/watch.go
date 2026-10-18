package watch

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
)

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

	playlistURL, err := t.fetchStreamPlaylistURL(ctx, channel)
	if err != nil {
		return false, err
	}
	response, err := t.httpClient.Do(ctx, httpclient.Request{
		Method: http.MethodGet,
		URL:    playlistURL,
		// Twitch CDN 会在播放列表返回后断开连接，避免复用即将失效的连接
		Headers: http.Header{"Connection": {"close"}},
	})
	if err != nil {
		return false, fmt.Errorf("获取视频分片列表失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		t.setStreamPlaylistURL(channel, "")
		return false, fmt.Errorf("获取视频分片列表失败: HTTP %d", response.StatusCode)
	}
	chunks, err := parsePlaylistURLs(response.Text(), playlistURL)
	if err != nil {
		t.setStreamPlaylistURL(channel, "")
		return false, fmt.Errorf("解析视频分片列表失败: %w", err)
	}
	for _, chunkURL := range chunks {
		response, err := t.httpClient.Do(ctx, httpclient.Request{Method: http.MethodHead, URL: chunkURL})
		if err != nil {
			return false, fmt.Errorf("发送视频分片 HEAD 请求失败: %w", err)
		}
		if response.StatusCode != http.StatusOK {
			t.setStreamPlaylistURL(channel, "")
			return false, fmt.Errorf("视频分片 HEAD 请求失败: HTTP %d", response.StatusCode)
		}
	}
	return true, nil
}

func (t *Tracker) fetchStreamPlaylistURL(ctx context.Context, channel domain.Channel) (string, error) {
	t.mu.Lock()
	tracked := t.channels[channel.ID]
	if tracked != nil && tracked.playbackBroadcastID == channel.Stream.BroadcastID && tracked.playbackURL != "" {
		playlistURL := tracked.playbackURL
		t.mu.Unlock()
		return playlistURL, nil
	}
	t.mu.Unlock()

	operation, err := gql.MustLookup(gql.OperationPlaybackAccessToken).WithVariables(map[string]any{"login": channel.Login})
	if err != nil {
		return "", fmt.Errorf("构造播放凭据请求失败: %w", err)
	}
	response, err := t.gqlClient.Do(ctx, operation)
	if err != nil {
		return "", fmt.Errorf("获取播放凭据失败: %w", err)
	}
	token, err := nestedMap(response.Data, "data", "streamPlaybackAccessToken")
	if err != nil {
		return "", fmt.Errorf("解析播放凭据失败: %w", err)
	}
	value, signature := stringValue(token, "value"), stringValue(token, "signature")
	if value == "" || signature == "" {
		return "", fmt.Errorf("播放凭据缺少 value 或 signature")
	}
	query := url.Values{"sig": {signature}, "token": {value}}
	masterURL := "https://usher.ttvnw.net/api/channel/hls/" + url.PathEscape(channel.Login) + ".m3u8?" + query.Encode()
	master, err := t.httpClient.Do(ctx, httpclient.Request{Method: http.MethodGet, URL: masterURL})
	if err != nil {
		return "", fmt.Errorf("获取直播播放列表失败: %w", err)
	}
	if master.StatusCode != http.StatusOK {
		return "", fmt.Errorf("获取直播播放列表失败: HTTP %d", master.StatusCode)
	}
	qualities, err := parsePlaylistURLs(master.Text(), masterURL)
	if err != nil {
		return "", fmt.Errorf("解析直播播放列表失败: %w", err)
	}
	// 选择最后一个画质，通常是最低画质
	playlistURL := qualities[len(qualities)-1]
	t.setStreamPlaylistURL(channel, playlistURL)
	return playlistURL, nil
}

func (t *Tracker) setStreamPlaylistURL(channel domain.Channel, playlistURL string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tracked := t.channels[channel.ID]
	if tracked == nil || tracked.channel == nil || tracked.channel.Stream == nil ||
		tracked.channel.Login != channel.Login || tracked.channel.Stream.BroadcastID != channel.Stream.BroadcastID {
		return
	}
	tracked.playbackBroadcastID = channel.Stream.BroadcastID
	tracked.playbackURL = playlistURL
}

func parsePlaylistURLs(body, playlistURL string) ([]string, error) {
	if !strings.HasPrefix(strings.TrimSpace(body), "#EXTM3U") {
		return nil, fmt.Errorf("响应不是 M3U8 播放列表")
	}
	base, err := url.Parse(playlistURL)
	if err != nil {
		return nil, fmt.Errorf("播放列表地址无效")
	}
	var urls []string
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		reference, err := url.Parse(line)
		if err != nil {
			return nil, fmt.Errorf("播放列表包含无效地址")
		}
		resolved := base.ResolveReference(reference)
		if (resolved.Scheme != "https" && resolved.Scheme != "http") || resolved.Host == "" || resolved.User != nil {
			return nil, fmt.Errorf("播放列表包含不支持的地址")
		}
		urls = append(urls, resolved.String())
	}
	if len(urls) == 0 {
		return nil, fmt.Errorf("播放列表中没有可请求的地址")
	}
	return urls, nil
}
