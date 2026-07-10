package domain

import (
	"time"
)

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
		// 要求 RequiredMinutes > 0：缺少 minuteWatchedGoal 的奖励活动会解析出
		// RequiredMinutes==0，若不加此判断会因 0>=0 被判定为“无需观看即可完成”。
		if drop != nil && !drop.IsClaimed && drop.RequiredMinutes > 0 && drop.CurrentMinutes() >= drop.RequiredMinutes {
			return true
		}
	}
	return false
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
	return (channelGame != nil && channelGame.ID == c.Game.ID) || c.Game.IsSpecial()
}

func (c *DropsCampaign) allowsChannel(channelID int64) bool {
	for _, channel := range c.AllowedChannels {
		if channel.ID == channelID {
			return true
		}
	}
	return false
}
