package browserlogin

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gorilla/websocket"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBrowserLaunchUsesNonzeroPortAndDisablesExtensions(t *testing.T) {
	launch, err := newBrowserLaunch("test profile")
	if err != nil {
		t.Fatal(err)
	}
	if launch.port <= 0 || launch.port > 65535 {
		t.Fatal("分配的调试端口无效")
	}
	args := launch.arguments("http://127.0.0.1:1234")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--remote-debugging-port="+strconv.Itoa(launch.port)) || args[len(args)-1] != "about:blank" {
		t.Fatal("启动参数未保留实际端口或空白页")
	}
	for _, forbidden := range []string{"--remote-debugging-port=0", "--enable-automation", "--headless", "--disable-web-security", "--ignore-certificate-errors", "--user-agent", "AutomationControlled"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("启动参数包含不需要的浏览器修改: %s", forbidden)
		}
	}
	for _, required := range []string{"--disable-extensions", "--disable-component-extensions-with-background-pages", "--proxy-server=http://127.0.0.1:1234"} {
		found := false
		for _, arg := range args {
			if arg == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺少启动参数: %s", required)
		}
	}
}

func TestBrowserProfileProofRequiresCompleteArgument(t *testing.T) {
	for _, test := range []struct {
		command, profile string
		owned            bool
	}{
		{`chrome --user-data-dir=/tmp/profile --no-first-run`, `/tmp/profile`, true},
		{`chrome --user-data-dir="/tmp/test profile" --no-first-run`, `/tmp/test profile`, true},
		{`chrome "--user-data-dir=C:\Users\test profile" --no-first-run`, `C:\Users\test profile`, true},
		{`chrome --user-data-dir="C:\Users\test profile" --no-first-run`, `C:\Users\test profile`, true},
		{`chrome --user-data-dir=/tmp/profile-other`, `/tmp/profile`, false},
		{`chrome --user-data-dir=/tmp/profile/other`, `/tmp/profile`, false},
		{`chrome --another="--user-data-dir=/tmp/profile"`, `/tmp/profile`, false},
		{`chrome --some-path=/tmp/profile`, `/tmp/profile`, false},
		{`chrome --user-data-dir=/tmp/profile`, "", false},
	} {
		if ownsBrowserProfile(test.command, test.profile) != test.owned {
			t.Fatalf("配置目录归属判定不符: owned=%v", test.owned)
		}
	}
}

type debuggerFixture struct {
	commandLine, endpoint, targetID, targetType string
	stall, noPage                               bool
	infoDelay                                   time.Duration
	targetCalls                                 atomic.Int32
	closeCalls                                  atomic.Int32
}

func newDebuggerFixture(t *testing.T, fixture *debuggerFixture) (*httptest.Server, *browserLaunch) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			endpoint := fixture.endpoint
			if endpoint == "" {
				endpoint = "ws://" + r.Host + "/devtools/browser/owned"
			}
			endpoint = strings.ReplaceAll(endpoint, "HOST", r.Host)
			if err := json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": endpoint}); err != nil {
				t.Error(err)
			}
			return
		}
		if r.URL.Path != "/devtools/browser/owned" {
			http.NotFound(w, r)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		for {
			var command struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			if err := c.ReadJSON(&command); err != nil {
				return
			}
			if fixture.stall {
				continue
			}
			var result any
			switch command.Method {
			case "SystemInfo.getInfo":
				if fixture.infoDelay > 0 {
					time.Sleep(fixture.infoDelay)
				}
				line := fixture.commandLine
				if line == "" {
					line = `msedge --user-data-dir="test profile" --disable-extensions`
				}
				result = map[string]string{"commandLine": line}
			case "Target.getTargets":
				fixture.targetCalls.Add(1)
				targets := []map[string]string{}
				if !fixture.noPage {
					id, kind := fixture.targetID, fixture.targetType
					if id == "" {
						id = "page1"
					}
					if kind == "" {
						kind = "page"
					}
					targets = append(targets, map[string]string{"targetId": id, "type": kind})
				}
				result = map[string]any{"targetInfos": targets}
			case "Browser.close":
				fixture.closeCalls.Add(1)
				return
			default:
				t.Error("意外的浏览器确认方法")
				return
			}
			if err := c.WriteJSON(map[string]any{"id": command.ID, "result": result}); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return server, &browserLaunch{port: port, profile: "test profile", timeout: time.Second}
}

func TestDiscoveryVerifiesProfileBeforeSelectingEdgeNewTab(t *testing.T) {
	fixture := &debuggerFixture{}
	server, launch := newDebuggerFixture(t, fixture)
	defer server.Close()
	endpoint, err := discoverDebugger(context.Background(), server.Client(), launch)
	if err != nil || !strings.HasSuffix(endpoint, "/devtools/page/page1") || launch.ownedEndpoint == "" || fixture.targetCalls.Load() != 1 {
		t.Fatal("没有确认本次实例并选择页面", err)
	}
}

func TestDiscoveryRejectsForeignProfileAndUnsafeBrowserEndpoint(t *testing.T) {
	for _, test := range []struct{ name, command, endpoint string }{
		{name: "other profile", command: `msedge --user-data-dir="another profile" --proxy-server=http://user:secret@example.test`},
		{name: "prefix only", command: `msedge --user-data-dir="test profile-more"`},
		{name: "remote", endpoint: "ws://example.test/devtools/browser/owned"},
		{name: "wrong port", endpoint: "ws://127.0.0.1:1/devtools/browser/owned"},
		{name: "http scheme", endpoint: "http://HOST/devtools/browser/owned"},
		{name: "page endpoint", endpoint: "ws://HOST/devtools/page/owned"},
		{name: "credentials", endpoint: "ws://user@HOST/devtools/browser/owned"},
		{name: "query", endpoint: "ws://HOST/devtools/browser/owned?token=secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &debuggerFixture{commandLine: test.command, endpoint: test.endpoint}
			server, launch := newDebuggerFixture(t, fixture)
			defer server.Close()
			endpoint, err := discoverDebugger(context.Background(), server.Client(), launch)
			if err == nil || endpoint != "" || launch.ownedEndpoint != "" || fixture.targetCalls.Load() != 0 {
				t.Fatal("连接了未确认的浏览器实例")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("错误暴露浏览器命令行")
			}
		})
	}
}

func TestDiscoveryRejectsNonPageAndMalformedTargetIDs(t *testing.T) {
	for _, fixture := range []*debuggerFixture{{targetType: "service_worker"}, {targetID: "../browser/other"}, {noPage: true}} {
		server, launch := newDebuggerFixture(t, fixture)
		endpoint, err := discoverDebugger(context.Background(), server.Client(), launch)
		server.Close()
		if err != nil || endpoint != "" || launch.ownedEndpoint == "" {
			t.Fatal("错误处理目标列表", err)
		}
	}
}

func TestNormalEdgeLauncherExitDoesNotStopDiscovery(t *testing.T) {
	server, launch := newDebuggerFixture(t, &debuggerFixture{})
	defer server.Close()
	exited := make(chan error, 1)
	exited <- nil
	endpoint, err := waitDebugger(context.Background(), launch, exited)
	if err != nil || endpoint == "" {
		t.Fatal("Edge启动器正常退出被误当浏览器关闭", err)
	}
}

func TestWaitDebuggerAllowsSlowBrowserConfirmation(t *testing.T) {
	fixture := &debuggerFixture{infoDelay: 1200 * time.Millisecond}
	server, launch := newDebuggerFixture(t, fixture)
	defer server.Close()
	launch.timeout = 3500 * time.Millisecond
	endpoint, err := waitDebugger(context.Background(), launch, make(chan error))
	if err != nil || endpoint == "" || fixture.targetCalls.Load() != 1 {
		t.Fatal("正常的浏览器确认被过短的探测时限中断", err)
	}
}

func TestWaitDebuggerBoundsStartupCancellationAndFailedLauncher(t *testing.T) {
	server, launch := newDebuggerFixture(t, &debuggerFixture{commandLine: `msedge --user-data-dir=foreign`})
	defer server.Close()
	launch.timeout = 160 * time.Millisecond
	if _, err := waitDebugger(context.Background(), launch, make(chan error)); err == nil || !strings.Contains(err.Error(), "启动超时") {
		t.Fatal("错误实例未在启动时限内失败", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitDebugger(ctx, launch, make(chan error)); !errors.Is(err, context.Canceled) {
		t.Fatal("取消未传回调用方", err)
	}
	exited := make(chan error, 1)
	exited <- errors.New("exit status 1")
	if _, err := waitDebugger(context.Background(), launch, exited); err == nil || !strings.Contains(err.Error(), "启动失败") {
		t.Fatal("未识别浏览器启动失败", err)
	}
}

func TestWaitDebuggerBoundsUnresponsiveCDP(t *testing.T) {
	server, launch := newDebuggerFixture(t, &debuggerFixture{stall: true})
	defer server.Close()
	launch.timeout = 160 * time.Millisecond
	if _, err := waitDebugger(context.Background(), launch, make(chan error)); err == nil || !strings.Contains(err.Error(), "启动超时") {
		t.Fatal("未终止无响应的浏览器确认", err)
	}
}

func TestWaitDebuggerDoesNotFollowHTTPRedirect(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	launch := &browserLaunch{port: port, profile: "test profile", timeout: 160 * time.Millisecond}
	if _, err := waitDebugger(context.Background(), launch, make(chan error)); err == nil || redirected.Load() {
		t.Fatal("发现端点跟随了不可信的HTTP跳转")
	}
}
