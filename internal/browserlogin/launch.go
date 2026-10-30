package browserlogin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const browserStartupTimeout = 20 * time.Second

type browserLaunch struct {
	port          int
	profile       string
	timeout       time.Duration
	ownedEndpoint string
}

func newBrowserLaunch(profile string) (browserLaunch, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return browserLaunch{}, errors.New("分配浏览器调试端口失败")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return browserLaunch{}, errors.New("释放浏览器调试端口失败")
	}
	return browserLaunch{port: port, profile: profile, timeout: browserStartupTimeout}, nil
}

func (launch browserLaunch) arguments(proxy string) []string {
	// 端口 0 会让 Chromium 开启自动化标记，使用实际分配的端口保留普通浏览器环境
	args := []string{"--remote-debugging-address=127.0.0.1", "--remote-debugging-port=" + strconv.Itoa(launch.port), "--user-data-dir=" + launch.profile, "--no-first-run", "--no-default-browser-check", "--disable-extensions", "--disable-component-extensions-with-background-pages", "--no-service-autorun", "--homepage=about:blank", "--no-pings", "--disable-session-crashed-bubble", "--disable-search-engine-choice-screen"}
	if proxy != "" {
		args = append(args, "--proxy-server="+proxy)
	}
	return append(args, "about:blank")
}

func waitDebugger(ctx context.Context, launch *browserLaunch, exited <-chan error) (string, error) {
	startup, cancel := context.WithTimeout(ctx, launch.timeout)
	defer cancel()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-startup.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("浏览器调试接口启动超时，端口可能被占用，请重试")
		case err := <-exited:
			if err != nil {
				return "", errors.New("浏览器启动失败")
			}
			// Edge 的启动器可以先正常退出，实际窗口由另一个浏览器进程继续运行
			exited = nil
		case <-ticker.C:
			endpoint, err := discoverDebugger(startup, client, launch)
			if err == nil && endpoint != "" {
				return endpoint, nil
			}
		}
	}
}

func discoverDebugger(ctx context.Context, client *http.Client, launch *browserLaunch) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/version", launch.port), nil)
	if err != nil {
		return "", errors.New("创建浏览器连接失败")
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	var version struct {
		Endpoint string `json:"webSocketDebuggerUrl"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&version)
	closeErr := response.Body.Close()
	if err := errors.Join(decodeErr, closeErr); err != nil {
		return "", err
	}
	if response.StatusCode != http.StatusOK || !validDebuggerEndpoint(version.Endpoint, launch.port, "/devtools/browser/") {
		return "", errors.New("浏览器调试接口尚未就绪")
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: time.Second, Proxy: nil}
	connection, _, err := dialer.DialContext(probe, version.Endpoint, nil)
	if err != nil {
		return "", errors.New("连接浏览器调试接口失败")
	}
	defer func() { _ = connection.Close() }()
	stop := context.AfterFunc(probe, func() { _ = connection.Close() })
	defer stop()
	var info struct {
		CommandLine string `json:"commandLine"`
	}
	if err := debuggerCall(connection, 1, "SystemInfo.getInfo", &info); err != nil {
		return "", err
	}
	// 调试端口可能被其他进程占用，必须由浏览器自身确认本次独立配置目录
	if !ownsBrowserProfile(info.CommandLine, launch.profile) {
		return "", errors.New("调试接口不属于本次登录浏览器")
	}
	launch.ownedEndpoint = version.Endpoint
	var targets struct {
		Infos []struct {
			ID   string `json:"targetId"`
			Type string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := debuggerCall(connection, 2, "Target.getTargets", &targets); err != nil {
		return "", err
	}
	for _, target := range targets.Infos {
		if target.Type == "page" && validTargetID(target.ID) {
			return fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/%s", launch.port, target.ID), nil
		}
	}
	return "", nil
}

func debuggerCall(connection *websocket.Conn, id int, method string, result any) error {
	if err := connection.WriteJSON(map[string]any{"id": id, "method": method}); err != nil {
		return errors.New("发送浏览器确认请求失败")
	}
	for {
		var response struct {
			ID     int             `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := connection.ReadJSON(&response); err != nil {
			return errors.New("读取浏览器确认结果失败")
		}
		if response.ID != id {
			continue
		}
		if len(response.Error) > 0 {
			return errors.New("浏览器拒绝确认请求")
		}
		if err := json.Unmarshal(response.Result, result); err != nil {
			return errors.New("浏览器确认结果无效")
		}
		return nil
	}
}

func validDebuggerEndpoint(raw string, port int, prefix string) bool {
	endpoint, err := url.Parse(raw)
	return err == nil && endpoint.Scheme == "ws" && endpoint.Host == fmt.Sprintf("127.0.0.1:%d", port) && endpoint.User == nil && strings.HasPrefix(endpoint.Path, prefix) && validTargetID(strings.TrimPrefix(endpoint.Path, prefix)) && endpoint.RawQuery == "" && endpoint.Fragment == ""
}

func validTargetID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func ownsBrowserProfile(commandLine, profile string) bool {
	if profile == "" {
		return false
	}
	for _, argument := range []string{"--user-data-dir=" + profile, "--user-data-dir=\"" + profile + "\"", "\"--user-data-dir=" + profile + "\"", "--user-data-dir='" + profile + "'", "'--user-data-dir=" + profile + "'"} {
		offset := 0
		for offset < len(commandLine) {
			index := strings.Index(commandLine[offset:], argument)
			if index < 0 {
				break
			}
			index += offset
			end := index + len(argument)
			if (index == 0 || commandLine[index-1] == ' ' || commandLine[index-1] == '\t') && (end == len(commandLine) || commandLine[end] == ' ' || commandLine[end] == '\t') {
				return true
			}
			offset = index + 1
		}
	}
	return false
}

func closeOwnedBrowser(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	dialer := websocket.Dialer{HandshakeTimeout: time.Second, Proxy: nil}
	connection, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return errors.New("关闭登录浏览器失败")
	}
	defer func() { _ = connection.Close() }()
	if err := connection.WriteJSON(map[string]any{"id": 99, "method": "Browser.close"}); err != nil {
		return errors.New("关闭登录浏览器失败")
	}
	return nil
}
