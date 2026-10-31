package runtime

import (
	"fmt"
	"path/filepath"
	"strconv"
)

type ProgressFiles struct {
	RewardsFile  string
	ProgressFile string
}

func (l Layout) ProgressFilesForUser(userID int64) (ProgressFiles, error) {
	if userID <= 0 {
		return ProgressFiles{}, fmt.Errorf("进度存储需要已验证的用户 ID")
	}
	// 旧版无身份文件保留在原处，不能推测其所属账号
	accountDir := filepath.Join(l.StateDir, "accounts", strconv.FormatInt(userID, 10))
	return ProgressFiles{
		RewardsFile:  filepath.Join(accountDir, "rewards.json"),
		ProgressFile: filepath.Join(accountDir, "progress.json"),
	}, nil
}
