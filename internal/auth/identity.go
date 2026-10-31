package auth

import "fmt"

// BindUser 将已验证账号绑定到本次服务生命周期，后续认证只允许同账号续期
func (s *State) BindUser() (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("认证状态未初始化")
	}
	s.validateMu.Lock()
	defer s.validateMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userID <= 0 || s.accessToken == "" || s.lastValidatedAt.IsZero() {
		return 0, fmt.Errorf("绑定账号前需要完成认证校验")
	}
	if s.boundUserID != 0 && s.boundUserID != s.userID {
		return 0, fmt.Errorf("当前服务已绑定其他账号，请重启后切换账号")
	}
	s.boundUserID = s.userID
	return s.boundUserID, nil
}

// checkBoundUser 在 validateMu 内、提交新凭据之前检查账号身份
func (s *State) checkBoundUser(userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.boundUserID != 0 && s.boundUserID != userID {
		return fmt.Errorf("当前服务已绑定其他账号，请重启后切换账号")
	}
	return nil
}
