package domain

import (
	"slices"
	"strings"
)

type Stream struct {
	BroadcastID  int64
	Game         *Game
	Viewers      int
	Title        string
	DropsEnabled bool
	// Twitch 报告的该频道当前可推进的活动; nil 表示尚未查到
	OfferedCampaignIDs []string
}

type Channel struct {
	ID            int64
	Login         string
	DisplayName   string
	Stream        *Stream
	ACLBased      bool
	PendingStream bool
}

func (c *Channel) Name() string {
	if c == nil {
		return ""
	}
	if displayName := strings.TrimSpace(c.DisplayName); displayName != "" {
		return displayName
	}
	return c.Login
}

func (c *Channel) Online() bool {
	return c != nil && c.Stream != nil
}

func (c *Channel) Offline() bool {
	return c != nil && c.Stream == nil && !c.PendingStream
}

func (c *Channel) PendingOnline() bool {
	return c != nil && c.Stream == nil && c.PendingStream
}

func (c *Channel) OffersCampaign(campaignID string) bool {
	if c == nil || c.Stream == nil {
		return false
	}
	if c.Stream.OfferedCampaignIDs == nil {
		return true
	}
	return slices.Contains(c.Stream.OfferedCampaignIDs, campaignID)
}

func (c *Channel) CurrentGame() *Game {
	if c == nil || c.Stream == nil {
		return nil
	}
	return c.Stream.Game
}

func (c *Channel) ViewerCount() int {
	if c == nil || c.Stream == nil {
		return 0
	}
	return c.Stream.Viewers
}
