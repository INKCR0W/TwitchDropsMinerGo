package inventory

import "fmt"

func normalizeWatchCampaign(value any) (map[string]any, error) {
	raw, err := asMap(value, "campaign")
	if err != nil {
		return nil, err
	}
	campaign := cloneMap(raw)
	if stringValue(campaign, "id") == "" {
		return nil, fmt.Errorf("目录活动缺少 id")
	}
	drops, err := sliceFromMap(campaign, "timeBasedDrops")
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]struct{})
	for _, value := range drops {
		drop := optionalMap(value)
		if int64Value(drop, "requiredSubs") > 0 && !boolValue(optionalMap(drop["self"]), "isClaimed") {
			excluded[stringValue(drop, "id")] = struct{}{}
		}
	}
	// 需要先完成订阅奖励的后续掉宝也不能靠观看解锁
	for changed := true; changed; {
		changed = false
		for _, value := range drops {
			drop := optionalMap(value)
			id := stringValue(drop, "id")
			if _, skip := excluded[id]; skip {
				continue
			}
			for _, prerequisite := range parsePreconditionDropIDs(drop["preconditionDrops"]) {
				if _, blocked := excluded[prerequisite]; blocked {
					excluded[id] = struct{}{}
					changed = true
					break
				}
			}
		}
	}
	watchDrops := make([]any, 0, len(drops))
	for _, value := range drops {
		drop, err := asMap(value, "timeBasedDrops")
		if err != nil {
			return nil, err
		}
		if _, skip := excluded[stringValue(drop, "id")]; skip {
			continue
		}
		benefits, err := sliceFromMap(drop, "benefitEdges")
		if err != nil {
			return nil, err
		}
		if len(benefits) == 0 {
			return nil, fmt.Errorf("目录活动 %s 的掉宝缺少奖励", stringValue(campaign, "id"))
		}
		for _, value := range benefits {
			benefit := optionalMap(optionalMap(value)["benefit"])
			if stringValue(benefit, "distributionType") == "" {
				return nil, fmt.Errorf("目录活动 %s 的奖励类型不完整", stringValue(campaign, "id"))
			}
		}
		watchDrops = append(watchDrops, drop)
	}
	if len(watchDrops) == 0 {
		return nil, nil
	}
	campaign["timeBasedDrops"] = watchDrops
	return campaign, nil
}
