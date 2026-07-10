package domain

import (
	"fmt"
	"strings"
	"time"
)

const MaxExtraMinutes = 15

type BaseDrop struct {
	ID                  string
	Name                string
	Campaign            *DropsCampaign
	Benefits            []Benefit
	StartsAt            time.Time
	EndsAt              time.Time
	ClaimID             string
	IsClaimed           bool
	PreconditionDropIDs []string
}

func (d *BaseDrop) PreconditionsMet() bool {
	if d == nil {
		return false
	}
	if len(d.PreconditionDropIDs) == 0 {
		return true
	}
	if d.Campaign == nil {
		return false
	}

	for _, dropID := range d.PreconditionDropIDs {
		precondition := d.Campaign.Drop(dropID)
		if precondition == nil || !precondition.IsClaimed {
			return false
		}
	}
	return true
}

func (d *BaseDrop) CanClaim(now time.Time) bool {
	return d != nil &&
		d.Campaign != nil &&
		d.ClaimID != "" &&
		!d.IsClaimed &&
		now.Before(d.Campaign.EndsAt.Add(24*time.Hour))
}

func (d *BaseDrop) RewardsText(delimiter string) string {
	if d == nil {
		return ""
	}
	if delimiter == "" {
		delimiter = ", "
	}

	names := make([]string, 0, len(d.Benefits))
	for _, benefit := range d.Benefits {
		names = append(names, benefit.Name)
	}
	return strings.Join(names, delimiter)
}

func (d *BaseDrop) UpdateClaim(claimID string) {
	if d == nil {
		return
	}
	d.ClaimID = strings.TrimSpace(claimID)
}

func (d *BaseDrop) GenerateClaimID(userID int64) string {
	if d == nil || userID <= 0 || d.Campaign == nil || d.ID == "" {
		return ""
	}
	return fmt.Sprintf("%d#%s#%s", userID, d.Campaign.ID, d.ID)
}

func (d *BaseDrop) baseEarnConditions() bool {
	if d == nil {
		return false
	}

	return d.PreconditionsMet() &&
		!d.IsClaimed &&
		(len(d.Benefits) > 0 || d.inPreconditionsChain())
}

func (d *BaseDrop) inPreconditionsChain() bool {
	if d == nil || d.Campaign == nil {
		return false
	}
	for _, dropID := range d.Campaign.PreconditionsChain() {
		if dropID == d.ID {
			return true
		}
	}
	return false
}
