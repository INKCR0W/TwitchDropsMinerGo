package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"twitchdropsminergo/internal/auth"
	"twitchdropsminergo/internal/browserlogin"
	"twitchdropsminergo/internal/httpclient"
)

type options struct {
	Output, Browser, Proxy string
	Timeout                time.Duration
}

func parseArgs(args []string) (options, error) {
	o := options{}
	f := flag.NewFlagSet("miner-login", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.Output, "output", "runtime/state/android-session.json", "授权会话输出文件")
	f.StringVar(&o.Browser, "browser", "", "Chrome 或 Edge 路径")
	f.StringVar(&o.Proxy, "proxy", "", "登录使用的代理")
	f.DurationVar(&o.Timeout, "timeout", 10*time.Minute, "登录等待时间")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || o.Output == "" || o.Timeout <= 0 {
		return o, fmt.Errorf("输出路径或登录等待时间无效")
	}
	return o, nil
}
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, output, stderr io.Writer) int {
	return runWithLogin(args, output, stderr, browserlogin.Login)
}

func runWithLogin(args []string, output, stderr io.Writer, login func(context.Context, browserlogin.Options) (string, error)) int {
	o, err := parseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "参数无效，请检查 -output、-browser、-proxy 和 -timeout")
		return 2
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, o.Timeout)
	defer cancel()
	if _, err := fmt.Fprintln(output, "请在打开的浏览器中完成 Twitch 登录和二步验证"); err != nil {
		_, _ = fmt.Fprintln(stderr, "输出登录提示失败")
		return 1
	}
	token, err := login(ctx, browserlogin.Options{BrowserPath: o.Browser, Proxy: o.Proxy})
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	// 本机只导出浏览器授权结果，令牌有效性和账号归属由运行服务的环境校验
	session := auth.Session{Version: auth.SessionVersion, ClientID: httpclient.AndroidAppClient.ClientID, AccessToken: token}
	if err = auth.WriteSession(o.Output, session); err != nil {
		_, _ = fmt.Fprintln(stderr, "保存授权会话失败")
		return 1
	}
	if _, err := fmt.Fprintf(output, "浏览器授权已完成，会话已保存至 %s\n请在运行服务的环境导入并校验\n", o.Output); err != nil {
		_, _ = fmt.Fprintln(stderr, "输出授权结果失败")
		return 1
	}
	return 0
}
