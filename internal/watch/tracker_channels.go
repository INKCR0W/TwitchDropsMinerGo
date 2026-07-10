package watch

import (
	"reflect"

	"twitchdropsminergo/internal/domain"
)

func (t *Tracker) AddChannel(channel domain.Channel) {
	if t == nil || channel.ID <= 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cloned := cloneChannel(&channel)
	if existing, ok := t.channels[channel.ID]; ok && existing != nil {
		if cloned.DisplayName == "" {
			cloned.DisplayName = existing.channel.DisplayName
		}
		if cloned.Stream == nil {
			cloned.Stream = cloneStream(existing.channel.Stream)
		}
		if !cloned.PendingStream {
			cloned.PendingStream = existing.channel.PendingStream
		}
		existing.epoch = t.bumpEpochLocked()
		existing.channel = &cloned
		return
	}

	t.channels[channel.ID] = &trackedChannel{
		channel: &cloned,
		epoch:   t.bumpEpochLocked(),
	}
}

func (t *Tracker) RemoveChannel(channelID int64) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil {
		return
	}
	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
	}
	delete(t.channels, channelID)
}

func (t *Tracker) Channel(channelID int64) (domain.Channel, bool) {
	if t == nil {
		return domain.Channel{}, false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return domain.Channel{}, false
	}

	return cloneChannel(tracked.channel), true
}

func (t *Tracker) applyFetched(channelID int64, expectedEpoch uint64, fetched fetchedChannel) {
	var (
		before  domain.Channel
		after   domain.Channel
		handler func(before, after domain.Channel)
		changed bool
	)

	t.mu.Lock()
	defer func() {
		t.mu.Unlock()
		if changed && handler != nil {
			handler(before, after)
		}
	}()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return
	}
	if tracked.epoch != expectedEpoch {
		return
	}
	before = cloneChannel(tracked.channel)

	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
		tracked.pendingCancel = nil
	}

	tracked.channel.PendingStream = false
	if fetched.DisplayName != "" {
		tracked.channel.DisplayName = fetched.DisplayName
	}
	tracked.channel.Stream = cloneStream(fetched.Stream)
	after = cloneChannel(tracked.channel)
	handler = t.onChange
	changed = !reflect.DeepEqual(before, after)
}

func (t *Tracker) setOffline(channelID int64) {
	var (
		before  domain.Channel
		after   domain.Channel
		handler func(before, after domain.Channel)
		changed bool
	)

	t.mu.Lock()
	defer func() {
		t.mu.Unlock()
		if changed && handler != nil {
			handler(before, after)
		}
	}()

	tracked, ok := t.channels[channelID]
	if !ok || tracked == nil || tracked.channel == nil {
		return
	}
	before = cloneChannel(tracked.channel)
	tracked.epoch = t.bumpEpochLocked()
	if tracked.pendingCancel != nil {
		tracked.pendingCancel()
		tracked.pendingCancel = nil
	}
	tracked.channel.PendingStream = false
	tracked.channel.Stream = nil
	after = cloneChannel(tracked.channel)
	handler = t.onChange
	changed = !reflect.DeepEqual(before, after)
}
