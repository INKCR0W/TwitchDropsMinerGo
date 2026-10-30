package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// AcquireSessionLock 协调同一路径的会话导出与导入，不阻塞浏览器授权过程
func AcquireSessionLock(ctx context.Context, sessionPath string) (*InstanceLock, error) {
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		return nil, fmt.Errorf("创建会话目录失败: %w", err)
	}
	fileLock := flock.New(sessionPath + ".lock")
	locked, err := fileLock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil || !locked {
		if err == nil {
			err = ErrAlreadyRunning
		}
		return nil, errors.Join(fmt.Errorf("获取会话文件锁失败: %w", err), fileLock.Close())
	}
	return &InstanceLock{lock: fileLock}, nil
}
