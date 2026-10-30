package browserlogin

import (
	"context"
	"errors"
	"strings"
)

// Login 由调用方展示授权地址并接收完整回调，不访问浏览器配置或日常会话
func Login(ctx context.Context, receive func(context.Context, string) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if receive == nil {
		return "", errors.New("没有提供授权结果输入方式")
	}
	address, state, err := loginURL()
	if err != nil {
		return "", err
	}
	callback, err := receive(ctx, address)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	token, err := callbackToken(strings.TrimSpace(callback), state)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", errors.New("未找到有效授权结果，请复制登录完成后的完整地址")
	}
	return token, nil
}
