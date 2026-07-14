package main

import (
	"context"
	"fmt"
	"time"

	"twitchdropsminergo/internal/app"
	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/pubsub"
	"twitchdropsminergo/internal/scheduler"
)

const (
	defaultStateSyncInterval = time.Second
	unhealthyErrorThreshold  = 5 * time.Minute
)

type localStateTarget interface {
	Settings() config.Settings
	UpdateObservation(app.Observation) error
}

type authSnapshotProvider interface {
	Snapshot() auth.Snapshot
}

type schedulerStateProvider interface {
	StatusSnapshot() scheduler.StatusSnapshot
	ActiveCampaign(*domain.Channel) *domain.DropsCampaign
}

type localStateSync struct {
	application localStateTarget
	auth        authSnapshotProvider
	scheduler   schedulerStateProvider
	interval    time.Duration
	now         func() time.Time
}

func newLocalStateSync(application localStateTarget, authState authSnapshotProvider, schedulerState schedulerStateProvider) *localStateSync {
	return &localStateSync{
		application: application,
		auth:        authState,
		scheduler:   schedulerState,
		interval:    defaultStateSyncInterval,
		now:         time.Now,
	}
}

func (s *localStateSync) Run(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("本地状态同步器未初始化")
	}
	if ctx == nil {
		return fmt.Errorf("本地状态同步器运行上下文不能为空")
	}
	if s.application == nil {
		return fmt.Errorf("本地状态同步缺少应用状态目标")
	}
	if s.auth == nil {
		return fmt.Errorf("本地状态同步缺少认证快照源")
	}
	if s.scheduler == nil {
		return fmt.Errorf("本地状态同步缺少调度快照源")
	}
	if s.interval <= 0 {
		s.interval = defaultStateSyncInterval
	}
	if s.now == nil {
		s.now = time.Now
	}

	// 状态文件只是本地观测输出，写入失败不应停止主调度；后续 tick 会继续重试。
	_ = s.sync()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_ = s.sync()
		}
	}
}

func (s *localStateSync) sync() error {
	observation := buildRuntimeObservation(
		s.application.Settings(),
		s.auth.Snapshot(),
		s.scheduler.StatusSnapshot(),
		s.scheduler.ActiveCampaign(nil),
		s.now().UTC(),
	)
	return s.application.UpdateObservation(observation)
}

func buildRuntimeObservation(settings config.Settings, authSnapshot auth.Snapshot, schedulerSnapshot scheduler.StatusSnapshot, activeCampaign *domain.DropsCampaign, now time.Time) app.Observation {
	effectiveSettings := settings.Clone()
	if effectiveSettings.IsZero() {
		effectiveSettings = config.DefaultSettings()
	}
	if err := effectiveSettings.Validate(); err != nil {
		effectiveSettings = config.DefaultSettings()
		_ = effectiveSettings.Validate()
	}

	healthy := authSnapshot.UserID > 0 &&
		(schedulerSnapshot.LastError == "" || now.Sub(schedulerSnapshot.ErrorSince) < unhealthyErrorThreshold)

	observation := app.Observation{
		Healthy:   healthy,
		Heartbeat: now.Truncate(time.Minute),
		Auth: app.AuthStatus{
			LoggedIn: authSnapshot.UserID > 0,
			UserID:   authSnapshot.UserID,
		},
		Schedule: app.ScheduleStatus{
			State:                  string(schedulerSnapshot.State),
			WantedGames:            convertGames(schedulerSnapshot.WantedGames),
			WatchingChannelID:      schedulerSnapshot.WatchingChannelID,
			SelectedChannelID:      schedulerSnapshot.SelectedChannelID,
			FullCleanup:            schedulerSnapshot.FullCleanup,
			LastProgressAt:         schedulerSnapshot.LastProgressAt,
			LastError:              schedulerSnapshot.LastError,
			ErrorSince:             schedulerSnapshot.ErrorSince,
			ChannelCount:           len(schedulerSnapshot.Channels),
			Channels:               convertChannels(schedulerSnapshot.Channels),
			InventoryCampaignCount: schedulerSnapshot.InventoryCampaignCount,
			InventoryDropCount:     schedulerSnapshot.InventoryDropCount,
			UserTopicUserID:        schedulerSnapshot.UserTopicUserID,
			PubSub:                 convertPubSubStatus(schedulerSnapshot.PubSub),
			ActiveCampaign:         convertCampaign(activeCampaign),
			ActiveDrop:             convertDrop(now, effectiveSettings, activeCampaign),
		},
		Settings: effectiveSettings.Sanitized(),
	}

	return observation
}

func convertGames(games []domain.Game) []app.GameStatus {
	converted := make([]app.GameStatus, 0, len(games))
	for _, game := range games {
		converted = append(converted, app.GameStatus{
			ID:   game.ID,
			Name: game.Name,
			Slug: game.Slug(),
		})
	}
	return converted
}

func convertChannels(channels []domain.Channel) []app.ChannelStatus {
	converted := make([]app.ChannelStatus, 0, len(channels))
	for _, channel := range channels {
		status := app.ChannelStatus{
			ID:            channel.ID,
			Login:         channel.Login,
			DisplayName:   channel.DisplayName,
			ACLBased:      channel.ACLBased,
			PendingStream: channel.PendingStream,
			Online:        channel.Online(),
		}
		if channel.Stream != nil {
			status.Stream = &app.StreamStatus{
				BroadcastID:  channel.Stream.BroadcastID,
				Viewers:      channel.Stream.Viewers,
				Title:        channel.Stream.Title,
				DropsEnabled: channel.Stream.DropsEnabled,
			}
			if channel.Stream.Game != nil {
				status.Stream.Game = &app.GameStatus{
					ID:   channel.Stream.Game.ID,
					Name: channel.Stream.Game.Name,
					Slug: channel.Stream.Game.Slug(),
				}
			}
		}
		converted = append(converted, status)
	}
	return converted
}

func convertPubSubStatus(status pubsub.Status) app.PubSubStatus {
	converted := app.PubSubStatus{
		Running:    status.Running,
		Endpoint:   status.Endpoint,
		TopicCount: status.TopicCount,
		Shards:     make([]app.PubSubShardStatus, 0, len(status.Shards)),
	}
	for _, shard := range status.Shards {
		converted.Shards = append(converted.Shards, app.PubSubShardStatus{
			Index:          shard.Index,
			State:          string(shard.State),
			Connected:      shard.Connected,
			TopicCount:     shard.TopicCount,
			SubmittedCount: shard.SubmittedCount,
		})
	}
	return converted
}

func convertCampaign(campaign *domain.DropsCampaign) *app.CampaignStatus {
	if campaign == nil {
		return nil
	}

	status := &app.CampaignStatus{
		ID:               campaign.ID,
		Name:             campaign.Name,
		ClaimedDrops:     campaign.ClaimedDrops(),
		TotalDrops:       campaign.TotalDrops(),
		RemainingMinutes: campaign.RemainingMinutes(),
		Progress:         campaign.Progress(),
	}
	if campaign.Game.ID > 0 || campaign.Game.Name != "" {
		status.Game = &app.GameStatus{
			ID:   campaign.Game.ID,
			Name: campaign.Game.Name,
			Slug: campaign.Game.Slug(),
		}
	}
	return status
}

func convertDrop(now time.Time, settings config.Settings, campaign *domain.DropsCampaign) *app.DropStatus {
	if campaign == nil {
		return nil
	}

	drop := campaign.FirstDrop(now, nil, settings.EnableBadgesEmotes, false)
	if drop == nil {
		return nil
	}

	return &app.DropStatus{
		ID:               drop.ID,
		Name:             drop.Name,
		CurrentMinutes:   drop.CurrentMinutes(),
		RequiredMinutes:  drop.RequiredMinutes,
		RemainingMinutes: drop.RemainingMinutes(),
		Claimable:        drop.CanClaim(now),
		Claimed:          drop.IsClaimed,
	}
}
