package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/runtime"
)

type sessionImporter interface {
	ImportSession(context.Context, auth.Session) error
}

type portableAuthState struct {
	*auth.State
	mu          sync.Mutex
	sessionFile string
	logger      *slog.Logger
}

func (s *portableAuthState) Validate(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.State.Validate(ctx)
	if !errors.Is(err, auth.ErrLoginRequired) {
		return err
	}
	imported, importErr := importDefaultSession(ctx, s.sessionFile, s.State)
	if importErr != nil {
		return importErr
	}
	if !imported {
		return err
	}
	if s.logger != nil {
		s.logger.Info("已自动导入 Android 登录会话", "user_id", s.Snapshot().UserID)
	}
	return s.State.Validate(ctx)
}

func (s *portableAuthState) HeadersProvider(options auth.HeadersOptions) func(context.Context) (http.Header, error) {
	return func(ctx context.Context) (http.Header, error) {
		if err := s.Validate(ctx); err != nil {
			return nil, err
		}
		return s.Headers(options), nil
	}
}

func importDefaultSession(ctx context.Context, path string, importer sessionImporter) (imported bool, resultErr error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("检查默认会话文件失败: %w", err)
	}
	lock, err := runtime.AcquireSessionLock(ctx, path)
	if err != nil {
		return false, err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("打开默认会话文件失败: %w", err)
	}
	original, statErr := file.Stat()
	session, readErr := auth.ReadSession(file)
	closeErr := file.Close()
	if err := errors.Join(statErr, readErr, closeErr); err != nil {
		return false, fmt.Errorf("读取默认会话文件失败: %w", err)
	}
	if err := importer.ImportSession(ctx, session); err != nil {
		return false, fmt.Errorf("默认会话导入失败，文件已保留: %w", err)
	}
	current, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return true, fmt.Errorf("检查已导入会话文件失败: %w", err)
	}
	// 导出工具遵守同一文件锁；外部上传若替换了文件，保留新文件以免误删
	if !os.SameFile(original, current) || original.Size() != current.Size() || !original.ModTime().Equal(current.ModTime()) {
		return true, nil
	}
	if err := os.Remove(path); err != nil {
		return true, fmt.Errorf("移除已导入会话文件失败: %w", err)
	}
	return true, nil
}
