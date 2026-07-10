package pubsub

import (
	"fmt"
	"sort"
)

func (m *Manager) AddTopics(topics ...Topic) error {
	if m == nil {
		return fmt.Errorf("PubSub 管理器未初始化")
	}

	filtered := uniqueTopics(topics)
	if len(filtered) == 0 {
		return nil
	}

	m.mu.Lock()
	currentTotal := 0
	existing := make(map[string]struct{})
	for _, shard := range m.shards {
		shard.mu.Lock()
		currentTotal += len(shard.topics)
		for key := range shard.topics {
			existing[key] = struct{}{}
		}
		shard.mu.Unlock()
	}

	pending := make([]Topic, 0, len(filtered))
	for _, topic := range filtered {
		if _, ok := existing[topic.Key()]; ok {
			continue
		}
		pending = append(pending, topic)
	}
	if len(pending) == 0 {
		m.mu.Unlock()
		return nil
	}

	capacity := (m.maxShards * m.shardTopicLimit) - currentTotal
	if len(pending) > capacity {
		m.mu.Unlock()
		return ErrTopicLimitExceeded
	}

	running := m.running
	runCtx := m.ctx
	newShards := make([]*shard, 0)
	for _, topic := range pending {
		var assigned bool
		for _, shard := range m.shards {
			if shard.addTopic(topic, m.shardTopicLimit) {
				assigned = true
				break
			}
		}
		if assigned {
			continue
		}

		shard := newShard(m, len(m.shards))
		if !shard.addTopic(topic, m.shardTopicLimit) {
			m.mu.Unlock()
			return fmt.Errorf("无法向新分片添加 topic %s", topic.Key())
		}

		m.shards = append(m.shards, shard)
		newShards = append(newShards, shard)
	}

	if running {
		for _, shard := range newShards {
			shard.start(runCtx)
		}
	}
	m.mu.Unlock()

	m.signalChanged()
	return nil
}

func (m *Manager) RemoveTopics(keys ...string) {
	if m == nil {
		return
	}

	keys = NormalizeTopicKeys(keys)
	if len(keys) == 0 {
		return
	}

	m.mu.Lock()
	for _, shard := range m.shards {
		shard.removeTopics(keys)
	}

	totalTopics := 0
	for _, shard := range m.shards {
		totalTopics += shard.topicCount()
	}

	requiredShards := 0
	if totalTopics > 0 {
		requiredShards = (totalTopics + m.shardTopicLimit - 1) / m.shardTopicLimit
	}

	removedShards := make([]*shard, 0)
	recycled := make([]Topic, 0)
	for len(m.shards) > requiredShards {
		last := m.shards[len(m.shards)-1]
		m.shards = m.shards[:len(m.shards)-1]
		recycled = append(recycled, last.clearAndDrainTopics()...)
		removedShards = append(removedShards, last)
	}

	if len(recycled) > 0 {
		sort.Slice(recycled, func(i, j int) bool {
			return recycled[i].Key() < recycled[j].Key()
		})
		for _, topic := range recycled {
			for _, shard := range m.shards {
				if shard.addTopic(topic, m.shardTopicLimit) {
					break
				}
			}
		}
	}
	m.mu.Unlock()

	for _, shard := range removedShards {
		shard.stop(true)
	}

	m.signalChanged()
}

func (m *Manager) trimEmptyShards() {
	m.mu.Lock()
	defer m.mu.Unlock()

	filtered := m.shards[:0]
	for _, shard := range m.shards {
		if shard.topicCount() == 0 {
			continue
		}
		shard.setIndex(len(filtered))
		filtered = append(filtered, shard)
	}
	m.shards = filtered
}
