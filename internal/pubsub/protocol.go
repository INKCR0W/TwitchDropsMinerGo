package pubsub

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

const (
	defaultNonceLength = 30
	textMessageType    = 1
)

type outboundEnvelope struct {
	Type  string        `json:"type"`
	Nonce string        `json:"nonce,omitempty"`
	Data  *outboundData `json:"data,omitempty"`
}

type outboundData struct {
	Topics    []string `json:"topics"`
	AuthToken string   `json:"auth_token"`
}

type inboundEnvelope struct {
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

type inboundMessage struct {
	Topic   string `json:"topic"`
	Message string `json:"message"`
}

type readResult struct {
	messageType int
	payload     []byte
	err         error
}

func uniqueTopics(topics []Topic) []Topic {
	if len(topics) == 0 {
		return []Topic{}
	}

	seen := make(map[string]struct{}, len(topics))
	unique := make([]Topic, 0, len(topics))
	for _, topic := range topics {
		if topic.Key() == "" {
			continue
		}
		if _, exists := seen[topic.Key()]; exists {
			continue
		}
		seen[topic.Key()] = struct{}{}
		unique = append(unique, topic)
	}

	sort.Slice(unique, func(i, j int) bool {
		return unique[i].Key() < unique[j].Key()
	})
	return unique
}

func chunkStrings(values []string, size int) [][]string {
	if len(values) == 0 {
		return nil
	}
	if size <= 0 {
		size = len(values)
	}

	chunks := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		chunk := append([]string(nil), values[start:end]...)
		chunks = append(chunks, chunk)
	}

	return chunks
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer stopTimer(timer)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func generateNonce() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	random := make([]byte, defaultNonceLength)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("生成 PubSub nonce 失败: %w", err)
	}

	nonce := make([]byte, defaultNonceLength)
	for index, value := range random {
		nonce[index] = alphabet[int(value)%len(alphabet)]
	}

	return string(nonce), nil
}
