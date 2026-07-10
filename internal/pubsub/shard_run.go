package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"twitchdropsminergo/internal/httpclient"
)

func (s *shard) run(ctx context.Context) {
	defer s.manager.wg.Done()

	s.setState(ShardStateDisconnected, false)
	backoff, _ := httpclient.NewExponentialBackoff(s.manager.backoff)
	for {
		if err := ctx.Err(); err != nil {
			s.finishRun()
			return
		}

		if s.topicCount() == 0 {
			s.setState(ShardStateDisconnected, false)
			if err := s.manager.sleep(ctx, s.manager.readTimeout); err != nil {
				s.finishRun()
				return
			}
			continue
		}

		s.setState(ShardStateConnecting, false)
		if err := s.manager.auth.Validate(ctx); err != nil {
			s.manager.logger.Warn("验证 PubSub 认证失败", "shard", s.index, "error", err)
			if err := s.manager.sleep(ctx, backoff.Next()); err != nil {
				s.finishRun()
				return
			}
			continue
		}

		headers, err := s.manager.buildHeaders(ctx)
		if err != nil {
			s.manager.logger.Warn("构造 PubSub 握手请求头失败", "shard", s.index, "error", err)
			if err := s.manager.sleep(ctx, backoff.Next()); err != nil {
				s.finishRun()
				return
			}
			continue
		}

		conn, _, err := s.manager.dialer.DialContext(ctx, s.manager.endpoint, headers)
		if err != nil {
			s.manager.logger.Warn("连接 PubSub 失败", "shard", s.index, "error", err)
			if err := s.manager.sleep(ctx, backoff.Next()); err != nil {
				s.finishRun()
				return
			}
			continue
		}

		s.setConn(conn)
		s.setState(ShardStateConnecting, true)
		connectedAt := s.manager.now()
		if err := s.handleConnection(ctx, conn); err != nil && ctx.Err() == nil {
			s.manager.logger.Warn("PubSub 连接断开，准备重连", "shard", s.index, "error", err)
		}

		_ = conn.Close()
		s.clearConn()
		if ctx.Err() != nil {
			s.finishRun()
			return
		}

		if s.topicCount() == 0 {
			s.setState(ShardStateDisconnected, false)
			backoff.Reset()
			continue
		}

		s.setState(ShardStateReconnecting, false)
		if s.manager.now().Sub(connectedAt) >= minConnectionLifetimeForBackoffReset {
			backoff.Reset()
		}
		if err := s.manager.sleep(ctx, backoff.Next()); err != nil {
			s.finishRun()
			return
		}
	}
}

func (s *shard) finishRun() {
	s.mu.Lock()
	done := s.done
	s.done = nil
	s.cancel = nil
	s.conn = nil
	s.started = false
	s.connected = false
	s.submitted = make(map[string]Topic)
	s.pending = make(map[string]pendingSubmission)
	s.state = ShardStateDisconnected
	s.mu.Unlock()

	s.manager.signalChanged()
	if done != nil {
		close(done)
	}
}

func (s *shard) handleConnection(ctx context.Context, conn Connection) error {
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()

	results := make(chan readResult, 1)
	go s.readLoop(readCtx, conn, results)

	nextPing := s.manager.now()
	pongDeadline := time.Time{}
	wake := s.wakeChan()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		now := s.manager.now()
		if !pongDeadline.IsZero() && !now.Before(pongDeadline) {
			return fmt.Errorf("等待 PONG 超时")
		}
		if !now.Before(nextPing) {
			if _, err := s.send(conn, outboundEnvelope{Type: "PING"}); err != nil {
				return err
			}
			nextPing = now.Add(s.manager.pingInterval)
			pongDeadline = now.Add(s.manager.pingTimeout)
		}

		if err := s.syncTopics(ctx, conn); err != nil {
			return err
		}

		timer := time.NewTimer(s.nextWaitDelay(nextPing, pongDeadline))
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return ctx.Err()
		case <-wake:
			stopTimer(timer)
		case result, ok := <-results:
			stopTimer(timer)
			if !ok {
				if err := ctx.Err(); err != nil {
					return err
				}
				return fmt.Errorf("PubSub 读循环意外结束")
			}
			if result.err != nil {
				return result.err
			}

			pongReceived, reconnect, err := s.handleInbound(ctx, result.messageType, result.payload)
			if err != nil {
				return err
			}
			if pongReceived {
				pongDeadline = time.Time{}
			}
			if reconnect {
				return fmt.Errorf("服务器请求重连")
			}
		case <-timer.C:
		}
	}
}

func (s *shard) readLoop(ctx context.Context, conn Connection, results chan<- readResult) {
	defer close(results)

	for {
		messageType, payload, err := conn.ReadMessage()
		select {
		case results <- readResult{messageType: messageType, payload: payload, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *shard) handleInbound(ctx context.Context, messageType int, payload []byte) (pongReceived bool, reconnect bool, err error) {
	if messageType != textMessageType {
		return false, false, nil
	}

	var envelope inboundEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		s.manager.logger.Warn("跳过无法解析的 PubSub 消息", "shard", s.index, "error", err)
		return false, false, nil
	}

	switch envelope.Type {
	case "MESSAGE":
		if err := s.dispatchMessage(ctx, envelope.Data); err != nil {
			return false, false, err
		}
	case "PONG":
		return true, false, nil
	case "RESPONSE":
		if err := s.resolvePending(envelope.Nonce, envelope.Error); err != nil {
			return false, false, err
		}
	case "RECONNECT":
		return false, true, nil
	default:
		s.manager.logger.Warn("收到未知 PubSub 消息类型", "shard", s.index, "type", envelope.Type)
	}

	return false, false, nil
}

func (s *shard) dispatchMessage(ctx context.Context, payload json.RawMessage) error {
	var message inboundMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return fmt.Errorf("解析 PubSub 事件失败: %w", err)
	}

	topic, ok := s.lookupTopic(message.Topic)
	if !ok || topic.Handler() == nil {
		return nil
	}

	raw := json.RawMessage(message.Message)
	if !json.Valid(raw) {
		quoted, err := json.Marshal(message.Message)
		if err != nil {
			return fmt.Errorf("编码 PubSub 字符串消息失败: %w", err)
		}
		raw = quoted
	}

	event := Event{
		Topic:      topic,
		Message:    raw,
		ReceivedAt: s.manager.now().UTC(),
	}

	events := s.eventsChan()
	if events == nil {
		return nil
	}
	select {
	case events <- event:
	case <-ctx.Done():
		return ctx.Err()
	default:
		s.manager.logger.Warn("PubSub 事件队列已满，丢弃事件", "shard", s.index, "topic", topic.Key())
	}

	return nil
}

func (s *shard) send(conn Connection, envelope outboundEnvelope) (string, error) {
	if conn == nil {
		return "", fmt.Errorf("PubSub 连接不存在")
	}
	nonce := ""
	if envelope.Type != "PING" {
		generated, err := s.manager.nonceGenerator()
		if err != nil {
			return "", err
		}
		nonce = generated
		envelope.Nonce = nonce
	}
	if err := conn.SetWriteDeadline(s.manager.now().Add(s.manager.pingTimeout)); err != nil {
		return "", err
	}
	if err := conn.WriteJSON(envelope); err != nil {
		return "", err
	}

	return nonce, nil
}
