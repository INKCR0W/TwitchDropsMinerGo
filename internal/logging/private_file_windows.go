//go:build windows

package logging

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const fileAllAccessMask windows.ACCESS_MASK = 0x1f01ff

func openPrivateAppendFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}

	if err := protectPrivateFile(path); err != nil {
		_ = file.Close()
		return nil, err
	}

	return file, nil
}

func protectPrivateFile(path string) error {
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
		newAllowACE(user.User.Sid, windows.TRUSTEE_IS_USER),
		newAllowACE(admins, windows.TRUSTEE_IS_GROUP),
		newAllowACE(system, windows.TRUSTEE_IS_USER),
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("创建日志文件 ACL 失败: %w", err)
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
		return fmt.Errorf("设置日志文件 ACL 失败: %w", err)
	}

	return nil
}

func newAllowACE(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
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
