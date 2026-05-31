package domain

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

type CampaignSpec struct {
	ID               string
	Name             string
	Game             Game
	Linked           bool
	LinkURL          string
	ImageURL         string
	StartsAt         time.Time
	EndsAt           time.Time
	Status           string
	IsRewardCampaign bool
	AllowedChannels  []Channel
	Drops            []TimedDropSpec
}

type TimedDropSpec struct {
	ID                  string
	Name                string
	Benefits            []Benefit
	StartsAt            time.Time
	EndsAt              time.Time
	ClaimID             string
	IsClaimed           bool
	PreconditionDropIDs []string
	RealCurrentMinutes  int
	RequiredMinutes     int
	ExtraCurrentMinutes int
}

type DropsCampaign struct {
	ID               string
	Name             string
	Game             Game
	Linked           bool
	LinkURL          string
	ImageURL         string
	StartsAt         time.Time
	EndsAt           time.Time
	Valid            bool
	IsRewardCampaign bool
	AllowedChannels  []Channel
	TimedDrops       map[string]*TimedDrop
}

func NewCampaign(spec CampaignSpec) (*DropsCampaign, error) {
	campaign := &DropsCampaign{
		ID:               spec.ID,
		Name:             spec.Name,
		Game:             spec.Game,
		Linked:           spec.Linked,
		LinkURL:          spec.LinkURL,
		ImageURL:         spec.ImageURL,
		StartsAt:         spec.StartsAt.UTC(),
		EndsAt:           spec.EndsAt.UTC(),
		Valid:            !strings.EqualFold(strings.TrimSpace(spec.Status), "EXPIRED"),
		IsRewardCampaign: spec.IsRewardCampaign || strings.HasPrefix(spec.ID, "reward:"),
		AllowedChannels:  slices.Clone(spec.AllowedChannels),
		TimedDrops:       make(map[string]*TimedDrop, len(spec.Drops)),
	}

	for _, dropSpec := range spec.Drops {
		if dropSpec.ID == "" {
			return nil, fmt.Errorf("drop id 不能为空")
		}
		if _, exists := campaign.TimedDrops[dropSpec.ID]; exists {
			return nil, fmt.Errorf("drop id %q 重复", dropSpec.ID)
		}

		drop := &TimedDrop{
			BaseDrop: BaseDrop{
				ID:                  dropSpec.ID,
				Name:                dropSpec.Name,
				Campaign:            campaign,
				Benefits:            slices.Clone(dropSpec.Benefits),
				StartsAt:            dropSpec.StartsAt.UTC(),
				EndsAt:              dropSpec.EndsAt.UTC(),
				ClaimID:             dropSpec.ClaimID,
				IsClaimed:           dropSpec.IsClaimed,
				PreconditionDropIDs: slices.Clone(dropSpec.PreconditionDropIDs),
			},
			RealCurrentMinutes:  dropSpec.RealCurrentMinutes,
			RequiredMinutes:     dropSpec.RequiredMinutes,
			ExtraCurrentMinutes: dropSpec.ExtraCurrentMinutes,
		}
		if drop.IsClaimed && drop.RealCurrentMinutes < drop.RequiredMinutes {
			drop.RealCurrentMinutes = drop.RequiredMinutes
		}
		if drop.IsClaimed {
			drop.ExtraCurrentMinutes = 0
		}
		campaign.TimedDrops[drop.ID] = drop
	}

	if err := campaign.validatePreconditions(); err != nil {
		return nil, err
	}
	return campaign, nil
}

func (c *DropsCampaign) validatePreconditions() error {
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)

	state := make(map[string]int, len(c.TimedDrops))
	var visit func(string) error
	visit = func(dropID string) error {
		switch state[dropID] {
		case visiting:
			return fmt.Errorf("drop precondition 存在循环: %s", dropID)
		case visited:
			return nil
		}

		drop := c.TimedDrops[dropID]
		if drop == nil {
			return fmt.Errorf("drop precondition %q 不存在", dropID)
		}

		state[dropID] = visiting
		for _, preconditionID := range drop.PreconditionDropIDs {
			if _, ok := c.TimedDrops[preconditionID]; !ok {
				return fmt.Errorf("drop %q 的 precondition %q 不存在", dropID, preconditionID)
			}
			if err := visit(preconditionID); err != nil {
				return err
			}
		}
		state[dropID] = visited
		return nil
	}

	for dropID := range c.TimedDrops {
		if state[dropID] != unvisited {
			continue
		}
		if err := visit(dropID); err != nil {
			return err
		}
	}
	return nil
}

func (c *DropsCampaign) Drop(dropID string) *TimedDrop {
	if c == nil {
		return nil
	}
	return c.TimedDrops[dropID]
}

func (c *DropsCampaign) Drops() []*TimedDrop {
	if c == nil || len(c.TimedDrops) == 0 {
		return nil
	}

	drops := make([]*TimedDrop, 0, len(c.TimedDrops))
	for _, drop := range c.TimedDrops {
		drops = append(drops, drop)
	}
	return drops
}

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

func (c *DropsCampaign) CanEarn(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) bool {
	if c == nil || !c.baseCanEarn(now, channel, enableBadgesEmotes, ignoreChannelStatus) {
		return false
	}

	for _, drop := range c.Drops() {
		if drop.baseCanEarn(now) {
			return true
		}
	}
	return false
}

func (c *DropsCampaign) CanRecordRewardCompletion(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) bool {
	if c == nil || !c.IsRewardCampaign || !c.baseCanEarn(now, channel, enableBadgesEmotes, ignoreChannelStatus) {
		return false
	}

	for _, drop := range c.Drops() {
		if drop != nil && !drop.IsClaimed && drop.CurrentMinutes() >= drop.RequiredMinutes {
			return true
		}
	}
	return false
}

func (c *DropsCampaign) UpdateMinutes(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool, newMinutes int) bool {
	if c == nil {
		return false
	}

	updated := false
	for _, drop := range c.Drops() {
		if !drop.CanEarn(now, channel, enableBadgesEmotes, ignoreChannelStatus) {
			continue
		}
		if drop.UpdateMinutes(newMinutes) {
			updated = true
		}
	}
	return updated
}

func (c *DropsCampaign) BumpMinutes(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) bool {
	if c == nil {
		return false
	}

	reachedLimit := false
	for _, drop := range c.Drops() {
		if drop.BumpMinutes(now, channel, enableBadgesEmotes, ignoreChannelStatus) {
			reachedLimit = true
		}
	}
	return reachedLimit
}

func (c *DropsCampaign) BumpRewardMinutes(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) bool {
	if c == nil || !c.IsRewardCampaign {
		return false
	}

	completed := false
	for _, drop := range c.Drops() {
		if drop.BumpMinutesUntilRequired(now, channel, enableBadgesEmotes, ignoreChannelStatus) {
			completed = true
		}
	}
	return completed
}

func (c *DropsCampaign) CanEarnWithin(now time.Time, stamp time.Time, enableBadgesEmotes bool) bool {
	if c == nil ||
		!c.Eligible(enableBadgesEmotes) ||
		!c.Valid ||
		!c.EndsAt.After(now) ||
		!c.StartsAt.Before(stamp) {
		return false
	}

	for _, drop := range c.Drops() {
		if drop.canEarnWithin(stamp, now) {
			return true
		}
	}
	return false
}

func (c *DropsCampaign) baseCanEarn(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) bool {
	if c == nil || !c.Eligible(enableBadgesEmotes) || !c.ActiveAt(now) {
		return false
	}
	if channel == nil {
		return true
	}
	if len(c.AllowedChannels) > 0 && !c.allowsChannel(channel.ID) {
		return false
	}
	if ignoreChannelStatus {
		return true
	}

	channelGame := channel.CurrentGame()
	return (channelGame != nil && channelGame.ID == c.Game.ID) || c.Game.IsSpecialEvents()
}

func (c *DropsCampaign) allowsChannel(channelID int64) bool {
	for _, channel := range c.AllowedChannels {
		if channel.ID == channelID {
			return true
		}
	}
	return false
}
