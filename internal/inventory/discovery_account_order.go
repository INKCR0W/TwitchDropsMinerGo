package inventory

import "sort"

func orderedAccountCampaigns(pending map[string]map[string]any, priority []string) []string {
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left := accountPriority(pending[ids[i]], priority)
		right := accountPriority(pending[ids[j]], priority)
		if left != right {
			return left < right
		}
		return ids[i] < ids[j]
	})
	return ids
}

func accountPriority(campaign map[string]any, priority []string) int {
	name := parseGame(optionalMap(campaign["game"])).Name
	for index, item := range priority {
		if accountGameListed(name, []string{item}) {
			return index
		}
	}
	return len(priority)
}
