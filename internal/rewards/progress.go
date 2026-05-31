package rewards

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/storage"
)

const schemaVersion = 1

type Progress struct {
	CampaignID     string    `json:"campaign_id"`
	DropID         string    `json:"drop_id"`
	MinutesWatched int       `json:"minutes_watched"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Store interface {
	Snapshot() map[string]Progress
	RecordProgress(campaignID string, dropID string, minutesWatched int, completed bool, now time.Time) (Progress, error)
}

type FileStore struct {
	mu   sync.Mutex
	path string
	data persistedProgress
}

type persistedProgress struct {
	SchemaVersion int                 `json:"schema_version"`
	Progress      map[string]Progress `json:"progress"`
}

func NewFileStore(path string) (*FileStore, error) {
	store := &FileStore{path: strings.TrimSpace(path)}
	if store.path == "" {
		return nil, fmt.Errorf("reward 进度文件路径不能为空")
	}
	data, err := storage.LoadJSONFile(store.path, defaultPersistedProgress())
	if err != nil {
		if !backupCorruptProgressFile(store.path) && !backupCorruptProgressFile(store.path+".bak") {
			return nil, fmt.Errorf("加载 reward 进度失败: %w", err)
		}
		data = defaultPersistedProgress()
	}
	store.data = normalizePersistedProgress(data)
	return store, nil
}

func (s *FileStore) Snapshot() map[string]Progress {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProgressMap(s.data.Progress)
}

func (s *FileStore) RecordProgress(campaignID string, dropID string, minutesWatched int, completed bool, now time.Time) (Progress, error) {
	if s == nil {
		return Progress{}, fmt.Errorf("reward 进度存储未初始化")
	}

	campaignID = strings.TrimSpace(campaignID)
	dropID = strings.TrimSpace(dropID)
	if campaignID == "" {
		return Progress{}, fmt.Errorf("reward campaign id 不能为空")
	}
	if dropID == "" {
		return Progress{}, fmt.Errorf("reward drop id 不能为空")
	}
	if minutesWatched < 0 {
		minutesWatched = 0
	}
	now = now.UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.data.Progress == nil {
		s.data.Progress = make(map[string]Progress)
	}

	previous := s.data
	previous.Progress = cloneProgressMap(s.data.Progress)
	current := s.data.Progress[campaignID]
	if current.CampaignID == "" {
		current.CampaignID = campaignID
	}
	current.DropID = dropID
	if minutesWatched > current.MinutesWatched {
		current.MinutesWatched = minutesWatched
	}
	if completed && current.CompletedAt.IsZero() {
		current.CompletedAt = now
	}
	current.UpdatedAt = now
	s.data.Progress[campaignID] = current

	if err := storage.SaveJSONFile(s.path, s.data); err != nil {
		s.data = previous
		return Progress{}, fmt.Errorf("保存 reward 进度失败: %w", err)
	}
	return current, nil
}

func CompletedCampaignIDs(progress map[string]Progress) map[string]struct{} {
	completed := make(map[string]struct{})
	for campaignID, item := range progress {
		campaignID = strings.TrimSpace(campaignID)
		if campaignID == "" || item.CompletedAt.IsZero() {
			continue
		}
		completed[campaignID] = struct{}{}
	}
	return completed
}

func defaultPersistedProgress() persistedProgress {
	return persistedProgress{
		SchemaVersion: schemaVersion,
		Progress:      map[string]Progress{},
	}
}

func normalizePersistedProgress(data persistedProgress) persistedProgress {
	if data.SchemaVersion < schemaVersion {
		data.SchemaVersion = schemaVersion
	}
	if data.Progress == nil {
		data.Progress = make(map[string]Progress)
	}
	return data
}

func cloneProgressMap(progress map[string]Progress) map[string]Progress {
	if len(progress) == 0 {
		return nil
	}

	cloned := make(map[string]Progress, len(progress))
	for key, value := range progress {
		cloned[key] = value
	}
	return cloned
}

func backupCorruptProgressFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if _, err := os.ReadFile(path); err != nil {
		return false
	}

	backupPath := path + ".bad"
	_ = os.Remove(backupPath)
	return os.Rename(path, backupPath) == nil
}
