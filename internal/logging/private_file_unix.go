//go:build !windows

package logging

import (
	"os"
)

func openPrivateAppendFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}
