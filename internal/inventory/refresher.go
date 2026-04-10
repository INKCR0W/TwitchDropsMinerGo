package inventory

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
)

const defaultChunkSize = 20

var boxArtDimensionsPattern = regexp.MustCompile(`-\d+x\d+(\.(?:jpg|png|gif))$`)

type GQLClient interface {
	Do(context.Context, gql.Operation) (gql.Response, error)
	DoBatch(context.Context, []gql.Operation) ([]gql.Response, error)
}

type AuthState interface {
	Validate(context.Context) error
	Snapshot() auth.Snapshot
}

type Options struct {
	GQLClient GQLClient
	AuthState AuthState
	Clock     func() time.Time
	ChunkSize int
}

type RefreshOptions struct {
	EnableBadgesEmotes bool
}

type Snapshot struct {
	Inventory           []*domain.DropsCampaign
	Campaigns           map[string]*domain.DropsCampaign
	Drops               map[string]*domain.TimedDrop
	MaintenanceTriggers []time.Time
}

type Refresher struct {
	gqlClient GQLClient
	authState AuthState
	now       func() time.Time
	chunkSize int
}

type campaignEnvelope struct {
	ID   string
	Data map[string]any
}

type campaignChunkResult struct {
	Data map[string]any
	Err  error
}

func NewRefresher(options Options) (*Refresher, error) {
	if options.GQLClient == nil {
		return nil, fmt.Errorf("inventory GQL 客户端不能为空")
	}
	if options.AuthState == nil {
		return nil, fmt.Errorf("inventory 认证状态不能为空")
	}

	now := options.Clock
	if now == nil {
		now = time.Now
	}

	chunkSize := options.ChunkSize
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	return &Refresher{
		gqlClient: options.GQLClient,
		authState: options.AuthState,
		now:       now,
		chunkSize: chunkSize,
	}, nil
}

func (r *Refresher) Refresh(ctx context.Context, options RefreshOptions) (Snapshot, error) {
	if r == nil {
		return Snapshot{}, fmt.Errorf("inventory 刷新器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if err := r.authState.Validate(ctx); err != nil {
		return Snapshot{}, fmt.Errorf("校验认证状态失败: %w", err)
	}

	authSnapshot := r.authState.Snapshot()
	if authSnapshot.UserID == 0 {
		return Snapshot{}, fmt.Errorf("认证状态缺少 user_id")
	}
	now := r.now().UTC()

	inventoryPayload, claimedBenefits, err := r.fetchInventoryPayload(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	availableCampaigns, err := r.fetchAvailableCampaigns(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	mergedPayload, err := r.fetchCampaignDetails(ctx, authSnapshot.UserID, inventoryPayload, availableCampaigns)
	if err != nil {
		return Snapshot{}, err
	}

	campaigns, err := buildCampaigns(mergedPayload, claimedBenefits)
	if err != nil {
		return Snapshot{}, err
	}
	sortCampaigns(campaigns, now, options.EnableBadgesEmotes)

	return buildSnapshot(campaigns, now, options), nil
}

func (r *Refresher) fetchInventoryPayload(ctx context.Context) (map[string]any, map[string]time.Time, error) {
	response, err := r.gqlClient.Do(ctx, gql.MustLookup(gql.OperationInventory))
	if err != nil {
		return nil, nil, fmt.Errorf("请求 Inventory 失败: %w", err)
	}

	inventoryRoot, err := nestedValue(response.Data, "currentUser", "inventory")
	if err != nil {
		return nil, nil, fmt.Errorf("解析 Inventory 响应失败: %w", err)
	}

	inventoryMap, err := asMap(inventoryRoot, "Inventory.currentUser.inventory")
	if err != nil {
		return nil, nil, err
	}

	ongoingCampaigns, err := sliceFromMap(inventoryMap, "dropCampaignsInProgress")
	if err != nil {
		return nil, nil, err
	}
	inventoryPayload, err := campaignsByID(ongoingCampaigns)
	if err != nil {
		return nil, nil, err
	}

	gameEventDrops, err := sliceFromMap(inventoryMap, "gameEventDrops")
	if err != nil {
		return nil, nil, err
	}
	claimedBenefits, err := parseClaimedBenefits(gameEventDrops)
	if err != nil {
		return nil, nil, err
	}

	return inventoryPayload, claimedBenefits, nil
}

func (r *Refresher) fetchAvailableCampaigns(ctx context.Context) ([]campaignEnvelope, error) {
	response, err := r.gqlClient.Do(ctx, gql.MustLookup(gql.OperationCampaigns))
	if err != nil {
		return nil, fmt.Errorf("请求 Campaigns 失败: %w", err)
	}

	campaignsRoot, err := nestedValue(response.Data, "currentUser", "dropCampaigns")
	if err != nil {
		return nil, fmt.Errorf("解析 Campaigns 响应失败: %w", err)
	}

	campaignList, err := asSlice(campaignsRoot, "Campaigns.currentUser.dropCampaigns")
	if err != nil {
		return nil, err
	}

	applicableStatuses := map[string]struct{}{
		"ACTIVE":   {},
		"UPCOMING": {},
	}

	availableCampaigns := make([]campaignEnvelope, 0, len(campaignList))
	for index, item := range campaignList {
		campaignData, err := asMap(item, fmt.Sprintf("Campaigns.currentUser.dropCampaigns[%d]", index))
		if err != nil {
			return nil, err
		}
		if _, ok := applicableStatuses[strings.ToUpper(stringValue(campaignData, "status"))]; !ok {
			continue
		}

		campaignID := stringValue(campaignData, "id")
		if campaignID == "" {
			return nil, fmt.Errorf("Campaigns.currentUser.dropCampaigns[%d].id 不能为空", index)
		}

		availableCampaigns = append(availableCampaigns, campaignEnvelope{
			ID:   campaignID,
			Data: cloneMap(campaignData),
		})
	}

	return availableCampaigns, nil
}

func (r *Refresher) fetchCampaignDetails(ctx context.Context, userID int64, inventoryPayload map[string]any, availableCampaigns []campaignEnvelope) (map[string]any, error) {
	if len(availableCampaigns) == 0 {
		return cloneMap(inventoryPayload), nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	chunks := splitCampaignChunks(availableCampaigns, r.chunkSize)
	results := make(chan campaignChunkResult, len(chunks))
	for _, chunk := range chunks {
		chunk := chunk
		go func() {
			data, err := r.fetchCampaignChunk(ctx, userID, chunk)
			results <- campaignChunkResult{Data: data, Err: err}
		}()
	}

	mergedPayload := cloneMap(inventoryPayload)
	var firstErr error
	for range chunks {
		result := <-results
		if result.Err != nil {
			if firstErr == nil {
				firstErr = result.Err
				cancel()
			}
			continue
		}

		if firstErr != nil {
			continue
		}

		mergedPayload, firstErr = mergeMaps(mergedPayload, result.Data)
		if firstErr != nil {
			cancel()
		}
	}

	if firstErr != nil {
		return nil, firstErr
	}

	return mergedPayload, nil
}

func (r *Refresher) fetchCampaignChunk(ctx context.Context, userID int64, chunk []campaignEnvelope) (map[string]any, error) {
	operations := make([]gql.Operation, 0, len(chunk))
	for _, campaign := range chunk {
		operation, err := gql.MustLookup(gql.OperationCampaignDetails).WithVariables(map[string]any{
			"channelLogin": strconv.FormatInt(userID, 10),
			"dropID":       campaign.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("构造 CampaignDetails 请求失败: %w", err)
		}
		operations = append(operations, operation)
	}

	responses, err := r.gqlClient.DoBatch(ctx, operations)
	if err != nil {
		return nil, fmt.Errorf("请求 CampaignDetails 失败: %w", err)
	}

	detailsPayload := make(map[string]any, len(chunk))
	for index, response := range responses {
		campaignValue, err := nestedValue(response.Data, "user", "dropCampaign")
		if err != nil {
			return nil, fmt.Errorf("解析 CampaignDetails[%d] 响应失败: %w", index, err)
		}
		campaignData, err := asMap(campaignValue, fmt.Sprintf("CampaignDetails[%d].user.dropCampaign", index))
		if err != nil {
			return nil, err
		}

		campaignID := stringValue(campaignData, "id")
		if campaignID == "" {
			return nil, fmt.Errorf("CampaignDetails[%d].user.dropCampaign.id 不能为空", index)
		}
		detailsPayload[campaignID] = cloneMap(campaignData)
	}

	availablePayload := make(map[string]any, len(chunk))
	for _, campaign := range chunk {
		availablePayload[campaign.ID] = cloneMap(campaign.Data)
	}

	mergedChunk, err := mergeMaps(availablePayload, detailsPayload)
	if err != nil {
		return nil, fmt.Errorf("合并 Campaigns 与 CampaignDetails 数据失败: %w", err)
	}

	return mergedChunk, nil
}

func buildCampaigns(payload map[string]any, claimedBenefits map[string]time.Time) ([]*domain.DropsCampaign, error) {
	campaigns := make([]*domain.DropsCampaign, 0, len(payload))
	for campaignID, rawCampaign := range payload {
		campaignData, err := asMap(rawCampaign, "campaign."+campaignID)
		if err != nil {
			return nil, err
		}
		if isNilValue(campaignData["game"]) {
			continue
		}

		campaign, err := buildCampaign(campaignData, claimedBenefits)
		if err != nil {
			return nil, fmt.Errorf("构造 campaign %q 失败: %w", campaignID, err)
		}
		campaigns = append(campaigns, campaign)
	}

	return campaigns, nil
}

func buildCampaign(data map[string]any, claimedBenefits map[string]time.Time) (*domain.DropsCampaign, error) {
	gameData, err := mapFromMap(data, "game")
	if err != nil {
		return nil, err
	}

	selfData := optionalMap(data["self"])
	spec := domain.CampaignSpec{
		ID:              stringValue(data, "id"),
		Name:            stringValue(data, "name"),
		Game:            parseGame(gameData),
		Linked:          boolValue(selfData, "isAccountConnected"),
		LinkURL:         stringValue(data, "accountLinkURL"),
		ImageURL:        normalizeBoxArtURL(stringValue(gameData, "boxArtURL")),
		StartsAt:        timeValue(data, "startAt"),
		EndsAt:          timeValue(data, "endAt"),
		Status:          stringValue(data, "status"),
		AllowedChannels: parseAllowedChannels(data["allow"]),
	}
	if spec.ID == "" {
		return nil, fmt.Errorf("campaign id 不能为空")
	}
	if spec.Name == "" {
		return nil, fmt.Errorf("campaign %q name 不能为空", spec.ID)
	}
	if spec.StartsAt.IsZero() || spec.EndsAt.IsZero() {
		return nil, fmt.Errorf("campaign %q 缺少开始或结束时间", spec.ID)
	}

	dropValues, err := sliceFromMap(data, "timeBasedDrops")
	if err != nil {
		return nil, err
	}
	spec.Drops, err = parseDrops(dropValues, claimedBenefits)
	if err != nil {
		return nil, err
	}

	return domain.NewCampaign(spec)
}

func parseGame(data map[string]any) domain.Game {
	name := stringValue(data, "displayName")
	if name == "" {
		name = stringValue(data, "name")
	}

	return domain.Game{
		ID:       int64Value(data, "id"),
		Name:     name,
		SlugText: stringValue(data, "slug"),
	}
}

func parseAllowedChannels(value any) []domain.Channel {
	allowData := optionalMap(value)
	if len(allowData) == 0 || !allowEnabled(allowData) {
		return nil
	}

	channelsValue, ok := allowData["channels"]
	if !ok || channelsValue == nil {
		return nil
	}

	channelList, ok := channelsValue.([]any)
	if !ok {
		return nil
	}

	channels := make([]domain.Channel, 0, len(channelList))
	for _, item := range channelList {
		channelData := optionalMap(item)
		if len(channelData) == 0 {
			continue
		}

		channelID := int64Value(channelData, "id")
		login := stringValue(channelData, "name")
		if channelID == 0 || login == "" {
			continue
		}

		channels = append(channels, domain.Channel{
			ID:          channelID,
			Login:       login,
			DisplayName: stringValue(channelData, "displayName"),
			ACLBased:    true,
		})
	}

	return channels
}

func allowEnabled(data map[string]any) bool {
	if len(data) == 0 {
		return false
	}
	value, ok := data["isEnabled"]
	if !ok {
		return true
	}

	enabled, ok := value.(bool)
	if !ok {
		return true
	}
	return enabled
}

func parseDrops(values []any, claimedBenefits map[string]time.Time) ([]domain.TimedDropSpec, error) {
	drops := make([]domain.TimedDropSpec, 0, len(values))
	for index, item := range values {
		dropData, err := asMap(item, fmt.Sprintf("timeBasedDrops[%d]", index))
		if err != nil {
			return nil, err
		}

		drop, err := parseDrop(dropData, claimedBenefits)
		if err != nil {
			return nil, err
		}
		drops = append(drops, drop)
	}

	return drops, nil
}

func parseDrop(data map[string]any, claimedBenefits map[string]time.Time) (domain.TimedDropSpec, error) {
	startsAt := timeValue(data, "startAt")
	endsAt := timeValue(data, "endAt")
	benefits, err := parseBenefits(data["benefitEdges"])
	if err != nil {
		return domain.TimedDropSpec{}, err
	}

	selfData := optionalMap(data["self"])
	isClaimed := boolValue(selfData, "isClaimed")
	if len(selfData) == 0 {
		isClaimed = inferClaimedByBenefits(benefits, claimedBenefits, startsAt, endsAt)
	}

	spec := domain.TimedDropSpec{
		ID:                  stringValue(data, "id"),
		Name:                stringValue(data, "name"),
		Benefits:            benefits,
		StartsAt:            startsAt,
		EndsAt:              endsAt,
		ClaimID:             stringValue(selfData, "dropInstanceID"),
		IsClaimed:           isClaimed,
		PreconditionDropIDs: parsePreconditionDropIDs(data["preconditionDrops"]),
		RealCurrentMinutes:  intValue(selfData, "currentMinutesWatched"),
		RequiredMinutes:     intValue(data, "requiredMinutesWatched"),
	}
	if spec.ID == "" {
		return domain.TimedDropSpec{}, fmt.Errorf("drop id 不能为空")
	}
	if spec.StartsAt.IsZero() || spec.EndsAt.IsZero() {
		return domain.TimedDropSpec{}, fmt.Errorf("drop %q 缺少开始或结束时间", spec.ID)
	}

	return spec, nil
}

func parseBenefits(value any) ([]domain.Benefit, error) {
	if value == nil {
		return nil, nil
	}

	edges, err := asSlice(value, "benefitEdges")
	if err != nil {
		return nil, err
	}

	benefits := make([]domain.Benefit, 0, len(edges))
	for index, edge := range edges {
		edgeData, err := asMap(edge, fmt.Sprintf("benefitEdges[%d]", index))
		if err != nil {
			return nil, err
		}
		benefitData, err := mapFromMap(edgeData, "benefit")
		if err != nil {
			return nil, err
		}

		benefits = append(benefits, domain.Benefit{
			ID:       stringValue(benefitData, "id"),
			Name:     stringValue(benefitData, "name"),
			Type:     domain.ParseBenefitType(stringValue(benefitData, "distributionType")),
			ImageURL: stringValue(benefitData, "imageAssetURL"),
		})
	}

	return benefits, nil
}

func inferClaimedByBenefits(benefits []domain.Benefit, claimedBenefits map[string]time.Time, startsAt time.Time, endsAt time.Time) bool {
	matched := false
	for _, benefit := range benefits {
		awardedAt, ok := claimedBenefits[benefit.ID]
		if !ok {
			continue
		}
		matched = true
		if awardedAt.Before(startsAt) || !awardedAt.Before(endsAt) {
			return false
		}
	}

	return matched
}

func parsePreconditionDropIDs(value any) []string {
	dropList, ok := value.([]any)
	if !ok {
		return nil
	}

	result := make([]string, 0, len(dropList))
	for _, item := range dropList {
		dropData := optionalMap(item)
		if dropID := stringValue(dropData, "id"); dropID != "" {
			result = append(result, dropID)
		}
	}

	return result
}

func parseClaimedBenefits(values []any) (map[string]time.Time, error) {
	claimedBenefits := make(map[string]time.Time, len(values))
	for index, item := range values {
		benefitData, err := asMap(item, fmt.Sprintf("gameEventDrops[%d]", index))
		if err != nil {
			return nil, err
		}

		benefitID := stringValue(benefitData, "id")
		if benefitID == "" {
			return nil, fmt.Errorf("gameEventDrops[%d].id 不能为空", index)
		}

		awardedAt := timeValue(benefitData, "lastAwardedAt")
		if awardedAt.IsZero() {
			return nil, fmt.Errorf("gameEventDrops[%d].lastAwardedAt 不能为空", index)
		}

		claimedBenefits[benefitID] = awardedAt
	}

	return claimedBenefits, nil
}

func buildSnapshot(campaigns []*domain.DropsCampaign, now time.Time, options RefreshOptions) Snapshot {
	snapshot := Snapshot{
		Inventory: make([]*domain.DropsCampaign, 0, len(campaigns)),
		Campaigns: make(map[string]*domain.DropsCampaign, len(campaigns)),
		Drops:     make(map[string]*domain.TimedDrop),
	}

	nextHour := now.Add(time.Hour)
	triggerSet := make(map[time.Time]struct{})
	for _, campaign := range campaigns {
		if campaign == nil {
			continue
		}

		snapshot.Inventory = append(snapshot.Inventory, campaign)
		snapshot.Campaigns[campaign.ID] = campaign
		for _, drop := range campaign.Drops() {
			snapshot.Drops[drop.ID] = drop
		}

		if campaign.CanEarnWithin(now, nextHour, options.EnableBadgesEmotes) {
			for _, trigger := range campaign.TimeTriggers() {
				if trigger.After(now) {
					triggerSet[trigger] = struct{}{}
				}
			}
		}
	}

	snapshot.MaintenanceTriggers = make([]time.Time, 0, len(triggerSet))
	for trigger := range triggerSet {
		snapshot.MaintenanceTriggers = append(snapshot.MaintenanceTriggers, trigger)
	}
	sort.Slice(snapshot.MaintenanceTriggers, func(i int, j int) bool {
		return snapshot.MaintenanceTriggers[i].Before(snapshot.MaintenanceTriggers[j])
	})

	return snapshot
}

func sortCampaigns(campaigns []*domain.DropsCampaign, now time.Time, enableBadgesEmotes bool) {
	sort.SliceStable(campaigns, func(i int, j int) bool {
		return campaigns[i].ID < campaigns[j].ID
	})
	sort.SliceStable(campaigns, func(i int, j int) bool {
		return campaigns[i].ActiveAt(now) && !campaigns[j].ActiveAt(now)
	})
	sort.SliceStable(campaigns, func(i int, j int) bool {
		return campaignSortTime(campaigns[i], now).Before(campaignSortTime(campaigns[j], now))
	})
	sort.SliceStable(campaigns, func(i int, j int) bool {
		return campaigns[i].Eligible(enableBadgesEmotes) && !campaigns[j].Eligible(enableBadgesEmotes)
	})
}

func campaignSortTime(campaign *domain.DropsCampaign, now time.Time) time.Time {
	if campaign != nil && campaign.UpcomingAt(now) {
		return campaign.StartsAt
	}
	if campaign == nil {
		return time.Time{}
	}
	return campaign.EndsAt
}

func splitCampaignChunks(campaigns []campaignEnvelope, chunkSize int) [][]campaignEnvelope {
	if len(campaigns) == 0 {
		return nil
	}
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}

	chunks := make([][]campaignEnvelope, 0, (len(campaigns)+chunkSize-1)/chunkSize)
	for start := 0; start < len(campaigns); start += chunkSize {
		end := start + chunkSize
		if end > len(campaigns) {
			end = len(campaigns)
		}
		chunk := make([]campaignEnvelope, end-start)
		copy(chunk, campaigns[start:end])
		chunks = append(chunks, chunk)
	}

	return chunks
}

func campaignsByID(values []any) (map[string]any, error) {
	campaigns := make(map[string]any, len(values))
	for index, item := range values {
		campaignData, err := asMap(item, fmt.Sprintf("campaigns[%d]", index))
		if err != nil {
			return nil, err
		}

		campaignID := stringValue(campaignData, "id")
		if campaignID == "" {
			return nil, fmt.Errorf("campaigns[%d].id 不能为空", index)
		}
		campaigns[campaignID] = cloneMap(campaignData)
	}

	return campaigns, nil
}

func mergeMaps(primary map[string]any, secondary map[string]any) (map[string]any, error) {
	merged := make(map[string]any, len(primary)+len(secondary))
	for key, value := range primary {
		if secondaryValue, ok := secondary[key]; ok {
			mergedValue, err := mergeValues(value, secondaryValue)
			if err != nil {
				return nil, err
			}
			merged[key] = mergedValue
			continue
		}
		merged[key] = cloneValue(value)
	}
	for key, value := range secondary {
		if _, exists := primary[key]; exists {
			continue
		}
		merged[key] = cloneValue(value)
	}

	return merged, nil
}

func mergeValues(primary any, secondary any) (any, error) {
	primaryNil := isNilValue(primary)
	secondaryNil := isNilValue(secondary)
	if primaryNil || secondaryNil {
		if primaryNil && secondaryNil {
			return nil, nil
		}
		return nil, fmt.Errorf("合并数据类型不一致: %T vs %T", primary, secondary)
	}

	if reflect.TypeOf(primary) != reflect.TypeOf(secondary) {
		return nil, fmt.Errorf("合并数据类型不一致: %T vs %T", primary, secondary)
	}

	switch typed := primary.(type) {
	case map[string]any:
		return mergeMaps(typed, secondary.(map[string]any))
	case []any:
		return cloneSlice(typed), nil
	default:
		return cloneValue(primary), nil
	}
}

func cloneMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}

	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneSlice(source []any) []any {
	cloned := make([]any, len(source))
	for index, value := range source {
		cloned[index] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		return cloneSlice(typed)
	default:
		return typed
	}
}

func nestedValue(root any, path ...string) (any, error) {
	current := root
	for _, part := range path {
		currentMap, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("路径 %q 的父节点不是对象", strings.Join(path, "."))
		}

		value, exists := currentMap[part]
		if !exists {
			return nil, fmt.Errorf("缺少字段 %q", strings.Join(path, "."))
		}
		current = value
	}

	return current, nil
}

func asMap(value any, label string) (map[string]any, error) {
	mapValue, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s 不是对象", label)
	}
	return mapValue, nil
}

func asSlice(value any, label string) ([]any, error) {
	if sliceValue, ok := value.([]any); ok {
		return sliceValue, nil
	}

	reflectValue := reflect.ValueOf(value)
	if !reflectValue.IsValid() {
		return nil, fmt.Errorf("%s 不是数组", label)
	}
	if reflectValue.Kind() != reflect.Slice && reflectValue.Kind() != reflect.Array {
		return nil, fmt.Errorf("%s 不是数组", label)
	}

	result := make([]any, reflectValue.Len())
	for index := range result {
		result[index] = reflectValue.Index(index).Interface()
	}
	return result, nil
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

func sliceFromMap(source map[string]any, key string) ([]any, error) {
	value, ok := source[key]
	if !ok || value == nil {
		return nil, nil
	}
	return asSlice(value, key)
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
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func boolValue(source map[string]any, key string) bool {
	if len(source) == 0 {
		return false
	}

	value, ok := source[key]
	if !ok || value == nil {
		return false
	}

	typed, ok := value.(bool)
	return ok && typed
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

func intValue(source map[string]any, key string) int {
	return int(int64Value(source, key))
}

func timeValue(source map[string]any, key string) time.Time {
	raw := stringValue(source, key)
	if raw == "" {
		return time.Time{}
	}

	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func normalizeBoxArtURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	return boxArtDimensionsPattern.ReplaceAllString(rawURL, "$1")
}

func isNilValue(value any) bool {
	if value == nil {
		return true
	}

	reflectValue := reflect.ValueOf(value)
	switch reflectValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflectValue.IsNil()
	default:
		return false
	}
}
