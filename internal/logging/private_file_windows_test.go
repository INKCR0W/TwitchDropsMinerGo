//go:build windows

package logging

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateFilePermissionForTest(path string) (os.FileMode, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return 0, fmt.Errorf("读取日志文件 ACL 失败: %w", err)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return 0, fmt.Errorf("读取日志文件 DACL 失败: %w", err)
	}
	if dacl == nil {
		return 0, fmt.Errorf("日志文件缺少 DACL")
	}

	if dacl.AceCount != 3 {
		return 0o666, nil
	}

	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return 0, fmt.Errorf("读取日志文件 ACE 失败: %w", err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Mask != fileAllAccessMask {
			return 0o666, nil
		}

		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !isPrivateLogSID(sid) {
			return 0o666, nil
		}
	}

	return 0o600, nil
}

func isPrivateLogSID(sid *windows.SID) bool {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err == nil && sid.Equals(user.User.Sid) {
		return true
	}
	return sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
		sid.IsWellKnown(windows.WinLocalSystemSid)
}
