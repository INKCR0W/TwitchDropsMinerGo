package runtime

import (
	"errors"
	"fmt"

	"github.com/gofrs/flock"
)

var ErrAlreadyRunning = errors.New("已有实例运行")

type InstanceLock struct {
	lock *flock.Flock
}

func AcquireInstanceLock(path string) (*InstanceLock, error) {
	fileLock := flock.New(path)
	locked, err := fileLock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("获取锁 %q 失败: %w", path, err)
	}
	if !locked {
		return nil, ErrAlreadyRunning
	}

	return &InstanceLock{lock: fileLock}, nil
}

func (l *InstanceLock) Close() error {
	if l == nil || l.lock == nil {
		return nil
	}

	if err := l.lock.Unlock(); err != nil {
		return fmt.Errorf("释放锁 %q 失败: %w", l.lock.Path(), err)
	}

	if err := l.lock.Close(); err != nil {
		return fmt.Errorf("关闭锁文件 %q 失败: %w", l.lock.Path(), err)
	}

	return nil
}
