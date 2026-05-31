//go:build !windows

package logging

import (
	"os"
)

func privateFilePermissionForTest(path string) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Mode().Perm(), nil
}
