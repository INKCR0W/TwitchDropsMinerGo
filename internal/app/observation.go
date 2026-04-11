package app

import (
	"time"

	"twitchdropsminergo/internal/config"
)

type Observation struct {
	Healthy   bool
	LastError string
	Auth      AuthStatus
	Schedule  ScheduleStatus
	Settings  config.Settings
}

type AuthStatus struct {
	LoggedIn bool  `json:"logged_in"`
	UserID   int64 `json:"user_id,omitempty"`
}

type ScheduleStatus struct {
	State                  string          `json:"state,omitempty"`
	WantedGames            []GameStatus    `json:"wanted_games,omitempty"`
	WatchingChannelID      int64           `json:"watching_channel_id,omitempty"`
	SelectedChannelID      int64           `json:"selected_channel_id,omitempty"`
	FullCleanup            bool            `json:"full_cleanup"`
	LastProgressAt         time.Time       `json:"last_progress_at,omitempty"`
	ChannelCount           int             `json:"channel_count"`
	Channels               []ChannelStatus `json:"channels,omitempty"`
	InventoryCampaignCount int             `json:"inventory_campaign_count"`
	InventoryDropCount     int             `json:"inventory_drop_count"`
	UserTopicUserID        int64           `json:"user_topic_user_id,omitempty"`
	PubSub                 PubSubStatus    `json:"pubsub"`
	ActiveCampaign         *CampaignStatus `json:"active_campaign,omitempty"`
	ActiveDrop             *DropStatus     `json:"active_drop,omitempty"`
}

type GameStatus struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

type ChannelStatus struct {
	ID            int64         `json:"id"`
	Login         string        `json:"login"`
	DisplayName   string        `json:"display_name,omitempty"`
	ACLBased      bool          `json:"acl_based"`
	PendingStream bool          `json:"pending_stream"`
	Online        bool          `json:"online"`
	Stream        *StreamStatus `json:"stream,omitempty"`
}

type StreamStatus struct {
	BroadcastID  int64       `json:"broadcast_id,omitempty"`
	Game         *GameStatus `json:"game,omitempty"`
	Viewers      int         `json:"viewers,omitempty"`
	Title        string      `json:"title,omitempty"`
	DropsEnabled bool        `json:"drops_enabled"`
}

type PubSubStatus struct {
	Running    bool                `json:"running"`
	Endpoint   string              `json:"endpoint,omitempty"`
	TopicCount int                 `json:"topic_count"`
	Shards     []PubSubShardStatus `json:"shards,omitempty"`
}

type PubSubShardStatus struct {
	Index          int    `json:"index"`
	State          string `json:"state"`
	Connected      bool   `json:"connected"`
	TopicCount     int    `json:"topic_count"`
	SubmittedCount int    `json:"submitted_count"`
}

type CampaignStatus struct {
	ID               string      `json:"id"`
	Name             string      `json:"name"`
	Game             *GameStatus `json:"game,omitempty"`
	ClaimedDrops     int         `json:"claimed_drops"`
	TotalDrops       int         `json:"total_drops"`
	RemainingMinutes int         `json:"remaining_minutes"`
	Progress         float64     `json:"progress"`
}

type DropStatus struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	CurrentMinutes   int    `json:"current_minutes"`
	RequiredMinutes  int    `json:"required_minutes"`
	RemainingMinutes int    `json:"remaining_minutes"`
	Claimable        bool   `json:"claimable"`
	Claimed          bool   `json:"claimed"`
}

func (o Observation) normalized(fallback config.Settings) Observation {
	normalized := o
	settings := normalized.Settings
	if settings.IsZero() {
		settings = fallback.Clone()
	}
	if err := settings.Validate(); err != nil {
		settings = fallback.Clone()
		_ = settings.Validate()
	}
	normalized.Settings = settings.Sanitized()
	normalized.Schedule = normalized.Schedule.clone()
	return normalized
}

func (s RuntimeState) clone() RuntimeState {
	cloned := s
	cloned.Settings = s.Settings.Clone()
	cloned.Schedule = s.Schedule.clone()
	return cloned
}

func (s ScheduleStatus) clone() ScheduleStatus {
	cloned := s
	cloned.WantedGames = cloneGames(s.WantedGames)
	cloned.Channels = cloneChannels(s.Channels)
	cloned.PubSub = s.PubSub.clone()
	if s.ActiveCampaign != nil {
		campaign := *s.ActiveCampaign
		if s.ActiveCampaign.Game != nil {
			game := *s.ActiveCampaign.Game
			campaign.Game = &game
		}
		cloned.ActiveCampaign = &campaign
	}
	if s.ActiveDrop != nil {
		drop := *s.ActiveDrop
		cloned.ActiveDrop = &drop
	}
	return cloned
}

func (s PubSubStatus) clone() PubSubStatus {
	cloned := s
	cloned.Shards = append([]PubSubShardStatus(nil), s.Shards...)
	return cloned
}

func cloneGames(games []GameStatus) []GameStatus {
	if len(games) == 0 {
		return nil
	}
	return append([]GameStatus(nil), games...)
}

func cloneChannels(channels []ChannelStatus) []ChannelStatus {
	if len(channels) == 0 {
		return nil
	}

	cloned := make([]ChannelStatus, 0, len(channels))
	for _, channel := range channels {
		copied := channel
		if channel.Stream != nil {
			stream := *channel.Stream
			if channel.Stream.Game != nil {
				game := *channel.Stream.Game
				stream.Game = &game
			}
			copied.Stream = &stream
		}
		cloned = append(cloned, copied)
	}
	return cloned
}
