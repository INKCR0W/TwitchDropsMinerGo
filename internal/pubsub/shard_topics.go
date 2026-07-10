package pubsub

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

func (s *shard) syncTopics(ctx context.Context, conn Connection) error {
	currentTopics, submittedTopics, pendingListen, pendingUnlisten := s.snapshotTopics()

	removedSet := make(map[string]struct{})
	for key := range submittedTopics {
		if _, ok := currentTopics[key]; !ok {
			if _, pending := pendingUnlisten[key]; pending {
				continue
			}
			removedSet[key] = struct{}{}
		}
	}
	for key := range pendingListen {
		if _, ok := currentTopics[key]; ok {
			continue
		}
		if _, pending := pendingUnlisten[key]; pending {
			continue
		}
		removedSet[key] = struct{}{}
	}

	removed := make([]string, 0, len(removedSet))
	for key := range removedSet {
		removed = append(removed, key)
	}
	sort.Strings(removed)

	added := make([]Topic, 0)
	for key, topic := range currentTopics {
		if _, ok := submittedTopics[key]; ok {
			continue
		}
		if _, ok := pendingListen[key]; ok {
			continue
		}
		if _, ok := pendingUnlisten[key]; ok {
			continue
		}
		added = append(added, topic)
	}
	sort.Slice(added, func(i, j int) bool {
		return added[i].Key() < added[j].Key()
	})

	if len(removed) == 0 && len(added) == 0 {
		return nil
	}

	if err := s.manager.auth.Validate(ctx); err != nil {
		return err
	}

	accessToken := strings.TrimSpace(s.manager.auth.Snapshot().AccessToken)
	if accessToken == "" {
		return fmt.Errorf("PubSub access token 为空")
	}

	for _, batch := range chunkStrings(removed, s.manager.listenBatchSize) {
		nonce, err := s.send(conn, outboundEnvelope{
			Type: "UNLISTEN",
			Data: &outboundData{
				Topics:    batch,
				AuthToken: accessToken,
			},
		})
		if err != nil {
			return err
		}
		s.markPending(nonce, "UNLISTEN", batch)
	}

	if len(added) == 0 {
		return nil
	}

	addedKeys := make([]string, 0, len(added))
	for _, topic := range added {
		addedKeys = append(addedKeys, topic.Key())
	}
	for _, batch := range chunkStrings(addedKeys, s.manager.listenBatchSize) {
		nonce, err := s.send(conn, outboundEnvelope{
			Type: "LISTEN",
			Data: &outboundData{
				Topics:    batch,
				AuthToken: accessToken,
			},
		})
		if err != nil {
			return err
		}
		s.markPending(nonce, "LISTEN", batch)
	}

	return nil
}

func (s *shard) addTopic(topic Topic, limit int) bool {
	var wake chan struct{}

	s.mu.Lock()
	if s.topics == nil {
		s.topics = make(map[string]Topic)
	}
	if s.submitted == nil {
		s.submitted = make(map[string]Topic)
	}
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	if len(s.topics) >= limit {
		s.mu.Unlock()
		return false
	}
	s.topics[topic.Key()] = topic
	wake = s.wake
	s.mu.Unlock()

	s.signalWake(wake)
	return true
}

func (s *shard) removeTopics(keys []string) {
	var wake chan struct{}
	var changed bool

	s.mu.Lock()
	if len(s.topics) == 0 {
		s.mu.Unlock()
		return
	}
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	for _, key := range keys {
		if _, exists := s.topics[key]; exists {
			changed = true
		}
		delete(s.topics, key)
	}
	if changed {
		wake = s.wake
	}
	s.mu.Unlock()

	s.signalWake(wake)
}

func (s *shard) clearAndDrainTopics() []Topic {
	s.mu.Lock()
	defer s.mu.Unlock()

	drained := make([]Topic, 0, len(s.topics))
	for _, topic := range s.topics {
		drained = append(drained, topic)
	}
	s.topics = make(map[string]Topic)
	s.submitted = make(map[string]Topic)
	s.pending = make(map[string]pendingSubmission)
	s.connected = false
	return drained
}

func (s *shard) snapshotTopics() (map[string]Topic, map[string]Topic, map[string]struct{}, map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current := make(map[string]Topic, len(s.topics))
	for key, topic := range s.topics {
		current[key] = topic
	}
	submitted := make(map[string]Topic, len(s.submitted))
	for key, topic := range s.submitted {
		submitted[key] = topic
	}
	pendingListen := make(map[string]struct{})
	pendingUnlisten := make(map[string]struct{})
	for _, pending := range s.pending {
		for _, key := range pending.topics {
			switch pending.action {
			case "LISTEN":
				pendingListen[key] = struct{}{}
			case "UNLISTEN":
				pendingUnlisten[key] = struct{}{}
			}
		}
	}

	return current, submitted, pendingListen, pendingUnlisten
}

func (s *shard) lookupTopic(key string) (Topic, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	topic, ok := s.topics[key]
	return topic, ok
}
