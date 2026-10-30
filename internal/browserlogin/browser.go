package browserlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type Options struct {
	BrowserPath string
	Proxy       string
}

func Login(ctx context.Context, options Options) (token string, resultErr error) {
	address, state, err := loginURL()
	if err != nil {
		return "", err
	}
	binary, err := findBrowser(options.BrowserPath)
	if err != nil {
		return "", err
	}
	profile, err := os.MkdirTemp("", "tdm-login-")
	if err != nil {
		return "", errors.New("创建浏览器临时目录失败")
	}
	defer func() {
		var cleanupErr error
		for attempt := 0; attempt < 10; attempt++ {
			cleanupErr = os.RemoveAll(profile)
			if cleanupErr == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if cleanupErr != nil {
			resultErr = errors.Join(resultErr, errors.New("浏览器临时登录目录清理失败，请删除该目录："+profile))
			token = ""
		}
	}()
	args := []string{"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0", "--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check", "about:blank"}
	if options.Proxy != "" {
		args = append([]string{"--proxy-server=" + options.Proxy}, args...)
	}
	command := exec.Command(binary, args...)
	if err := command.Start(); err != nil {
		return "", errors.New("启动浏览器失败")
	}
	exited := make(chan struct{})
	go func() { _ = command.Wait(); close(exited) }()
	defer func() {
		select {
		case <-exited:
			return
		case <-time.After(time.Second):
		}
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			resultErr = errors.Join(resultErr, errors.New("关闭登录浏览器失败"))
			token = ""
		}
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			resultErr = errors.Join(resultErr, errors.New("等待登录浏览器退出超时"))
			token = ""
		}
	}()
	endpoint, err := waitDebugger(ctx, profile, exited)
	if err != nil {
		return "", err
	}
	return capture(ctx, endpoint, address, state)
}

func findBrowser(requested string) (string, error) {
	if requested != "" {
		if p, err := exec.LookPath(requested); err == nil {
			return p, nil
		}
		return "", errors.New("指定的浏览器不存在")
	}
	candidates := []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "chrome", "msedge"}
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if base != "" {
				candidates = append(candidates, filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"), filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
			}
		}
	}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates, "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge")
	}
	for _, candidate := range candidates {
		if p, err := exec.LookPath(candidate); err == nil {
			return p, nil
		}
	}
	return "", errors.New("没有找到 Chrome 或 Edge，请使用 -browser 指定浏览器路径")
}

func waitDebugger(ctx context.Context, profile string, exited <-chan struct{}) (string, error) {
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-exited:
			return "", errors.New("浏览器已关闭")
		case <-ticker.C:
		}
		data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
		if err != nil {
			continue
		}
		parts := strings.Split(string(data), "\n")
		port, err := strconv.Atoi(parts[0])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), nil)
		if err != nil {
			return "", errors.New("创建浏览器连接失败")
		}
		response, err := client.Do(req)
		if err != nil {
			continue
		}
		var targets []struct {
			Type string `json:"type"`
			URL  string `json:"webSocketDebuggerUrl"`
		}
		err = json.NewDecoder(http.MaxBytesReader(nil, response.Body, 1<<20)).Decode(&targets)
		closeErr := response.Body.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			continue
		}
		prefix := fmt.Sprintf("ws://127.0.0.1:%d/", port)
		for _, target := range targets {
			if target.Type == "page" && strings.HasPrefix(target.URL, prefix) {
				return target.URL, nil
			}
		}
	}
}

func capture(ctx context.Context, endpoint, address, state string) (token string, resultErr error) {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Proxy: nil}
	connection, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return "", errors.New("连接浏览器失败")
	}
	defer func() {
		closeErr := connection.WriteJSON(map[string]any{"id": 99, "method": "Browser.close"})
		if err := connection.Close(); closeErr == nil {
			closeErr = err
		}
		if closeErr != nil && resultErr == nil {
			resultErr = errors.New("关闭登录浏览器连接失败")
			token = ""
		}
	}()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			connection.Close()
		case <-done:
		}
	}()
	if err = connection.WriteJSON(map[string]any{"id": 1, "method": "Page.enable"}); err != nil {
		return "", errors.New("初始化浏览器监听失败")
	}
	rootFrame := ""
	for {
		var event struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Error  json.RawMessage `json:"error"`
			Params struct {
				Frame struct {
					ID       string `json:"id"`
					URL      string `json:"url"`
					ParentID string `json:"parentId"`
				} `json:"frame"`
				URL     string `json:"url"`
				FrameID string `json:"frameId"`
			} `json:"params"`
		}
		if err = connection.ReadJSON(&event); err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("浏览器已关闭或连接中断")
		}
		if len(event.Error) > 0 {
			return "", errors.New("浏览器拒绝授权页面操作")
		}
		if event.ID == 1 {
			if err = connection.WriteJSON(map[string]any{"id": 2, "method": "Page.navigate", "params": map[string]string{"url": address}}); err != nil {
				return "", errors.New("打开授权页面失败")
			}
		}
		if event.Method == "Page.navigatedWithinDocument" && rootFrame != "" && event.Params.FrameID == rootFrame {
			token, err := callbackToken(event.Params.URL, state)
			if err != nil || token != "" {
				return token, err
			}
		}
		if event.Method == "Page.frameNavigated" && event.Params.Frame.ParentID == "" {
			rootFrame = event.Params.Frame.ID
			token, err := callbackToken(event.Params.Frame.URL, state)
			if err != nil || token != "" {
				return token, err
			}
		}
	}
}
