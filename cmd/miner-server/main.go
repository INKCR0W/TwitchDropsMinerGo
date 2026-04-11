package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"twitchdropsminergo/internal/api"
	"twitchdropsminergo/internal/app"
	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/domain"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/logging"
	"twitchdropsminergo/internal/pubsub"
	"twitchdropsminergo/internal/runtime"
	"twitchdropsminergo/internal/scheduler"
	"twitchdropsminergo/internal/storage"
	"twitchdropsminergo/internal/watch"
)

const defaultListenAddr = "127.0.0.1:8080"

type runner interface {
	Run(context.Context) error
}

func main() {
	os.Exit(run(os.Args[1:]))
}

type cliOptions struct {
	RuntimeDir string
	ListenAddr string
}

func run(args []string) int {
	options, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}

	layout, err := runtime.ResolveLayout(options.RuntimeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "解析运行目录失败: %v\n", err)
		return 2
	}

	if err := layout.Ensure(); err != nil {
		fmt.Fprintf(os.Stderr, "初始化运行目录失败: %v\n", err)
		return 1
	}

	lock, err := runtime.AcquireInstanceLock(layout.LockFile)
	if err != nil {
		if errors.Is(err, runtime.ErrAlreadyRunning) {
			fmt.Fprintln(os.Stderr, "已有实例运行")
			return 3
		}

		fmt.Fprintf(os.Stderr, "获取单实例锁失败: %v\n", err)
		return 1
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "释放单实例锁失败: %v\n", closeErr)
		}
	}()

	settingsStore := config.NewFileStore(layout.SettingsFile)
	settings, err := settingsStore.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 4
	}
	if err := ensureSettingsFile(layout.SettingsFile, settings); err != nil {
		fmt.Fprintf(os.Stderr, "初始化配置文件失败: %v\n", err)
		return 4
	}

	logger, closeLogger, err := logging.New(settings.Log, layout.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		return 1
	}
	defer func() {
		if closeErr := closeLogger(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "关闭日志文件失败: %v\n", closeErr)
		}
	}()

	stateStore := storage.NewJSONFile(layout.StateFile, app.DefaultRuntimeState())
	application, err := app.New(app.Options{
		Logger:     logger,
		StateStore: stateStore,
		Settings:   settingsStore,
	})
	if err != nil {
		logger.Error("初始化应用失败", "error", err)
		return 1
	}

	clientInfo := httpclient.AndroidAppClient
	httpClient, err := httpclient.New(httpclient.Options{
		Settings:    application.Settings(),
		CookiesPath: layout.CookiesFile,
		ClientInfo:  clientInfo,
	})
	if err != nil {
		logger.Error("初始化 HTTP 客户端失败", "error", err)
		return 1
	}
	defer func() {
		if closeErr := httpClient.Close(); closeErr != nil {
			logger.Warn("关闭 HTTP 客户端失败", "error", closeErr)
		}
	}()

	authState, err := auth.New(auth.Options{
		HTTPClient:        httpClient,
		ClientInfo:        clientInfo,
		DeviceCodeHandler: logDeviceCode(logger),
	})
	if err != nil {
		logger.Error("初始化认证状态失败", "error", err)
		return 1
	}

	gqlClient, err := gql.NewClient(gql.ClientOptions{
		HTTPClient: httpClient,
		ClientInfo: clientInfo,
		HeadersProvider: authState.HeadersProvider(auth.HeadersOptions{
			UserAgent: clientInfo.UserAgent,
			GQL:       true,
		}),
	})
	if err != nil {
		logger.Error("初始化 GQL 客户端失败", "error", err)
		return 1
	}

	refresher, err := inventory.NewRefresher(inventory.Options{
		GQLClient: gqlClient,
		AuthState: authState,
	})
	if err != nil {
		logger.Error("初始化 inventory 刷新器失败", "error", err)
		return 1
	}

	tracker, err := watch.NewTracker(watch.Options{
		GQLClient:  gqlClient,
		HTTPClient: httpClient,
		AuthState:  authState,
		ClientInfo: clientInfo,
	})
	if err != nil {
		logger.Error("初始化 watch 跟踪器失败", "error", err)
		return 1
	}

	pubsubManager, err := pubsub.NewManager(pubsub.Options{
		Logger: logger,
		Auth:   authState,
		HeadersProvider: authState.HeadersProvider(auth.HeadersOptions{
			UserAgent: clientInfo.UserAgent,
		}),
		ProxyURL: application.Settings().Proxy,
	})
	if err != nil {
		logger.Error("初始化 PubSub 管理器失败", "error", err)
		return 1
	}

	schedulerInstance, err := scheduler.New(scheduler.Options{
		Logger:    logger,
		Settings:  application.Settings(),
		Refresher: refresher,
		Tracker:   tracker,
		PubSub:    pubsubManager,
		GQLClient: gqlClient,
		AuthState: authState,
	})
	if err != nil {
		logger.Error("初始化调度器失败", "error", err)
		return 1
	}

	apiHandler, err := api.NewHandler(api.Options{
		Logger:        logger,
		ListenAddress: options.ListenAddr,
		Health: func(context.Context) api.HealthResponse {
			return api.HealthResponse{
				Status: "ok",
				Time:   time.Now().UTC(),
			}
		},
		Status: func(context.Context) (api.StatusResponse, error) {
			return buildStatusResponse(application, authState.Snapshot(), schedulerInstance.StatusSnapshot()), nil
		},
		CurrentSettings: func(context.Context) (config.Settings, error) {
			return application.Settings(), nil
		},
		UpdateSettings: func(ctx context.Context, next config.Settings) (config.Settings, error) {
			current := application.Settings()
			if err := validateHotUpdatableSettings(current, next); err != nil {
				return config.Settings{}, err
			}
			if err := application.UpdateSettings(next); err != nil {
				return config.Settings{}, err
			}
			if err := schedulerInstance.UpdateSettings(application.Settings()); err != nil {
				if rollbackErr := application.UpdateSettings(current); rollbackErr != nil {
					logger.Error("回滚运行配置失败", "error", rollbackErr)
				}
				return config.Settings{}, err
			}
			logger.Info("运行配置已更新")
			return application.Settings(), nil
		},
		Reload: func(context.Context) error {
			schedulerInstance.Reload()
			logger.Info("收到 inventory reload 请求")
			return nil
		},
	})
	if err != nil {
		logger.Error("初始化运维接口失败", "error", err)
		return 1
	}

	server := &http.Server{
		Addr:              options.ListenAddr,
		Handler:           apiHandler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logger.Info("运行时基础设施已就绪",
		"runtime_dir", layout.RootDir,
		"settings_file", layout.SettingsFile,
		"state_file", layout.StateFile,
		"cookies_file", layout.CookiesFile,
		"log_file", layout.LogFile,
		"listen_addr", options.ListenAddr,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runService(ctx, stop, application, schedulerInstance, server, logger); err != nil {
		logger.Error("服务退出失败", "error", err)
		return 1
	}

	return 0
}

func parseArgs(args []string) (cliOptions, error) {
	var options cliOptions

	flagSet := flag.NewFlagSet("miner-server", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	flagSet.StringVar(&options.RuntimeDir, "runtime-dir", runtime.DefaultRootDir, "运行目录")
	flagSet.StringVar(&options.ListenAddr, "listen-addr", defaultListenAddr, "运维接口监听地址")

	if err := flagSet.Parse(args); err != nil {
		return cliOptions{}, err
	}

	if flagSet.NArg() > 0 {
		return cliOptions{}, fmt.Errorf("不支持的位置参数: %s", strings.Join(flagSet.Args(), " "))
	}
	if strings.TrimSpace(options.ListenAddr) == "" {
		return cliOptions{}, fmt.Errorf("listen-addr 不能为空")
	}

	return options, nil
}

func ensureSettingsFile(path string, settings config.Settings) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return config.Save(path, settings)
}

func runService(ctx context.Context, stop context.CancelFunc, application *app.App, service runner, server *http.Server, logger *slog.Logger) error {
	appErrCh := make(chan error, 1)
	serviceErrCh := make(chan error, 1)
	serverErrCh := make(chan error, 1)

	go func() {
		appErrCh <- application.Run(ctx)
	}()
	go func() {
		serviceErrCh <- service.Run(ctx)
	}()
	go func() {
		logger.Info("运维接口开始监听", "listen_addr", server.Addr)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			serverErrCh <- nil
			return
		}
		serverErrCh <- err
	}()

	var (
		appErr      error
		serviceErr  error
		serverErr   error
		appDone     bool
		serviceDone bool
		serverDone  bool
	)

	select {
	case appErr = <-appErrCh:
		appDone = true
	case serviceErr = <-serviceErrCh:
		serviceDone = true
	case serverErr = <-serverErrCh:
		serverDone = true
	case <-ctx.Done():
	}

	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) && serverErr == nil {
		serverErr = err
	}

	if !serverDone {
		serverErr = <-serverErrCh
	}
	if !appDone {
		appErr = <-appErrCh
	}
	if !serviceDone {
		serviceErr = <-serviceErrCh
	}

	return firstRuntimeError(appErr, serviceErr, serverErr)
}

func firstRuntimeError(errorsToCheck ...error) error {
	for _, err := range errorsToCheck {
		if err == nil {
			continue
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		if errors.Is(err, http.ErrServerClosed) {
			continue
		}
		return err
	}
	return nil
}

func logDeviceCode(logger *slog.Logger) auth.DeviceCodeHandler {
	return func(_ context.Context, deviceCode auth.DeviceCode) error {
		logger.Info(
			"需要完成 Twitch Device Code 授权",
			"user_code", deviceCode.UserCode,
			"verification_uri", deviceCode.VerificationURI,
			"expires_at", deviceCode.ExpiresAt.UTC(),
			"interval", deviceCode.Interval.String(),
		)
		return nil
	}
}

func validateHotUpdatableSettings(current config.Settings, next config.Settings) error {
	if current.Proxy != next.Proxy {
		return fmt.Errorf("proxy 变更需要重启后生效，当前接口暂不支持热更新")
	}
	if current.ConnectionQuality != next.ConnectionQuality {
		return fmt.Errorf("connection_quality 变更需要重启后生效，当前接口暂不支持热更新")
	}
	if current.Log != next.Log {
		return fmt.Errorf("log 配置变更需要重启后生效，当前接口暂不支持热更新")
	}
	return nil
}

func buildStatusResponse(application *app.App, authSnapshot auth.Snapshot, schedulerSnapshot scheduler.StatusSnapshot) api.StatusResponse {
	runtimeState := application.RuntimeState()

	return api.StatusResponse{
		Healthy: true,
		Runtime: api.RuntimeStatus{
			SchemaVersion: runtimeState.SchemaVersion,
			RunCount:      runtimeState.RunCount,
			LastStartedAt: runtimeState.LastStartedAt,
			LastStoppedAt: runtimeState.LastStoppedAt,
		},
		Auth: api.AuthStatus{
			LoggedIn: authSnapshot.UserID > 0,
			UserID:   authSnapshot.UserID,
		},
		Schedule: api.ScheduleStatus{
			State:                  string(schedulerSnapshot.State),
			WantedGames:            convertGames(schedulerSnapshot.WantedGames),
			WatchingChannelID:      schedulerSnapshot.WatchingChannelID,
			SelectedChannelID:      schedulerSnapshot.SelectedChannelID,
			FullCleanup:            schedulerSnapshot.FullCleanup,
			LastProgressAt:         schedulerSnapshot.LastProgressAt,
			ChannelCount:           len(schedulerSnapshot.Channels),
			Channels:               convertChannels(schedulerSnapshot.Channels),
			InventoryCampaignCount: schedulerSnapshot.InventoryCampaignCount,
			InventoryDropCount:     schedulerSnapshot.InventoryDropCount,
			UserTopicUserID:        schedulerSnapshot.UserTopicUserID,
			PubSub:                 convertPubSubStatus(schedulerSnapshot.PubSub),
		},
		Settings: application.Settings(),
	}
}

func convertGames(games []domain.Game) []api.GameStatus {
	converted := make([]api.GameStatus, 0, len(games))
	for _, game := range games {
		converted = append(converted, api.GameStatus{
			ID:   game.ID,
			Name: game.Name,
			Slug: game.Slug(),
		})
	}
	return converted
}

func convertChannels(channels []domain.Channel) []api.ChannelStatus {
	converted := make([]api.ChannelStatus, 0, len(channels))
	for _, channel := range channels {
		status := api.ChannelStatus{
			ID:            channel.ID,
			Login:         channel.Login,
			DisplayName:   channel.DisplayName,
			ACLBased:      channel.ACLBased,
			PendingStream: channel.PendingStream,
			Online:        channel.Online(),
		}
		if channel.Stream != nil {
			status.Stream = &api.StreamStatus{
				BroadcastID:  channel.Stream.BroadcastID,
				Viewers:      channel.Stream.Viewers,
				Title:        channel.Stream.Title,
				DropsEnabled: channel.Stream.DropsEnabled,
			}
			if channel.Stream.Game != nil {
				status.Stream.Game = &api.GameStatus{
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

func convertPubSubStatus(status pubsub.Status) api.PubSubStatus {
	converted := api.PubSubStatus{
		Running:    status.Running,
		Endpoint:   status.Endpoint,
		TopicCount: status.TopicCount,
		Shards:     make([]api.PubSubShardStatus, 0, len(status.Shards)),
	}
	for _, shard := range status.Shards {
		converted.Shards = append(converted.Shards, api.PubSubShardStatus{
			Index:          shard.Index,
			State:          string(shard.State),
			Connected:      shard.Connected,
			TopicCount:     shard.TopicCount,
			SubmittedCount: shard.SubmittedCount,
		})
	}
	return converted
}
