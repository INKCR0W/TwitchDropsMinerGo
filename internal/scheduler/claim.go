package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"twitchdropsminergo/internal/gql"
)

func (s *Scheduler) claimReadyDrops(ctx context.Context, candidates []claimCandidate) claimSweepResult {
	result := claimSweepResult{Total: len(candidates)}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			result.TimedOut = true
			return result
		}

		ok, err := s.claimDropRequest(ctx, candidate.ClaimID)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				result.TimedOut = true
				return result
			}
			result.Failed++
			s.logger.Warn("认领掉宝失败", "campaign_id", candidate.CampaignID, "drop_id", candidate.DropID, "error", err)
			continue
		}
		if ok {
			result.Claimed++
			s.markDropClaimed(candidate.DropID)
			s.logger.Info("认领掉宝成功", "campaign_id", candidate.CampaignID, "drop_id", candidate.DropID)
		}
	}

	return result
}

func (s *Scheduler) claimDropRequest(ctx context.Context, claimID string) (bool, error) {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return false, nil
	}

	operation, err := gql.MustLookup(gql.OperationClaimDrop).WithVariables(map[string]any{
		"input": map[string]any{
			"dropInstanceID": claimID,
		},
	})
	if err != nil {
		return false, fmt.Errorf("构造 ClaimDrop 请求失败: %w", err)
	}

	response, err := s.gqlClient.Do(ctx, operation)
	if err != nil {
		return false, err
	}

	data, err := asMap(response.Data, "data")
	if err != nil {
		return false, err
	}
	claimData := optionalMap(data["claimDropRewards"])
	switch stringValue(claimData, "status") {
	case "ELIGIBLE_FOR_ALL", "DROP_INSTANCE_ALREADY_CLAIMED":
		return true, nil
	default:
		return false, nil
	}
}

func (s *Scheduler) processDropClaim(ctx context.Context, message dropEventMessage) error {
	dropID := strings.TrimSpace(message.Data.DropID)
	claimID := strings.TrimSpace(message.Data.DropInstanceID)
	if dropID == "" {
		return nil
	}

	campaignID, effectiveClaimID, ok := s.updateDropClaim(dropID, claimID)
	if !ok {
		s.logger.Warn("收到未知掉宝的认领事件", "drop_id", dropID, "claim_id", claimID)
		return nil
	}

	claimed, err := s.claimDropRequest(ctx, effectiveClaimID)
	if err != nil {
		return fmt.Errorf("认领 websocket 掉宝失败: %w", err)
	}
	if claimed {
		s.markDropClaimed(dropID)
	}

	watchingChannel := s.currentWatchingChannel()
	if watchingChannel != nil {
		if err := s.sleep(ctx, 4*time.Second); err != nil {
			return err
		}
		for attempt := 0; attempt < 8; attempt++ {
			currentDropID, _, ok, err := s.fetchCurrentDrop(ctx, watchingChannel.ID)
			if err != nil {
				return fmt.Errorf("轮询 CurrentDrop 失败: %w", err)
			}
			if !ok || currentDropID != dropID {
				break
			}
			if err := s.sleep(ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}

	if s.campaignCanEarn(campaignID, watchingChannel) {
		s.restartWatching()
		return nil
	}

	s.ChangeState(StateInventoryFetch)
	return nil
}

func (s *Scheduler) deleteNotification(ctx context.Context, notificationID string) error {
	operation, err := gql.MustLookup(gql.OperationNotificationsDelete).WithVariables(map[string]any{
		"input": map[string]any{
			"id": notificationID,
		},
	})
	if err != nil {
		return fmt.Errorf("构造 NotificationsDelete 请求失败: %w", err)
	}

	if _, err := s.gqlClient.Do(ctx, operation); err != nil {
		return err
	}
	return nil
}

func (s *Scheduler) readyDrops(now time.Time) []claimCandidate {
	snapshot := s.snapshotCopy()
	ready := make([]claimCandidate, 0)
	for _, campaign := range snapshot.Inventory {
		if campaign == nil || campaign.UpcomingAt(now) {
			continue
		}
		for _, drop := range campaign.Drops() {
			if !drop.CanClaim(now) {
				continue
			}
			ready = append(ready, claimCandidate{
				CampaignID: campaign.ID,
				DropID:     drop.ID,
				ClaimID:    drop.ClaimID,
			})
		}
	}
	return ready
}

func (s *Scheduler) updateDropClaim(dropID string, claimID string) (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil {
		return "", "", false
	}
	if claimID != "" {
		drop.UpdateClaim(claimID)
	}

	campaignID := ""
	if drop.Campaign != nil {
		campaignID = drop.Campaign.ID
	}
	return campaignID, drop.ClaimID, true
}

func (s *Scheduler) markDropClaimed(dropID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	drop := s.snapshot.Drops[dropID]
	if drop == nil {
		return false
	}
	return drop.MarkClaimed()
}
