package watch

import (
	"testing"

	"twitchdropsminergo/internal/gql"
)

func TestParseAvailableDropsTreatsErrorResponseAsUnknown(t *testing.T) {
	t.Parallel()

	// GQL 层遇到 server error 会把出错路径填成 null 并当作成功返回
	response := gql.Response{
		Data:   map[string]any{"channel": nil},
		Errors: []gql.ResponseError{{Message: "server error", Path: []any{"channel"}}},
	}

	ids, err := parseAvailableDropsResponse(response)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if ids != nil {
		t.Fatalf("出错响应必须判为未知(nil), 否则会把频道误判为无掉宝可拿: %#v", ids)
	}
}

func TestParseAvailableDropsTreatsEmptyAnswerAsKnown(t *testing.T) {
	t.Parallel()

	response := gql.Response{Data: map[string]any{"channel": nil}}

	ids, err := parseAvailableDropsResponse(response)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if ids == nil {
		t.Fatal("Twitch 明确回答无掉宝可拿时应返回非 nil 空切片")
	}
	if len(ids) != 0 {
		t.Fatalf("应为空: %#v", ids)
	}
}

func TestParseAvailableDropsTreatsMissingFieldAsUnknown(t *testing.T) {
	t.Parallel()

	response := gql.Response{Data: map[string]any{"channel": map[string]any{"id": "1"}}}

	ids, err := parseAvailableDropsResponse(response)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if ids != nil {
		t.Fatalf("字段缺失应判为未知(nil), 否则一次 schema 漂移会门控掉所有特殊活动: %#v", ids)
	}
}
