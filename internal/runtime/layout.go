package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultRootDir = "runtime"

type Layout struct {
	RootDir      string
	SettingsFile string
	StateDir     string
	StateFile    string
	CookiesFile  string
	RewardsFile  string
	ProgressFile string
	LogsDir      string
	LogFile      string
	LockFile     string
}

func ResolveLayout(rootDir string) (Layout, error) {
	rootDir = strings.TrimSpace(rootDir)
	if rootDir == "" {
		rootDir = DefaultRootDir
	}

	absoluteRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return Layout{}, fmt.Errorf("解析绝对路径失败: %w", err)
	}

	absoluteRoot = filepath.Clean(absoluteRoot)
	stateDir := filepath.Join(absoluteRoot, "state")
	logsDir := filepath.Join(absoluteRoot, "logs")

	return Layout{
		RootDir:      absoluteRoot,
		SettingsFile: filepath.Join(absoluteRoot, "settings.json"),
		StateDir:     stateDir,
		StateFile:    filepath.Join(stateDir, "state.json"),
		CookiesFile:  filepath.Join(stateDir, "cookies.json"),
		RewardsFile:  filepath.Join(stateDir, "rewards.json"),
		ProgressFile: filepath.Join(stateDir, "progress.json"),
		LogsDir:      logsDir,
		LogFile:      filepath.Join(logsDir, "miner-server.log"),
		LockFile:     filepath.Join(absoluteRoot, "lock.file"),
	}, nil
}

func (l Layout) Ensure() error {
	for _, dir := range []string{l.RootDir, l.StateDir, l.LogsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建目录 %q 失败: %w", dir, err)
		}
	}

	return nil
}
