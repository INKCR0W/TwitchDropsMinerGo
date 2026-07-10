package domain

import (
	"fmt"
	"slices"
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

func (c *DropsCampaign) ObserveMinutes(source *TimedDrop, minutes int) bool {
	if c == nil || source == nil {
		return false
	}

	updated := source.observeMinutes(minutes)
	for _, drop := range c.Drops() {
		if drop != source && drop.sharesCumulativeCounter(source) && drop.observeMinutes(minutes) {
			updated = true
		}
	}
	return updated
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
