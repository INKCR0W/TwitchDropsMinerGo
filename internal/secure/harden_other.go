//go:build !windows

package secure

import "os"

func HardenFile(path string) error {
	return os.Chmod(path, 0o600)
}
