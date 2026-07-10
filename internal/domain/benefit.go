package domain

import (
	"strings"
)

type BenefitType string

const (
	BenefitTypeUnknown           BenefitType = "UNKNOWN"
	BenefitTypeBadge             BenefitType = "BADGE"
	BenefitTypeEmote             BenefitType = "EMOTE"
	BenefitTypeDirectEntitlement BenefitType = "DIRECT_ENTITLEMENT"
)

type Benefit struct {
	ID       string
	Name     string
	Type     BenefitType
	ImageURL string
}

func ParseBenefitType(value string) BenefitType {
	switch BenefitType(strings.TrimSpace(value)) {
	case BenefitTypeBadge:
		return BenefitTypeBadge
	case BenefitTypeEmote:
		return BenefitTypeEmote
	case BenefitTypeDirectEntitlement:
		return BenefitTypeDirectEntitlement
	default:
		return BenefitTypeUnknown
	}
}

func (t BenefitType) IsBadgeOrEmote() bool {
	return t == BenefitTypeBadge || t == BenefitTypeEmote
}
