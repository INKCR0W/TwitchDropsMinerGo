package gql

import (
	"encoding/json"
	"testing"
)

func TestRegistryIncludesCoreSliceOperations(t *testing.T) {
	t.Parallel()

	cases := map[OperationKey]string{
		OperationInventory:       "Inventory",
		OperationCampaigns:       "ViewerDropsDashboard",
		OperationRewardCampaigns: "ViewerDropsDashboard",
		OperationCampaignDetails: "DropCampaignDetails",
		OperationCurrentDrop:     "DropCurrentSessionContext",
		OperationGetStreamInfo:   "VideoPlayerStreamInfoOverlayChannel",
		OperationAvailableDrops:  "DropsHighlightService_AvailableDrops",
		OperationClaimDrop:       "DropsPage_ClaimDropRewards",
	}

	for key, expectedName := range cases {
		operation, ok := Lookup(key)
		if !ok {
			t.Fatalf("缺少核心操作模板: %s", key)
		}
		if operation.OperationName != expectedName {
			t.Fatalf("%s 的 operationName 不匹配: %q", key, operation.OperationName)
		}
		if operation.Extensions.PersistedQuery.SHA256Hash == "" {
			t.Fatalf("%s 的 persisted query hash 不能为空", key)
		}
	}
}

func TestRewardCampaignsOperationUsesCurrentDashboardPersistedQuery(t *testing.T) {
	t.Parallel()

	operation := MustLookup(OperationRewardCampaigns)
	if operation.OperationName != "ViewerDropsDashboard" {
		t.Fatalf("operationName 不匹配: %q", operation.OperationName)
	}
	if got := operation.Extensions.PersistedQuery.SHA256Hash; got != "d9cae7761dafab85908c85e6683cb4201b449e66ac3bb5e894f15ff12aeafaa7" {
		t.Fatalf("RewardCampaigns hash 不匹配: %q", got)
	}
	if got, ok := operation.Variables["fetchRewardCampaigns"].(bool); !ok || !got {
		t.Fatalf("RewardCampaigns 必须开启 fetchRewardCampaigns: %#v", operation.Variables)
	}

	campaigns := MustLookup(OperationCampaigns)
	if got := campaigns.Extensions.PersistedQuery.SHA256Hash; got == operation.Extensions.PersistedQuery.SHA256Hash {
		t.Fatal("RewardCampaigns 不能替换普通 Campaigns hash，否则普通 drops 可能丢失")
	}
	if got, ok := campaigns.Variables["fetchRewardCampaigns"].(bool); !ok || got {
		t.Fatalf("普通 Campaigns 必须继续关闭 fetchRewardCampaigns: %#v", campaigns.Variables)
	}
}

func TestLookupReturnsIndependentCopy(t *testing.T) {
	t.Parallel()

	first := MustLookup(OperationInventory)
	first.Variables["fetchRewardCampaigns"] = true

	second := MustLookup(OperationInventory)
	if value, ok := second.Variables["fetchRewardCampaigns"].(bool); !ok || value {
		t.Fatalf("注册表模板被意外修改: %#v", second.Variables)
	}
}

func TestOperationWithVariablesMergesNestedMaps(t *testing.T) {
	t.Parallel()

	operation := MustLookup(OperationClaimDrop).MustWithVariables(map[string]any{
		"input": map[string]any{
			"dropInstanceID": "claim-1",
		},
	})

	input, ok := operation.Variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("input 变量类型不匹配: %T", operation.Variables["input"])
	}
	if input["dropInstanceID"] != "claim-1" {
		t.Fatalf("变量未被覆盖: %#v", input)
	}
}

func TestOperationWithVariablesRejectsMissingRequiredValues(t *testing.T) {
	t.Parallel()

	operation := MustLookup(OperationCampaignDetails)
	if _, err := operation.WithVariables(map[string]any{
		"dropID": "campaign-1",
	}); err == nil {
		t.Fatal("期望缺少必填变量时返回错误")
	}
}

func TestOperationMarshalFailsWhenRequiredValueUnresolved(t *testing.T) {
	t.Parallel()

	operation := MustLookup(OperationGetStreamInfo)
	if _, err := json.Marshal(operation); err == nil {
		t.Fatal("期望未赋值模板不能直接序列化")
	}
}
