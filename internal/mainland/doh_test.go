package mainland

import (
	"context"
	"testing"
	"time"
)

func TestParseDoHAnswers(t *testing.T) {
	t.Parallel()
	body := []byte(`{"Status":0,"Answer":[
		{"name":"spade.twitch.tv","type":5,"data":"spade.sci.twitch.tv.","TTL":300},
		{"name":"spade.sci.twitch.tv","type":5,"data":"science-edge.us-west-2.elb.amazonaws.com.","TTL":60},
		{"name":"science-edge.us-west-2.elb.amazonaws.com","type":1,"data":"52.43.15.68","TTL":45},
		{"name":"science-edge.us-west-2.elb.amazonaws.com","type":1,"data":"34.212.216.197","TTL":30}
	]}`)
	got, err := parseDoHAnswers(body)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(got.IPs) != 2 || got.IPs[0] != "52.43.15.68" || got.IPs[1] != "34.212.216.197" {
		t.Fatalf("A 记录解析错误: %#v", got.IPs)
	}
	wantCN := []string{"spade.sci.twitch.tv", "science-edge.us-west-2.elb.amazonaws.com"}
	if len(got.CNAMEs) != 2 || got.CNAMEs[0] != wantCN[0] || got.CNAMEs[1] != wantCN[1] {
		t.Fatalf("CNAME 链解析错误(应去尾点): %#v", got.CNAMEs)
	}
	if got.TTL != 30 {
		t.Fatalf("TTL 应取 A 记录最小值 30, 实际 %d", got.TTL)
	}
}

func TestParseDoHAnswersZeroTTLWins(t *testing.T) {
	t.Parallel()
	body := []byte(`{"Answer":[{"name":"x","type":1,"data":"1.2.3.4","TTL":0},{"name":"x","type":1,"data":"5.6.7.8","TTL":3600}]}`)
	got, err := parseDoHAnswers(body)
	if err != nil {
		t.Fatal(err)
	}
	if got.TTL != 0 {
		t.Fatalf("含 TTL:0 的 A 记录时最小 TTL 应为 0, 实际 %d", got.TTL)
	}
}

func TestResolverCachesUntilTTL(t *testing.T) {
	t.Parallel()
	calls := 0
	now := time.Unix(1000, 0)
	body := []byte(`{"Answer":[{"name":"id.twitch.tv","type":1,"data":"35.166.45.5","TTL":100}]}`)
	get := func(ctx context.Context, url string) ([]byte, error) { calls++; return body, nil }
	r := newResolver(get, func() time.Time { return now })

	if _, err := r.resolve(context.Background(), "id.twitch.tv"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve(context.Background(), "id.twitch.tv"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("TTL 内应命中缓存, httpGet 调用应为 1, 实际 %d", calls)
	}

	now = now.Add(101 * time.Second) // 超过钳制下限 60s 与应答 TTL 100s
	if _, err := r.resolve(context.Background(), "id.twitch.tv"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("过期后应重新查询, 实际调用 %d", calls)
	}
}

func TestResolverInvalidate(t *testing.T) {
	t.Parallel()
	calls := 0
	body := []byte(`{"Answer":[{"name":"id.twitch.tv","type":1,"data":"35.166.45.5","TTL":100}]}`)
	get := func(ctx context.Context, url string) ([]byte, error) { calls++; return body, nil }
	r := newResolver(get, func() time.Time { return time.Unix(1000, 0) })
	_, _ = r.resolve(context.Background(), "id.twitch.tv")
	r.invalidate("id.twitch.tv")
	_, _ = r.resolve(context.Background(), "id.twitch.tv")
	if calls != 2 {
		t.Fatalf("invalidate 后应重新查询, 实际 %d", calls)
	}
}
