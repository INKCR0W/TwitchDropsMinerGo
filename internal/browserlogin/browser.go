package browserlogin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		for attempt := 0; attempt < 50; attempt++ {
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
	launch, err := newBrowserLaunch(profile)
	if err != nil {
		return "", err
	}
	command := exec.Command(binary, launch.arguments(options.Proxy)...)
	if err := command.Start(); err != nil {
		return "", errors.New("启动浏览器失败")
	}
	exited := make(chan error, 1)
	processDone := make(chan struct{})
	go func() { exited <- command.Wait(); close(processDone) }()
	defer func() {
		select {
		case <-processDone:
			return
		case <-time.After(time.Second):
		}
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			resultErr = errors.Join(resultErr, errors.New("关闭登录浏览器失败"))
			token = ""
		}
		select {
		case <-processDone:
		case <-time.After(5 * time.Second):
			resultErr = errors.Join(resultErr, errors.New("等待登录浏览器退出超时"))
			token = ""
		}
	}()
	endpoint, err := waitDebugger(ctx, &launch, exited)
	if err != nil {
		return "", errors.Join(err, closeOwnedBrowser(launch.ownedEndpoint))
	}
	token, err = capture(ctx, endpoint, address, state)
	if err != nil {
		return "", errors.Join(err, closeOwnedBrowser(launch.ownedEndpoint))
	}
	return token, nil
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
			_ = connection.Close()
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
