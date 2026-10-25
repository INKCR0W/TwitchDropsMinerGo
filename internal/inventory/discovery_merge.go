package inventory

func mergeCampaignPayloads(primary, secondary map[string]any) (map[string]any, error) {
	merged, err := mergeMaps(primary, secondary)
	if err != nil {
		return nil, err
	}
	for id, value := range primary {
		known := optionalMap(value)
		found := optionalMap(secondary[id])
		if found == nil {
			continue
		}
		knownDrops, err := sliceFromMap(known, "timeBasedDrops")
		if err != nil {
			return nil, err
		}
		foundDrops, err := sliceFromMap(found, "timeBasedDrops")
		if err != nil {
			return nil, err
		}
		byID := make(map[string]any)
		order := make([]string, 0, len(knownDrops)+len(foundDrops))
		for _, value := range append(knownDrops, foundDrops...) {
			drop, err := asMap(value, "campaign.timeBasedDrops")
			if err != nil {
				return nil, err
			}
			dropID := stringValue(drop, "id")
			if primary, exists := byID[dropID]; exists {
				byID[dropID], err = mergeMaps(optionalMap(primary), drop)
				if err != nil {
					return nil, err
				}
			} else {
				byID[dropID] = cloneMap(drop)
				order = append(order, dropID)
			}
		}
		drops := make([]any, 0, len(order))
		for _, id := range order {
			drops = append(drops, byID[id])
		}
		optionalMap(merged[id])["timeBasedDrops"] = drops
	}
	return merged, nil
}
