package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"twitchdropsminergo/internal/app"
	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/gql"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/inventory"
	"twitchdropsminergo/internal/logging"
	"twitchdropsminergo/internal/progress"
	"twitchdropsminergo/internal/pubsub"
	"twitchdropsminergo/internal/rewards"
	"twitchdropsminergo/internal/runtime"
	"twitchdropsminergo/internal/scheduler"
	"twitchdropsminergo/internal/storage"
	"twitchdropsminergo/internal/watch"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type cliOptions struct {
	RuntimeDir  string
	Healthcheck bool
}

func run(args []string) int {
	options, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "参数错误: %v\n", err)
		return 2
	}

	if options.Healthcheck {
		return runHealthcheck(options.RuntimeDir)
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

	clearPendingLogin(logger, layout.PendingLoginFile)()

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

	mainlandDialer := newMainlandDialer(application.Settings(), logger)

	clientInfo := httpclient.AndroidAppClient
	httpClient, err := httpclient.New(httpclient.Options{
		Logger:         logger,
		Settings:       application.Settings(),
		CookiesPath:    layout.CookiesFile,
		ClientInfo:     clientInfo,
		DialTLSContext: mainlandDialerTLS(mainlandDialer),
	})
	if err != nil {
		return failRun(application, logger, "初始化 HTTP 客户端失败", err)
	}
	defer func() {
		if closeErr := httpClient.Close(); closeErr != nil {
			logger.Warn("关闭 HTTP 客户端失败", "error", closeErr)
		}
	}()

	authState, err := auth.New(auth.Options{
		HTTPClient:           httpClient,
		ClientInfo:           clientInfo,
		DeviceCodeHandler:    deviceCodeNotifier(logger, os.Stdout, layout.PendingLoginFile),
		AuthenticatedHandler: clearPendingLogin(logger, layout.PendingLoginFile),
	})
	if err != nil {
		return failRun(application, logger, "初始化认证状态失败", err)
	}

	gqlClient, err := gql.NewClient(gql.ClientOptions{
		HTTPClient: httpClient,
		ClientInfo: clientInfo,
		Logger:     logger,
		HeadersProvider: authState.HeadersProvider(auth.HeadersOptions{
			UserAgent: clientInfo.UserAgent,
			GQL:       true,
		}),
	})
	if err != nil {
		return failRun(application, logger, "初始化 GQL 客户端失败", err)
	}

	rewardProgress, err := rewards.NewFileStore(layout.RewardsFile)
	if err != nil {
		return failRun(application, logger, "初始化 reward 进度存储失败", err)
	}

	watchProgress, err := progress.NewFileStore(layout.ProgressFile)
	if err != nil {
		return failRun(application, logger, "初始化观看进度存储失败", err)
	}

	refresher, err := inventory.NewRefresher(inventory.Options{
		GQLClient:      gqlClient,
		AuthState:      authState,
		RewardProgress: rewardProgress.Snapshot(),
		Logger:         logger,
	})
	if err != nil {
		return failRun(application, logger, "初始化 inventory 刷新器失败", err)
	}

	tracker, err := watch.NewTracker(watch.Options{
		GQLClient:   gqlClient,
		SpadeClient: httpClient,
		// spade 复用完整鉴权头(含 OAuth), 与 GQL 请求一致
		WatchHeaders: authState.HeadersProvider(auth.HeadersOptions{
			UserAgent: clientInfo.UserAgent,
			GQL:       true,
		}),
		AuthState: authState,
		Logger:    logger,
	})
	if err != nil {
		return failRun(application, logger, "初始化 watch 跟踪器失败", err)
	}

	pubsubManager, err := pubsub.NewManager(pubsub.Options{
		Logger: logger,
		Auth:   authState,
		HeadersProvider: authState.HeadersProvider(auth.HeadersOptions{
			UserAgent: clientInfo.UserAgent,
		}),
		ProxyURL:          application.Settings().Proxy,
		NetDialTLSContext: mainlandDialerTLS(mainlandDialer),
	})
	if err != nil {
		return failRun(application, logger, "初始化 PubSub 管理器失败", err)
	}

	schedulerInstance, err := scheduler.New(scheduler.Options{
		Logger:         logger,
		Settings:       application.Settings(),
		Refresher:      refresher,
		Tracker:        tracker,
		PubSub:         pubsubManager,
		GQLClient:      gqlClient,
		AuthState:      authState,
		RewardProgress: rewardProgress,
		WatchProgress:  watchProgress,
	})
	if err != nil {
		return failRun(application, logger, "初始化调度器失败", err)
	}
	stateSync := newLocalStateSync(application, authState, schedulerInstance)

	logger.Info("运行时基础设施已就绪",
		"runtime_dir", layout.RootDir,
		"settings_file", layout.SettingsFile,
		"state_file", layout.StateFile,
		"cookies_file", layout.CookiesFile,
		"rewards_file", layout.RewardsFile,
		"log_file", layout.LogFile,
		"state_observer", "enabled",
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runService(ctx, stop,
		namedRunner{name: "应用状态持久化", runner: application},
		namedRunner{name: "调度服务", runner: schedulerInstance},
		namedRunner{name: "本地状态同步", runner: stateSync},
	); err != nil {
		return failRun(application, logger, "运行时退出失败", err)
	}

	return 0
}

func parseArgs(args []string) (cliOptions, error) {
	var options cliOptions

	flagSet := flag.NewFlagSet("miner-server", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)
	flagSet.StringVar(&options.RuntimeDir, "runtime-dir", runtime.DefaultRootDir, "运行目录")
	flagSet.BoolVar(&options.Healthcheck, "healthcheck", false, "健康检查模式")

	if err := flagSet.Parse(args); err != nil {
		return cliOptions{}, err
	}

	if flagSet.NArg() > 0 {
		return cliOptions{}, fmt.Errorf("不支持的位置参数: %s", strings.Join(flagSet.Args(), " "))
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

func failRun(application *app.App, logger *slog.Logger, message string, err error) int {
	if err == nil {
		return 0
	}
	if logger != nil {
		logger.Error(message, "error", err)
	}
	if application != nil {
		recordErr := application.RecordFailure(fmt.Errorf("%s: %w", message, err))
		if recordErr != nil && logger != nil {
			logger.Warn("写入失败状态失败", "error", recordErr)
		}
	}
	return 1
}
