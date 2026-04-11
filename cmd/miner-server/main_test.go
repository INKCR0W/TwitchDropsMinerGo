package main

import (
	"io"
	"log/slog"
	"testing"

	"twitchdropsminergo/internal/app"
	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/pubsub"
	"twitchdropsminergo/internal/scheduler"
)

func TestParseArgsAppliesDefaults(t *testing.T) {
	t.Parallel()

	options, err := parseArgs(nil)
	if err != nil {
		t.Fatalf("parseArgs 返回错误: %v", err)
	}
	if options.RuntimeDir == "" || options.ListenAddr != defaultListenAddr {
		t.Fatalf("默认参数不匹配: %#v", options)
	}
}

func TestValidateHotUpdatableSettingsRejectsTransportChanges(t *testing.T) {
	t.Parallel()

	current := config.DefaultSettings()
	next := current.Clone()
	next.Proxy = "http://proxy.example.com:8080"

	if err := validateHotUpdatableSettings(current, next); err == nil {
		t.Fatal("Proxy 变更应被拒绝")
	}
}

func TestBuildStatusResponseMapsSnapshots(t *testing.T) {
	t.Parallel()

	application, err := app.New(app.Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("app.New 返回错误: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Language = "简体中文"
	if err := application.UpdateSettings(settings); err != nil {
		t.Fatalf("UpdateSettings 返回错误: %v", err)
	}

	response := buildStatusResponse(application, auth.Snapshot{UserID: 42}, scheduler.StatusSnapshot{
		State:             scheduler.StateChannelSwitch,
		WantedGames:       []domain.Game{{ID: 1, Name: "Game", SlugText: "game"}},
		WatchingChannelID: 9,
		Channels: []domain.Channel{
			{
				ID:          9,
				Login:       "watching",
				DisplayName: "Watching",
				Stream: &domain.Stream{
					BroadcastID:  99,
					Game:         &domain.Game{ID: 1, Name: "Game", SlugText: "game"},
					Viewers:      120,
					Title:        "Live",
					DropsEnabled: true,
				},
			},
		},
		PubSub: pubsub.Status{
			Running:    true,
			Endpoint:   "wss://pubsub.example.com",
			TopicCount: 2,
			Shards: []pubsub.ShardStatus{
				{
					Index:          0,
					State:          pubsub.ShardStateConnected,
					Connected:      true,
					TopicCount:     2,
					SubmittedCount: 2,
				},
			},
		},
	})

	if !response.Auth.LoggedIn || response.Auth.UserID != 42 {
		t.Fatalf("认证状态映射错误: %#v", response.Auth)
	}
	if response.Schedule.State != string(scheduler.StateChannelSwitch) {
		t.Fatalf("调度状态映射错误: %#v", response.Schedule)
	}
	if response.Schedule.ChannelCount != 1 || response.Schedule.Channels[0].Login != "watching" {
		t.Fatalf("频道状态映射错误: %#v", response.Schedule.Channels)
	}
	if response.Schedule.PubSub.TopicCount != 2 || !response.Schedule.PubSub.Running {
		t.Fatalf("PubSub 状态映射错误: %#v", response.Schedule.PubSub)
	}
	if response.Settings.Language != "简体中文" {
		t.Fatalf("设置映射错误: %#v", response.Settings)
	}
}
