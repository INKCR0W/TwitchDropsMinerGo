package domain

import (
	"math"
	"sort"
	"time"
)

func (c *DropsCampaign) TimeTriggers() []time.Time {
	if c == nil {
		return nil
	}

	triggerMap := map[time.Time]struct{}{
		c.StartsAt: {},
		c.EndsAt:   {},
	}
	for _, drop := range c.Drops() {
		triggerMap[drop.StartsAt] = struct{}{}
		triggerMap[drop.EndsAt] = struct{}{}
	}

	triggers := make([]time.Time, 0, len(triggerMap))
	for trigger := range triggerMap {
		triggers = append(triggers, trigger)
	}
	sort.Slice(triggers, func(i int, j int) bool {
		return triggers[i].Before(triggers[j])
	})
	return triggers
}

func (c *DropsCampaign) ActiveAt(now time.Time) bool {
	return c != nil &&
		c.Valid &&
		!now.Before(c.StartsAt) &&
		now.Before(c.EndsAt)
}

func (c *DropsCampaign) UpcomingAt(now time.Time) bool {
	return c != nil &&
		c.Valid &&
		now.Before(c.StartsAt)
}

func (c *DropsCampaign) ExpiredAt(now time.Time) bool {
	return c == nil ||
		!c.Valid ||
		!now.Before(c.EndsAt)
}

func (c *DropsCampaign) TotalDrops() int {
	if c == nil {
		return 0
	}
	return len(c.TimedDrops)
}

func (c *DropsCampaign) Eligible(enableBadgesEmotes bool) bool {
	if c == nil {
		return false
	}
	if c.HasBadgeOrEmote() {
		return enableBadgesEmotes
	}
	return c.Linked
}

func (c *DropsCampaign) HasBadgeOrEmote() bool {
	if c == nil {
		return false
	}
	for _, drop := range c.Drops() {
		for _, benefit := range drop.Benefits {
			if benefit.Type.IsBadgeOrEmote() {
				return true
			}
		}
	}
	return false
}

func (c *DropsCampaign) Finished() bool {
	if c == nil {
		return false
	}
	if len(c.Drops()) == 0 {
		return false
	}
	for _, drop := range c.Drops() {
		if !drop.IsClaimed && drop.RequiredMinutes > 0 {
			return false
		}
	}
	return true
}

func (c *DropsCampaign) ClaimedDrops() int {
	if c == nil {
		return 0
	}

	total := 0
	for _, drop := range c.Drops() {
		if drop.IsClaimed {
			total++
		}
	}
	return total
}

func (c *DropsCampaign) RemainingDrops() int {
	if c == nil {
		return 0
	}

	total := 0
	for _, drop := range c.Drops() {
		if !drop.IsClaimed {
			total++
		}
	}
	return total
}

func (c *DropsCampaign) RequiredMinutes() int {
	if c == nil {
		return 0
	}

	maximum := 0
	for _, drop := range c.Drops() {
		if required := drop.TotalRequiredMinutes(); required > maximum {
			maximum = required
		}
	}
	return maximum
}

func (c *DropsCampaign) RemainingMinutes() int {
	if c == nil || c.TotalDrops() == 0 {
		return 0
	}

	drops := c.Drops()
	maximum := drops[0].TotalRemainingMinutes()
	for _, drop := range drops[1:] {
		if remaining := drop.TotalRemainingMinutes(); remaining > maximum {
			maximum = remaining
		}
	}
	return maximum
}

func (c *DropsCampaign) Progress() float64 {
	if c == nil || c.TotalDrops() == 0 {
		return 0
	}

	total := 0.0
	for _, drop := range c.Drops() {
		total += drop.Progress()
	}
	return total / float64(c.TotalDrops())
}

func (c *DropsCampaign) Availability(now time.Time) float64 {
	if c == nil || c.TotalDrops() == 0 {
		return math.Inf(1)
	}

	minimum := math.Inf(1)
	for _, drop := range c.Drops() {
		if availability := drop.Availability(now); availability < minimum {
			minimum = availability
		}
	}
	return minimum
}

// MinSpareMinutes 返回活动内最紧的 drop 的绝对富余时间(分钟)。用于 smart_balance
// 判断某 priority 活动是否还留有足够富余,可暂时让位给更紧急的非 priority 活动。
func (c *DropsCampaign) MinSpareMinutes(now time.Time) float64 {
	if c == nil || c.TotalDrops() == 0 {
		return math.Inf(1)
	}

	minimum := math.Inf(1)
	for _, drop := range c.Drops() {
		if spare := drop.SpareMinutes(now); spare < minimum {
			minimum = spare
		}
	}
	return minimum
}

func (c *DropsCampaign) FirstEarnableDrop(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) *TimedDrop {
	if c == nil {
		return nil
	}

	var selected *TimedDrop
	for _, drop := range c.Drops() {
		if !drop.CanEarn(now, channel, enableBadgesEmotes, ignoreChannelStatus) {
			continue
		}
		if selected == nil ||
			drop.RemainingMinutes() < selected.RemainingMinutes() ||
			(drop.RemainingMinutes() == selected.RemainingMinutes() && drop.ID < selected.ID) {
			selected = drop
		}
	}
	return selected
}

func (c *DropsCampaign) FirstDrop(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) *TimedDrop {
	return c.FirstEarnableDrop(now, channel, enableBadgesEmotes, ignoreChannelStatus)
}

func (c *DropsCampaign) PreconditionsChain() []string {
	if c == nil {
		return nil
	}

	chainSet := make(map[string]struct{})
	for _, drop := range c.Drops() {
		if drop.IsClaimed {
			continue
		}
		for _, dropID := range drop.PreconditionDropIDs {
			chainSet[dropID] = struct{}{}
		}
	}

	chain := make([]string, 0, len(chainSet))
	for dropID := range chainSet {
		chain = append(chain, dropID)
	}
	sort.Strings(chain)
	return chain
}
