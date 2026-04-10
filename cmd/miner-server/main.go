package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"twitchdropsminergo/internal/app"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/logging"
	"twitchdropsminergo/internal/runtime"
	"twitchdropsminergo/internal/storage"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

type cliOptions struct {
	RuntimeDir string
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

	settings, err := config.Load(layout.SettingsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		return 4
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

	logger.Info("运行时基础设施已就绪",
		"runtime_dir", layout.RootDir,
		"settings_file", layout.SettingsFile,
		"state_file", layout.StateFile,
		"log_file", layout.LogFile,
	)

	stateStore := storage.NewJSONFile(layout.StateFile, app.DefaultRuntimeState())
	application, err := app.New(app.Options{
		Logger:     logger,
		StateStore: stateStore,
	})
	if err != nil {
		logger.Error("初始化服务失败", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := application.Run(ctx); err != nil {
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
