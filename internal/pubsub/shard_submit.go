package pubsub

import (
	"fmt"
	"strings"
)

func (s *shard) markUnsubmitted(keys []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, key := range keys {
		delete(s.submitted, key)
	}
}

func (s *shard) markPending(nonce string, action string, topics []string) {
	if nonce == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending == nil {
		s.pending = make(map[string]pendingSubmission)
	}
	s.pending[nonce] = pendingSubmission{
		action: action,
		topics: append([]string(nil), topics...),
	}
}

func (s *shard) resolvePending(nonce string, responseErr string) error {
	if nonce == "" {
		if responseErr != "" {
			return fmt.Errorf("PubSub RESPONSE 错误: %s", responseErr)
		}
		return nil
	}

	s.mu.Lock()
	pending, ok := s.pending[nonce]
	if ok {
		delete(s.pending, nonce)
	}
	currentTopics := make(map[string]Topic, len(s.topics))
	for key, topic := range s.topics {
		currentTopics[key] = topic
	}
	s.mu.Unlock()

	if responseErr != "" {
		if isAuthResponseError(responseErr) {
			action := pending.action
			if action == "" {
				action = "请求"
			}
			return fmt.Errorf("PubSub %s 认证被拒绝: %s", action, responseErr)
		}
		s.manager.logger.Warn("PubSub 订阅被拒绝，丢弃相关 topic 并保持连接",
			"shard", s.index, "action", pending.action, "error", responseErr, "topics", pending.topics)
		if pending.action == "LISTEN" {
			s.removeTopics(pending.topics)
		} else {
			s.markUnsubmitted(pending.topics)
		}
		s.refreshConnectedState()
		return nil
	}
	if !ok {
		return nil
	}

	switch pending.action {
	case "LISTEN":
		s.markSubmitted(pending.topics, currentTopics)
	case "UNLISTEN":
		s.markUnsubmitted(pending.topics)
	}
	s.refreshConnectedState()
	return nil
}

func isAuthResponseError(responseErr string) bool {
	upper := strings.ToUpper(responseErr)
	return strings.Contains(upper, "BADAUTH") || strings.Contains(upper, "UNAUTHORIZED")
}

func (s *shard) markSubmitted(keys []string, current map[string]Topic) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.submitted == nil {
		s.submitted = make(map[string]Topic)
	}
	for _, key := range keys {
		topic, ok := current[key]
		if !ok {
			continue
		}
		if _, exists := s.topics[key]; !exists {
			continue
		}
		s.submitted[key] = topic
	}
}
