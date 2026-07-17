package mainland

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	dohTTLMin = 60 * time.Second
	dohTTLMax = 3600 * time.Second
)

type dnsAnswer struct {
	Name string `json:"name"`
	Type int    `json:"type"`
	Data string `json:"data"`
	TTL  int    `json:"TTL"`
}

type dohResult struct {
	IPs    []string
	CNAMEs []string
	TTL    int
}

func parseDoHAnswers(body []byte) (dohResult, error) {
	var payload struct {
		Answer []dnsAnswer `json:"Answer"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return dohResult{}, fmt.Errorf("解析 DoH 响应失败: %w", err)
	}
	var out dohResult
	minTTL := -1
	for _, a := range payload.Answer {
		switch a.Type {
		case 1: // A
			out.IPs = append(out.IPs, a.Data)
			if minTTL < 0 || a.TTL < minTTL {
				minTTL = a.TTL
			}
		case 5: // CNAME
			out.CNAMEs = append(out.CNAMEs, strings.TrimSuffix(a.Data, "."))
		}
	}
	if minTTL < 0 {
		minTTL = 0
	}
	out.TTL = minTTL
	return out, nil
}

type cacheEntry struct {
	result  dohResult
	expires time.Time
}

type resolver struct {
	httpGet func(ctx context.Context, url string) ([]byte, error)
	clock   func() time.Time
	mu      sync.Mutex
	cache   map[string]cacheEntry
}

func newResolver(httpGet func(ctx context.Context, url string) ([]byte, error), clock func() time.Time) *resolver {
	return &resolver{httpGet: httpGet, clock: clock, cache: map[string]cacheEntry{}}
}

func (r *resolver) resolve(ctx context.Context, host string) (dohResult, error) {
	now := r.clock()
	r.mu.Lock()
	if e, ok := r.cache[host]; ok && now.Before(e.expires) {
		r.mu.Unlock()
		return e.result, nil
	}
	r.mu.Unlock()

	q := "https://doh-endpoint/dns-query?" + url.Values{
		"name": {host}, "type": {"A"},
	}.Encode()
	body, err := r.httpGet(ctx, q)
	if err != nil {
		return dohResult{}, fmt.Errorf("DoH 查询 %s 失败: %w", host, err)
	}
	res, err := parseDoHAnswers(body)
	if err != nil {
		return dohResult{}, err
	}

	ttl := time.Duration(res.TTL) * time.Second
	if ttl < dohTTLMin {
		ttl = dohTTLMin
	}
	if ttl > dohTTLMax {
		ttl = dohTTLMax
	}
	r.mu.Lock()
	r.cache[host] = cacheEntry{result: res, expires: now.Add(ttl)}
	r.mu.Unlock()
	return res, nil
}

func (r *resolver) invalidate(host string) {
	r.mu.Lock()
	delete(r.cache, host)
	r.mu.Unlock()
}
