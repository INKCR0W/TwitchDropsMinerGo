package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/browserlogin"
	"twitchdropsminergo/internal/config"
	"twitchdropsminergo/internal/httpclient"
	"twitchdropsminergo/internal/runtime"
)

const maxManualCallbackBytes = 8 * 1024

func runBrowserLogin(ctx context.Context, layout runtime.Layout, settings config.Settings, logger *slog.Logger, input io.Reader, output io.Writer) int {
	importer := loginSessionImport(func(ctx context.Context, session auth.Session) error {
		state, client, err := newSessionAuthState(layout, settings, logger)
		if err != nil {
			return fmt.Errorf("初始化登录客户端失败: %w", err)
		}
		defer func() {
			if err := client.Close(); err != nil {
				logger.Warn("关闭登录客户端失败", "error", err)
			}
		}()
		return state.ImportSession(ctx, session)
	})
	if err := interactiveLogin(ctx, input, output, importer); err != nil {
		logger.Error("浏览器登录失败", "error", err)
		return 1
	}
	return 0
}

type loginSessionImport func(context.Context, auth.Session) error

func (operation loginSessionImport) ImportSession(ctx context.Context, session auth.Session) error {
	return operation(ctx, session)
}

func interactiveLogin(ctx context.Context, input io.Reader, output io.Writer, importer sessionImporter) error {
	loginCtx, cancelLogin := context.WithTimeout(ctx, 10*time.Minute)
	defer cancelLogin()
	token, err := browserlogin.Login(loginCtx, func(ctx context.Context, address string) (string, error) {
		if _, err := fmt.Fprintf(output, "在平时使用的浏览器中打开下面的地址，完成 Twitch 登录和二步验证：\n%s\n\n登录完成后，复制地址栏的完整地址，粘贴到这里并按回车（输入不会显示）：\n", address); err != nil {
			return "", errors.New("输出授权提示失败")
		}
		callback, err := readManualCallback(ctx, input)
		if _, outputErr := fmt.Fprintln(output); outputErr != nil {
			return "", errors.Join(err, errors.New("输出授权输入提示失败"))
		}
		return callback, err
	})
	if err != nil {
		return err
	}
	// 浏览器等待不占用网络校验时间，校验成功后直接保存到本运行目录的 Cookie
	validateCtx, cancelValidate := context.WithTimeout(ctx, time.Minute)
	defer cancelValidate()
	if err := importer.ImportSession(validateCtx, auth.Session{
		Version: auth.SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: token,
	}); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "登录成功，Cookie 已保存"); err != nil {
		return errors.New("输出登录结果失败")
	}
	return nil
}

func ensureStartupLogin(ctx context.Context, canInteract bool, validate func(context.Context) error, login func(context.Context) error, logger *slog.Logger) error {
	if !canInteract {
		return nil
	}
	validateCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err := validate(validateCtx)
	cancel()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !errors.Is(err, auth.ErrLoginRequired) {
		logger.Warn("启动时暂未完成认证校验，将由调度器继续重试", "error", err)
		return nil
	}
	return login(ctx)
}

func readManualCallback(ctx context.Context, input io.Reader) (callback string, resultErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	readLine := func() (string, error) {
		line, err := bufio.NewReader(io.LimitReader(input, maxManualCallbackBytes+1)).ReadString('\n')
		if len(line) > maxManualCallbackBytes {
			return "", errors.New("授权结果地址过长")
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", errors.New("读取授权结果失败")
		}
		return strings.TrimSpace(line), nil
	}
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		state, err := term.MakeRaw(int(file.Fd()))
		if err != nil {
			return "", errors.New("无法关闭终端输入回显")
		}
		defer func() {
			if err := term.Restore(int(file.Fd()), state); err != nil {
				callback = ""
				resultErr = errors.Join(resultErr, errors.New("恢复终端输入模式失败"))
			}
		}()
		// 先关闭回显，再启动读取，取消时不会被读取协程重新改回终端模式
		readLine = func() (string, error) {
			return readHiddenCallback(input)
		}
	}
	return waitManualCallback(ctx, readLine)
}

func readHiddenCallback(input io.Reader) (string, error) {
	terminal := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{input, io.Discard}, "")
	line, err := terminal.ReadPassword("")
	if errors.Is(err, io.EOF) {
		return "", context.Canceled
	}
	if err != nil {
		return "", errors.New("读取授权结果失败")
	}
	return strings.TrimSpace(line), nil
}

func waitManualCallback(ctx context.Context, readLine func() (string, error)) (string, error) {
	type result struct {
		line string
		err  error
	}
	// CLI 只接收一次输入，取消后直接退出，不重复创建仍阻塞的读取协程
	completed := make(chan result, 1)
	go func() {
		line, err := readLine()
		completed <- result{line: line, err: err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case value := <-completed:
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return value.line, value.err
	}
}
