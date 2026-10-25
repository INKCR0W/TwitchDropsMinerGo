package inventory

import (
	"context"
	"fmt"
	"strings"

	"twitchdropsminergo/internal/gql"
)

const discoveryBenefitFields = `{ viewerDropCampaigns { timeBasedDrops { benefitEdges { benefit { id distributionType } } } } }`

func (r *Refresher) completeDiscoveryBenefitTypes(ctx context.Context, channels []discoveryChannel, responses []gql.Response) {
	var declarations, selections []string
	variables := make(map[string]any)
	for index, response := range responses {
		if !needsDiscoveryBenefitTypes(response) {
			continue
		}
		key := fmt.Sprintf("channel%d", index)
		declarations = append(declarations, "$"+key+": ID!")
		selections = append(selections, key+": channel(id: $"+key+") "+discoveryBenefitFields)
		variables[key] = channels[index].id
	}
	if len(variables) == 0 {
		return
	}
	client, ok := r.gqlClient.(rawGQLClient)
	if !ok {
		r.logger.Warn("GQL 客户端不支持补查奖励类型，将跳过类型不明的活动")
		return
	}
	response, err := client.DoRaw(ctx, gql.RawQuery{
		Query:     "query DiscoveryBenefitTypes(" + strings.Join(declarations, ", ") + ") { " + strings.Join(selections, " ") + " }",
		Variables: variables,
	})
	if err != nil {
		r.logger.Warn("补查奖励类型失败，将跳过类型不明的活动", "error", err)
		return
	}
	types := make(map[string]string)
	for _, channel := range optionalMap(response.Data) {
		visitDiscoveryBenefits(optionalMap(channel), func(benefit map[string]any) {
			if id, kind := stringValue(benefit, "id"), stringValue(benefit, "distributionType"); id != "" && kind != "" {
				types[id] = kind
			}
		})
	}
	for index := range responses {
		data := cloneMap(optionalMap(responses[index].Data))
		visitDiscoveryBenefits(optionalMap(data["channel"]), func(benefit map[string]any) {
			if kind := types[stringValue(benefit, "id")]; kind != "" {
				benefit["distributionType"] = kind
			}
		})
		responses[index].Data = data
	}
}

func needsDiscoveryBenefitTypes(response gql.Response) bool {
	channel := optionalMap(optionalMap(response.Data)["channel"])
	campaigns, _ := channel["viewerDropCampaigns"].([]any)
	for _, value := range campaigns {
		drops, _ := optionalMap(value)["timeBasedDrops"].([]any)
		for _, value := range drops {
			drop := optionalMap(value)
			if int64Value(drop, "requiredSubs") == 0 && validateDiscoveryBenefits(drop) != nil {
				return true
			}
		}
	}
	return false
}

func visitDiscoveryBenefits(channel map[string]any, visit func(map[string]any)) {
	campaigns, _ := channel["viewerDropCampaigns"].([]any)
	for _, value := range campaigns {
		drops, _ := optionalMap(value)["timeBasedDrops"].([]any)
		for _, value := range drops {
			edges, _ := optionalMap(value)["benefitEdges"].([]any)
			for _, value := range edges {
				if benefit := optionalMap(optionalMap(value)["benefit"]); benefit != nil {
					visit(benefit)
				}
			}
		}
	}
}

func validateDiscoveryBenefits(drop map[string]any) error {
	edges, err := sliceFromMap(drop, "benefitEdges")
	if err != nil {
		return err
	}
	if len(edges) == 0 {
		return fmt.Errorf("drop %s 缺少奖励信息", stringValue(drop, "id"))
	}
	for _, value := range edges {
		benefit := optionalMap(optionalMap(value)["benefit"])
		kind := stringValue(benefit, "distributionType")
		if kind == "" {
			return fmt.Errorf("benefit %s 缺少 distributionType", stringValue(benefit, "id"))
		}
	}
	return nil
}
