package watch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/inventory"
)

const (
	DefaultOnlineDelay = 120 * time.Second
	defaultBatchSize   = 20
)

var (
	ErrChannelNotTracked = errors.New("watch 频道未纳入跟踪")

	settingsPattern = regexp.MustCompile(`src="(https://[\w.]+/config/settings\.[0-9a-f]{32}\.js)"`)
	spadePattern    = regexp.MustCompile(`"(?:spade_?url|spadeUrl)"\s*:\s*"([^"]+)"`)
)

type GQLClient interface {
	Do(context.Context, gql.Operation) (gql.Response, error)
	DoBatch(context.Context, []gql.Operation) ([]gql.Response, error)
}

type HTTPClient interface {
	Do(context.Context, httpclient.Request) (httpclient.Response, error)
}

type AuthState interface {
	Validate(context.Context) error
	Snapshot() auth.Snapshot
	Headers(auth.HeadersOptions) http.Header
}

type Options struct {
	GQLClient   GQLClient
	HTTPClient  HTTPClient
	AuthState   AuthState
	ClientInfo  httpclient.ClientInfo
	OnlineDelay time.Duration
	BatchSize   int
	Clock       func() time.Time
	Sleep       func(context.Context, time.Duration) error
}

type Tracker struct {
	gqlClient   GQLClient
	httpClient  HTTPClient
	authState   AuthState
	clientInfo  httpclient.ClientInfo
	onlineDelay time.Duration
	batchSize   int
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	settings  config.Settings
	inventory inventory.Snapshot
	channels  map[int64]*trackedChannel
	wg        sync.WaitGroup
}

type trackedChannel struct {
	channel       *domain.Channel
	spadeURL      string
	pendingSeq    uint64
	pendingCancel context.CancelFunc
}

type channelSpec struct {
	ID          int64
	Login       string
	DisplayName string
	ACLBased    bool
}

type fetchedChannel struct {
	DisplayName string
	Stream      *domain.Stream
}

type streamStateMessage struct {
	Type    string `json:"type"`
	Viewers int    `json:"viewers"`
}

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

func NewTracker(options Options) (*Tracker, error) {
	if options.GQLClient == nil {
		return nil, fmt.Errorf("watch GQL 客户端不能为空")
	}
	if options.HTTPClient == nil {
		return nil, fmt.Errorf("watch HTTP 客户端不能为空")
	}
	if options.AuthState == nil {
		return nil, fmt.Errorf("watch 认证状态不能为空")
	}

	clientInfo := options.ClientInfo
	if clientInfo == (httpclient.ClientInfo{}) {
		clientInfo = httpclient.AndroidAppClient
	}

	onlineDelay := options.OnlineDelay
	if onlineDelay <= 0 {
		onlineDelay = DefaultOnlineDelay
	}

	batchSize := options.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	now := options.Clock
	if now == nil {
		now = time.Now
	}

	sleep := options.Sleep
	if sleep == nil {
		sleep = sleepContext
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Tracker{
		gqlClient:   options.GQLClient,
		httpClient:  options.HTTPClient,
		authState:   options.AuthState,
		clientInfo:  clientInfo,
		onlineDelay: onlineDelay,
		batchSize:   batchSize,
		now:         now,
		sleep:       sleep,
		ctx:         ctx,
		cancel:      cancel,
		settings:    config.DefaultSettings(),
		channels:    make(map[int64]*trackedChannel),
	}, nil
}

func (t *Tracker) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	t.cancel()

	t.mu.Lock()
	for _, tracked := range t.channels {
		if tracked == nil || tracked.pendingCancel == nil {
			continue
		}
		tracked.pendingCancel()
		tracked.pendingCancel = nil
		if tracked.channel != nil {
			tracked.channel.PendingStream = false
		}
	}
	t.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		t.wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (t *Tracker) Configure(settings config.Settings, snapshot inventory.Snapshot) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.settings = settings
	t.inventory = snapshot
}

func (t *Tracker) AddChannel(channel domain.Channel) {
	if t == nil || channel.ID <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cloned := cloneChannel(&channel)
	if existing, ok := t.channels[channel.ID]; ok && existing != nil {
		if cloned.DisplayName == "" {
			cloned.DisplayName = existing.channel.DisplayName
		}
		if cloned.Stream == nil {
			cloned.Stream = cloneStream(existing.channel.Stream)
		}
		if !cloned.PendingStream {
			cloned.PendingStream = existing.channel.PendingStream
		}
		existing.channel = &cloned
		return
	}

	t.channels[channel.ID] = &trackedChannel{
		channel: &cloned,
	}
}

func (t *Tracker) RemoveChannel(channelID int64) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil {
		return
	}
	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
	}
	delete(t.channels, channelID)
}

func (t *Tracker) Channel(channelID int64) (domain.Channel, bool) {
	if t == nil {
		return domain.Channel{}, false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return domain.Channel{}, false
	}

	return cloneChannel(tracked.channel), true
}

func (t *Tracker) SyncChannel(ctx context.Context, channelID int64) (bool, error) {
	if t == nil {
		return false, fmt.Errorf("watch 跟踪器未初始化")
	}

	spec, settings, snapshot, err := t.lookupChannel(channelID)
	if err != nil {
		return false, err
	}

	fetched, err := t.fetchChannel(ctx, spec, settings, snapshot)
	if err != nil {
		return false, err
	}

	t.applyFetched(channelID, fetched)
	return fetched.Stream != nil, nil
}

func (t *Tracker) SyncChannels(ctx context.Context, channelIDs ...int64) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	specs, settings, snapshot, err := t.collectChannels(channelIDs)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		return nil
	}

	fetched := make(map[int64]fetchedChannel, len(specs))
	for _, chunk := range chunkSpecs(specs, t.batchSize) {
		operations := make([]gql.Operation, 0, len(chunk))
		for _, spec := range chunk {
			operation, err := gql.MustLookup(gql.OperationGetStreamInfo).WithVariables(map[string]any{
				"channel": spec.Login,
			})
			if err != nil {
				return fmt.Errorf("构造 GetStreamInfo 请求失败: %w", err)
			}
			operations = append(operations, operation)
		}

		responses, err := t.gqlClient.DoBatch(ctx, operations)
		if err != nil {
			return fmt.Errorf("批量请求 GetStreamInfo 失败: %w", err)
		}

		pendingDrops := make([]channelSpec, 0, len(chunk))
		for index, response := range responses {
			result, err := parseGetStreamInfoResponse(chunk[index], response, settings.AvailableDropsCheck)
			if err != nil {
				return err
			}
			fetched[chunk[index].ID] = result
			if result.Stream != nil && settings.AvailableDropsCheck {
				pendingDrops = append(pendingDrops, chunk[index])
			}
		}

		if len(pendingDrops) > 0 {
			if err := t.fillDropsEnabledBatch(ctx, pendingDrops, fetched, settings, snapshot); err != nil {
				return err
			}
		}
	}

	for _, spec := range specs {
		result, ok := fetched[spec.ID]
		if !ok {
			continue
		}
		t.applyFetched(spec.ID, result)
	}

	return nil
}

func (t *Tracker) CheckOnline(channelID int64) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}

	t.mu.Lock()
	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		t.mu.Unlock()
		return ErrChannelNotTracked
	}
	if tracked.pendingCancel != nil {
		t.mu.Unlock()
		return nil
	}

	pendingCtx, cancel := context.WithCancel(t.ctx)
	tracked.pendingSeq++
	sequence := tracked.pendingSeq
	tracked.pendingCancel = cancel
	tracked.channel.PendingStream = true
	t.wg.Add(1)
	t.mu.Unlock()

	go func() {
		defer t.wg.Done()

		if err := t.sleep(pendingCtx, t.onlineDelay); err != nil {
			return
		}

		t.mu.Lock()
		tracked, ok := t.channels[channelID]
		if !ok || tracked == nil || tracked.channel == nil || tracked.pendingCancel == nil || tracked.pendingSeq != sequence {
			t.mu.Unlock()
			return
		}
		tracked.pendingCancel = nil
		tracked.channel.PendingStream = false
		t.mu.Unlock()

		_, _ = t.SyncChannel(t.ctx, channelID)
	}()

	return nil
}

func (t *Tracker) ProcessStreamState(ctx context.Context, channelID int64, message json.RawMessage) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}

	var parsed streamStateMessage
	if err := json.Unmarshal(message, &parsed); err != nil {
		return fmt.Errorf("解析 StreamState 消息失败: %w", err)
	}

	switch strings.TrimSpace(parsed.Type) {
	case "viewcount":
		channel, ok := t.Channel(channelID)
		if !ok {
			return ErrChannelNotTracked
		}
		if !channel.Online() {
			return t.CheckOnline(channelID)
		}
		t.mu.Lock()
		tracked := t.channels[channelID]
		if tracked != nil && tracked.channel != nil && tracked.channel.Stream != nil {
			tracked.channel.Stream.Viewers = parsed.Viewers
		}
		t.mu.Unlock()
		return nil
	case "stream-down":
		t.setOffline(channelID)
		return nil
	case "stream-up":
		return t.CheckOnline(channelID)
	case "commercial":
		return nil
	default:
		return nil
	}
}

func (t *Tracker) ProcessStreamUpdate(ctx context.Context, channelID int64, message json.RawMessage) error {
	if t == nil {
		return fmt.Errorf("watch 跟踪器未初始化")
	}
	if _, ok := t.Channel(channelID); !ok {
		return ErrChannelNotTracked
	}
	if len(message) > 0 && !json.Valid(message) {
		return fmt.Errorf("解析 StreamUpdate 消息失败: 无效 JSON")
	}

	return t.CheckOnline(channelID)
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

func (t *Tracker) lookupChannel(channelID int64) (channelSpec, config.Settings, inventory.Snapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return channelSpec{}, config.Settings{}, inventory.Snapshot{}, ErrChannelNotTracked
	}

	return channelSpec{
		ID:          tracked.channel.ID,
		Login:       tracked.channel.Login,
		DisplayName: tracked.channel.DisplayName,
		ACLBased:    tracked.channel.ACLBased,
	}, t.settings, t.inventory, nil
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

func (t *Tracker) collectChannels(channelIDs []int64) ([]channelSpec, config.Settings, inventory.Snapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	settings := t.settings
	snapshot := t.inventory
	if len(channelIDs) == 0 {
		specs := make([]channelSpec, 0, len(t.channels))
		for _, tracked := range t.channels {
			if tracked == nil || tracked.channel == nil {
				continue
			}
			specs = append(specs, channelSpec{
				ID:          tracked.channel.ID,
				Login:       tracked.channel.Login,
				DisplayName: tracked.channel.DisplayName,
				ACLBased:    tracked.channel.ACLBased,
			})
		}
		return specs, settings, snapshot, nil
	}

	specs := make([]channelSpec, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		tracked, ok := t.channels[channelID]
		if !ok || tracked == nil || tracked.channel == nil {
			return nil, config.Settings{}, inventory.Snapshot{}, ErrChannelNotTracked
		}
		specs = append(specs, channelSpec{
			ID:          tracked.channel.ID,
			Login:       tracked.channel.Login,
			DisplayName: tracked.channel.DisplayName,
			ACLBased:    tracked.channel.ACLBased,
		})
	}
	return specs, settings, snapshot, nil
}

func (t *Tracker) fetchChannel(ctx context.Context, spec channelSpec, settings config.Settings, snapshot inventory.Snapshot) (fetchedChannel, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	operation, err := gql.MustLookup(gql.OperationGetStreamInfo).WithVariables(map[string]any{
		"channel": spec.Login,
	})
	if err != nil {
		return fetchedChannel{}, fmt.Errorf("构造 GetStreamInfo 请求失败: %w", err)
	}

	response, err := t.gqlClient.Do(ctx, operation)
	if err != nil {
		return fetchedChannel{}, fmt.Errorf("请求 GetStreamInfo 失败: %w", err)
	}

	fetched, err := parseGetStreamInfoResponse(spec, response, settings.AvailableDropsCheck)
	if err != nil {
		return fetchedChannel{}, err
	}

	if fetched.Stream == nil || !settings.AvailableDropsCheck {
		return fetched, nil
	}

	available, err := gql.MustLookup(gql.OperationAvailableDrops).WithVariables(map[string]any{
		"channelID": strconv.FormatInt(spec.ID, 10),
	})
	if err != nil {
		return fetchedChannel{}, fmt.Errorf("构造 AvailableDrops 请求失败: %w", err)
	}

	response, err = t.gqlClient.Do(ctx, available)
	if err != nil {
		return fetched, nil
	}

	campaignIDs, err := parseAvailableDropsResponse(response)
	if err != nil {
		return fetchedChannel{}, err
	}

	channel := &domain.Channel{
		ID:          spec.ID,
		Login:       spec.Login,
		DisplayName: firstNonEmpty(fetched.DisplayName, spec.DisplayName),
		Stream:      cloneStream(fetched.Stream),
		ACLBased:    spec.ACLBased,
	}
	fetched.Stream.DropsEnabled = dropsEnabled(t.now().UTC(), settings, snapshot, channel, campaignIDs)
	return fetched, nil
}

func (t *Tracker) fillDropsEnabledBatch(ctx context.Context, specs []channelSpec, fetched map[int64]fetchedChannel, settings config.Settings, snapshot inventory.Snapshot) error {
	operations := make([]gql.Operation, 0, len(specs))
	for _, spec := range specs {
		operation, err := gql.MustLookup(gql.OperationAvailableDrops).WithVariables(map[string]any{
			"channelID": strconv.FormatInt(spec.ID, 10),
		})
		if err != nil {
			return fmt.Errorf("构造 AvailableDrops 请求失败: %w", err)
		}
		operations = append(operations, operation)
	}

	responses, err := t.gqlClient.DoBatch(ctx, operations)
	if err != nil {
		return fmt.Errorf("批量请求 AvailableDrops 失败: %w", err)
	}

	for index, response := range responses {
		campaignIDs, err := parseAvailableDropsResponse(response)
		if err != nil {
			return err
		}

		result := fetched[specs[index].ID]
		if result.Stream == nil {
			continue
		}
		channel := &domain.Channel{
			ID:          specs[index].ID,
			Login:       specs[index].Login,
			DisplayName: firstNonEmpty(result.DisplayName, specs[index].DisplayName),
			Stream:      cloneStream(result.Stream),
			ACLBased:    specs[index].ACLBased,
		}
		result.Stream.DropsEnabled = dropsEnabled(t.now().UTC(), settings, snapshot, channel, campaignIDs)
		fetched[specs[index].ID] = result
	}

	return nil
}

func parseGetStreamInfoResponse(spec channelSpec, response gql.Response, availableDropsCheck bool) (fetchedChannel, error) {
	userValue, err := nestedMap(response.Data, "data", "user")
	if err != nil {
		return fetchedChannel{}, err
	}
	if userValue == nil {
		return fetchedChannel{DisplayName: spec.DisplayName}, nil
	}

	displayName := firstNonEmpty(stringValue(userValue, "displayName"), spec.DisplayName)
	streamValue, exists := userValue["stream"]
	if !exists || isNilValue(streamValue) {
		return fetchedChannel{DisplayName: displayName}, nil
	}

	streamData, err := asMap(streamValue, "data.user.stream")
	if err != nil {
		return fetchedChannel{}, err
	}
	settingsData, err := mapFromMap(userValue, "broadcastSettings")
	if err != nil {
		return fetchedChannel{}, err
	}

	stream := &domain.Stream{
		BroadcastID:  int64Value(streamData, "id"),
		Game:         parseGame(optionalMap(settingsData["game"])),
		Viewers:      int(int64Value(streamData, "viewersCount")),
		Title:        stringValue(settingsData, "title"),
		DropsEnabled: !availableDropsCheck,
	}

	return fetchedChannel{
		DisplayName: displayName,
		Stream:      stream,
	}, nil
}

func parseAvailableDropsResponse(response gql.Response) ([]string, error) {
	channelValue, err := nestedMap(response.Data, "data", "channel")
	if err != nil {
		return nil, err
	}
	if channelValue == nil {
		return nil, nil
	}

	dropsValue, ok := channelValue["viewerDropCampaigns"]
	if !ok || isNilValue(dropsValue) {
		return nil, nil
	}

	items, err := asSlice(dropsValue, "data.channel.viewerDropCampaigns")
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(items))
	for index, item := range items {
		campaignData, err := asMap(item, fmt.Sprintf("data.channel.viewerDropCampaigns[%d]", index))
		if err != nil {
			return nil, err
		}
		if campaignID := stringValue(campaignData, "id"); campaignID != "" {
			ids = append(ids, campaignID)
		}
	}

	return ids, nil
}

func dropsEnabled(now time.Time, settings config.Settings, snapshot inventory.Snapshot, channel *domain.Channel, availableCampaignIDs []string) bool {
	for _, campaignID := range availableCampaignIDs {
		campaign := snapshot.Campaigns[campaignID]
		if campaign == nil {
			continue
		}
		if campaign.CanEarn(now, channel, settings.EnableBadgesEmotes, true) {
			return true
		}
	}
	return false
}

func (t *Tracker) applyFetched(channelID int64, fetched fetchedChannel) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return
	}

	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
		tracked.pendingCancel = nil
	}

	tracked.channel.PendingStream = false
	if fetched.DisplayName != "" {
		tracked.channel.DisplayName = fetched.DisplayName
	}
	tracked.channel.Stream = cloneStream(fetched.Stream)
}

func (t *Tracker) setOffline(channelID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return
	}
	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
		tracked.pendingCancel = nil
	}
	tracked.channel.PendingStream = false
	tracked.channel.Stream = nil
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

func chunkSpecs(specs []channelSpec, size int) [][]channelSpec {
	if len(specs) == 0 {
		return nil
	}
	if size <= 0 {
		size = len(specs)
	}

	chunks := make([][]channelSpec, 0, (len(specs)+size-1)/size)
	for start := 0; start < len(specs); start += size {
		end := start + size
		if end > len(specs) {
			end = len(specs)
		}
		chunk := append([]channelSpec(nil), specs[start:end]...)
		chunks = append(chunks, chunk)
	}
	return chunks
}

func cloneChannel(channel *domain.Channel) domain.Channel {
	if channel == nil {
		return domain.Channel{}
	}

	cloned := *channel
	cloned.Stream = cloneStream(channel.Stream)
	return cloned
}

func cloneStream(stream *domain.Stream) *domain.Stream {
	if stream == nil {
		return nil
	}

	cloned := *stream
	if stream.Game != nil {
		game := *stream.Game
		cloned.Game = &game
	}
	return &cloned
}

func parseGame(data map[string]any) *domain.Game {
	if len(data) == 0 {
		return nil
	}

	name := firstNonEmpty(stringValue(data, "displayName"), stringValue(data, "name"))
	if name == "" && int64Value(data, "id") == 0 {
		return nil
	}

	game := domain.Game{
		ID:       int64Value(data, "id"),
		Name:     name,
		SlugText: stringValue(data, "slug"),
	}
	return &game
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

func nestedMap(root any, label string, path ...string) (map[string]any, error) {
	current := root
	currentPath := label
	for _, part := range path {
		currentMap, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s 的父节点不是对象", currentPath)
		}
		value, exists := currentMap[part]
		if !exists {
			return nil, fmt.Errorf("缺少字段 %s.%s", currentPath, part)
		}
		currentPath += "." + part
		current = value
		if isNilValue(current) {
			return nil, nil
		}
	}

	return asMap(current, currentPath)
}

func asMap(value any, label string) (map[string]any, error) {
	mapValue, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是对象", label)
	}
	return mapValue, nil
}

func optionalMap(value any) map[string]any {
	mapValue, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return mapValue
}

func mapFromMap(source map[string]any, key string) (map[string]any, error) {
	value, ok := source[key]
	if !ok {
		return nil, fmt.Errorf("缺少字段 %q", key)
	}
	return asMap(value, key)
}

func asSlice(value any, label string) ([]any, error) {
	sliceValue, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是数组", label)
	}
	return sliceValue, nil
}

func stringValue(source map[string]any, key string) string {
	if len(source) == 0 {
		return ""
	}

	value, ok := source[key]
	if !ok || value == nil {
		return ""
	}

	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func int64Value(source map[string]any, key string) int64 {
	if len(source) == 0 {
		return 0
	}

	value, ok := source[key]
	if !ok || value == nil {
		return 0
	}

	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}

	return 0
}

func isNilValue(value any) bool {
	return value == nil
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
