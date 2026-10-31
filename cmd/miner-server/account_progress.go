package main

import (
	"context"
	"fmt"
	"time"

	"twitchdropsminergo/internal/progress"
	"twitchdropsminergo/internal/rewards"
	"twitchdropsminergo/internal/runtime"
)

type accountProgressStores struct {
	rewards *rewards.FileStore
	watch   *progress.FileStore
}

func initializeAccountProgress(ctx context.Context, layout runtime.Layout, state *portableAuthState) (accountProgressStores, error) {
	validateCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := state.Validate(validateCtx); err != nil {
		return accountProgressStores{}, fmt.Errorf("初始化账号进度前认证失败: %w", err)
	}
	userID, err := state.BindUser()
	if err != nil {
		return accountProgressStores{}, err
	}
	files, err := layout.ProgressFilesForUser(userID)
	if err != nil {
		return accountProgressStores{}, err
	}
	reward, err := rewards.NewFileStore(files.RewardsFile)
	if err != nil {
		return accountProgressStores{}, err
	}
	watch, err := progress.NewFileStore(files.ProgressFile)
	if err != nil {
		return accountProgressStores{}, err
	}
	return accountProgressStores{rewards: reward, watch: watch}, nil
}
