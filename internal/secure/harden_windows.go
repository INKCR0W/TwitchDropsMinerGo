//go:build windows

package secure

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// fileAllAccessMask 对应 FILE_ALL_ACCESS。
const fileAllAccessMask windows.ACCESS_MASK = 0x1f01ff

func HardenFile(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("获取当前用户失败: %w", err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("创建管理员 SID 失败: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("创建 SYSTEM SID 失败: %w", err)
	}

	entries := []windows.EXPLICIT_ACCESS{
		allowACE(user.User.Sid, windows.TRUSTEE_IS_USER),
		allowACE(admins, windows.TRUSTEE_IS_GROUP),
		allowACE(system, windows.TRUSTEE_IS_USER),
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("创建文件 ACL 失败: %w", err)
	}

	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("设置文件 ACL 失败: %w", err)
	}

	return nil
}

func allowACE(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: fileAllAccessMask,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
