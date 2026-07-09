package inventory

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"twitchdropsminergo/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func validCampaignMap(id string, gameID float64) map[string]any {
	return map[string]any{
		"id":      id,
		"name":    "Campaign " + id,
		"status":  "ACTIVE",
		"game":    map[string]any{"id": gameID, "name": "Game", "slug": "game"},
		"startAt": "2026-01-01T00:00:00Z",
		"endAt":   "2030-01-01T00:00:00Z",
		"timeBasedDrops": []any{
			map[string]any{
				"id":                     "drop-" + id,
				"name":                   "Drop " + id,
				"startAt":                "2026-01-01T00:00:00Z",
				"endAt":                  "2030-01-01T00:00:00Z",
				"requiredMinutesWatched": float64(30),
				"benefitEdges":           []any{},
			},
		},
	}
}

func TestBuildCampaignsSkipsMalformedCampaign(t *testing.T) {
	t.Parallel()

	bad := validCampaignMap("bad", 2)
	delete(bad, "endAt") // 缺少结束时间 -> buildCampaign 报错

	payload := map[string]any{
		"good": validCampaignMap("good", 1),
		"bad":  bad,
	}

	campaigns, err := buildCampaigns(payload, nil, discardLogger())
	if err != nil {
		t.Fatalf("单个畸形 campaign 不应让整批失败: %v", err)
	}
	if len(campaigns) != 1 {
		t.Fatalf("期望保留 1 个有效 campaign，实际 %d", len(campaigns))
	}
	if campaigns[0].ID != "good" {
		t.Fatalf("保留的 campaign 不正确: %q", campaigns[0].ID)
	}
}

func TestBuildCampaignsRejectsDropMissingRequiredMinutes(t *testing.T) {
	t.Parallel()

	campaign := validCampaignMap("c1", 1)
	drops := campaign["timeBasedDrops"].([]any)
	delete(drops[0].(map[string]any), "requiredMinutesWatched") // 模拟 API 字段改名/缺失

	campaigns, err := buildCampaigns(map[string]any{"c1": campaign}, nil, discardLogger())
	if err != nil {
		t.Fatalf("缺字段应被跳过而非返回错误: %v", err)
	}
	// 该 campaign 因 drop 缺少 requiredMinutesWatched 被跳过，而不是静默解析成 0
	if len(campaigns) != 0 {
		t.Fatalf("期望跳过缺少 requiredMinutesWatched 的 campaign，实际保留 %d", len(campaigns))
	}
}

func TestInferClaimedByBenefitsRequiresEveryAwardedBenefitInWindow(t *testing.T) {
	t.Parallel()

	startsAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	endsAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	benefits := []domain.Benefit{
		{ID: "badge", Type: domain.BenefitTypeBadge},
		{ID: "emote", Type: domain.BenefitTypeEmote},
	}

	cases := []struct {
		name            string
		claimedBenefits map[string]time.Time
		want            bool
	}{
		{
			name:            "无任何 benefit 被领取",
			claimedBenefits: map[string]time.Time{},
			want:            false,
		},
		{
			name: "全部 benefit 都在窗口内领取",
			claimedBenefits: map[string]time.Time{
				"badge": startsAt.Add(time.Hour),
				"emote": endsAt.Add(-time.Hour),
			},
			want: true,
		},
		{
			name: "仅部分 benefit 在窗口内，另一枚在窗口前",
			claimedBenefits: map[string]time.Time{
				"badge": startsAt.Add(time.Hour),
				"emote": startsAt.Add(-time.Hour),
			},
			want: false,
		},
		{
			name: "仅部分 benefit 在窗口内，另一枚在窗口后",
			claimedBenefits: map[string]time.Time{
				"badge": startsAt.Add(time.Hour),
				"emote": endsAt,
			},
			want: false,
		},
		{
			name: "只出现一枚 benefit 且在窗口内",
			claimedBenefits: map[string]time.Time{
				"badge": startsAt,
			},
			want: true,
		},
		{
			name: "不属于该 drop 的 benefit 即使在窗口外也应忽略",
			claimedBenefits: map[string]time.Time{
				"badge":   startsAt,
				"emote":   endsAt.Add(-time.Minute),
				"foreign": endsAt.Add(time.Hour),
			},
			want: true,
		},
	}

	for _, testCase := range cases {
		if got := inferClaimedByBenefits(benefits, testCase.claimedBenefits, startsAt, endsAt); got != testCase.want {
			t.Errorf("%s: inferClaimedByBenefits() = %v, want %v", testCase.name, got, testCase.want)
		}
	}
}
