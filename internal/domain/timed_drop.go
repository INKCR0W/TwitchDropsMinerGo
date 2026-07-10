package domain

import (
	"math"
	"time"
)

type TimedDrop struct {
	BaseDrop
	RealCurrentMinutes  int
	RequiredMinutes     int
	ExtraCurrentMinutes int
}

func (d *TimedDrop) CurrentMinutes() int {
	if d == nil {
		return 0
	}
	return d.RealCurrentMinutes + d.ExtraCurrentMinutes
}

func (d *TimedDrop) RemainingMinutes() int {
	if d == nil {
		return 0
	}
	return d.RequiredMinutes - d.CurrentMinutes()
}

func (d *TimedDrop) TotalRequiredMinutes() int {
	if d == nil {
		return 0
	}

	total := d.RequiredMinutes
	maxPrecondition := 0
	if d.Campaign != nil {
		for _, dropID := range d.PreconditionDropIDs {
			precondition := d.Campaign.Drop(dropID)
			if precondition == nil {
				continue
			}
			if required := precondition.TotalRequiredMinutes(); required > maxPrecondition {
				maxPrecondition = required
			}
		}
	}
	return total + maxPrecondition
}

func (d *TimedDrop) TotalRemainingMinutes() int {
	if d == nil {
		return 0
	}

	total := d.RemainingMinutes()
	maxPrecondition := 0
	if d.Campaign != nil {
		for _, dropID := range d.PreconditionDropIDs {
			precondition := d.Campaign.Drop(dropID)
			if precondition == nil {
				continue
			}
			if remaining := precondition.TotalRemainingMinutes(); remaining > maxPrecondition {
				maxPrecondition = remaining
			}
		}
	}
	return total + maxPrecondition
}

func (d *TimedDrop) Progress() float64 {
	if d == nil || d.CurrentMinutes() <= 0 || d.RequiredMinutes <= 0 {
		return 0
	}
	if d.CurrentMinutes() >= d.RequiredMinutes {
		return 1
	}
	return float64(d.CurrentMinutes()) / float64(d.RequiredMinutes)
}

func (d *TimedDrop) Availability(now time.Time) float64 {
	if d == nil {
		return math.Inf(1)
	}
	if d.RequiredMinutes <= 0 || d.TotalRemainingMinutes() <= 0 || !now.Before(d.EndsAt) {
		return math.Inf(1)
	}
	return d.EndsAt.Sub(now).Minutes() / float64(d.TotalRemainingMinutes())
}

// SpareMinutes 返回该 drop 的绝对富余时间(分钟): 距结束的时间减去还需观看的分钟数。
// 值越小越紧迫; 已领取/已结束/无需观看的 drop 返回 +Inf(不构成约束)。
func (d *TimedDrop) SpareMinutes(now time.Time) float64 {
	if d == nil {
		return math.Inf(1)
	}
	if d.RequiredMinutes <= 0 || d.TotalRemainingMinutes() <= 0 || !now.Before(d.EndsAt) {
		return math.Inf(1)
	}
	return d.EndsAt.Sub(now).Minutes() - float64(d.TotalRemainingMinutes())
}

func (d *TimedDrop) CanEarn(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) bool {
	return d != nil &&
		d.baseCanEarn(now) &&
		d.Campaign != nil &&
		d.Campaign.baseCanEarn(now, channel, enableBadgesEmotes, ignoreChannelStatus)
}

func (d *TimedDrop) baseCanEarn(now time.Time) bool {
	return d.baseEarnConditions() &&
		!now.Before(d.StartsAt) &&
		now.Before(d.EndsAt)
}

func (d *TimedDrop) canEarnWithin(stamp time.Time, now time.Time) bool {
	return d.baseEarnConditions() &&
		d.EndsAt.After(now) &&
		d.StartsAt.Before(stamp)
}

func (d *TimedDrop) baseEarnConditions() bool {
	return d != nil &&
		d.BaseDrop.baseEarnConditions() &&
		d.RequiredMinutes > 0 &&
		d.CurrentMinutes() < d.RequiredMinutes &&
		d.ExtraCurrentMinutes < MaxExtraMinutes
}

// Twitch 对同一活动同一时间窗的 drop 只维护一个累计观看计数, 各 drop 只是阈值不同
func (d *TimedDrop) sharesCumulativeCounter(other *TimedDrop) bool {
	return d != nil && other != nil &&
		d.RequiredMinutes > 0 && other.RequiredMinutes > 0 &&
		len(d.PreconditionDropIDs) == 0 && len(other.PreconditionDropIDs) == 0 &&
		d.StartsAt.Equal(other.StartsAt) &&
		d.EndsAt.Equal(other.EndsAt)
}

// 换台后 dropCurrentSession 可能回报更小的分钟数, 只增不减才不会抹掉整个活动的进度
func (d *TimedDrop) observeMinutes(minutes int) bool {
	if d == nil || d.IsClaimed {
		return false
	}
	if minutes > d.RequiredMinutes {
		minutes = d.RequiredMinutes
	}
	if minutes <= d.RealCurrentMinutes && d.ExtraCurrentMinutes == 0 {
		return false
	}

	if minutes > d.RealCurrentMinutes {
		d.RealCurrentMinutes = minutes
	}
	d.ExtraCurrentMinutes = 0
	return true
}

func (d *TimedDrop) MarkClaimed() bool {
	if d == nil {
		return false
	}
	if d.IsClaimed && d.RealCurrentMinutes == d.RequiredMinutes && d.ExtraCurrentMinutes == 0 {
		return false
	}

	d.IsClaimed = true
	d.RealCurrentMinutes = d.RequiredMinutes
	d.ExtraCurrentMinutes = 0
	return true
}

func (d *TimedDrop) BumpMinutes(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) bool {
	if d == nil || !d.CanEarn(now, channel, enableBadgesEmotes, ignoreChannelStatus) {
		return false
	}

	d.ExtraCurrentMinutes++
	return d.ExtraCurrentMinutes >= MaxExtraMinutes
}

func (d *TimedDrop) BumpMinutesUntilRequired(now time.Time, channel *Channel, enableBadgesEmotes bool, ignoreChannelStatus bool) bool {
	if d == nil || d.IsClaimed {
		return false
	}
	if d.CurrentMinutes() >= d.RequiredMinutes {
		return d.Campaign != nil && d.Campaign.baseCanEarn(now, channel, enableBadgesEmotes, ignoreChannelStatus)
	}
	if !d.CanEarn(now, channel, enableBadgesEmotes, ignoreChannelStatus) {
		return false
	}

	d.ExtraCurrentMinutes++
	return d.CurrentMinutes() >= d.RequiredMinutes
}
