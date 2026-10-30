package inventory

import (
	"context"
	"fmt"
	"strings"

	"twitchdropsminergo/internal/gql"
)

const catalogAccountChannelBatchSize = 10

func resolveCatalogChannelAccounts(ctx context.Context, client rawGQLClient, channelIDs []string, pending map[string]map[string]any) error {
	if len(channelIDs) == 1 {
		response, err := client.DoRaw(ctx, gql.RawQuery{Query: catalogAccountChannelQuery, Variables: map[string]any{"channelID": channelIDs[0]}})
		if err != nil {
			return err
		}
		applyCatalogAccounts(pending, optionalMap(optionalMap(response.Data)["channel"])["viewerDropCampaigns"])
		return nil
	}
	declarations := make([]string, 0, len(channelIDs))
	fields := make([]string, 0, len(channelIDs))
	variables := make(map[string]any, len(channelIDs))
	for index, id := range channelIDs {
		key := fmt.Sprintf("channel%d", index)
		declarations = append(declarations, "$"+key+": ID!")
		fields = append(fields, key+": channel(id: $"+key+") { viewerDropCampaigns { "+catalogAccountFields+" } }")
		variables[key] = id
	}
	query := "query CatalogChannelAccounts(" + strings.Join(declarations, ", ") + ") { " + strings.Join(fields, " ") + " }"
	response, err := client.DoRaw(ctx, gql.RawQuery{Query: query, Variables: variables})
	if err != nil {
		return err
	}
	data := optionalMap(response.Data)
	for index := range channelIDs {
		channel := optionalMap(data[fmt.Sprintf("channel%d", index)])
		applyCatalogAccounts(pending, channel["viewerDropCampaigns"])
	}
	return nil
}
