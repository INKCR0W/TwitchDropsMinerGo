// Package progress 记录 Twitch 上报过的累计观看时长
//
// badge/emote 活动的进度既不在 inventory 的 self 字段里, 也不在 gameEventDrops 里, 只有观看时的
// dropCurrentSession 会回报, 不落盘的话每次刷新 inventory 都会重新去挂已经完成的活动
package progress

import (
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"time"

	"twitchdropsminergo/internal/storage"
)

const schemaVersion = 1

type Entry struct {
	CampaignID     string    `json:"campaign_id"`
	DropID         string    `json:"drop_id"`
	MinutesWatched int       `json:"minutes_watched"`
	ExpiresAt      time.Time `json:"expires_at,omitzero"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type FileStore struct {
	mu   sync.Mutex
	path string
	data persisted
}

type persisted struct {
	SchemaVersion int              `json:"schema_version"`
	Entries       map[string]Entry `json:"entries"`
}

func NewFileStore(path string) (*FileStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("观看进度文件路径不能为空")
	}

	data, err := storage.LoadJSONFile(path, defaultPersisted())
	if err != nil {
		if _, quarantineErr := storage.QuarantineCorrupt(path); quarantineErr != nil {
			return nil, fmt.Errorf("隔离损坏的观看进度文件失败: %w", quarantineErr)
		}
		data = defaultPersisted()
	}
	if data.Entries == nil {
		data.Entries = make(map[string]Entry)
	}
	return &FileStore{path: path, data: data}, nil
}

func (s *FileStore) Snapshot() []Entry {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries := make([]Entry, 0, len(s.data.Entries))
	for _, entry := range s.data.Entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i int, j int) bool {
		if entries[i].CampaignID != entries[j].CampaignID {
			return entries[i].CampaignID < entries[j].CampaignID
		}
		return entries[i].DropID < entries[j].DropID
	})
	return entries
}

func (s *FileStore) Record(campaignID string, dropID string, minutesWatched int, expiresAt time.Time, now time.Time) error {
	if s == nil {
		return fmt.Errorf("观看进度存储未初始化")
	}

	campaignID = strings.TrimSpace(campaignID)
	dropID = strings.TrimSpace(dropID)
	if campaignID == "" || dropID == "" {
		return fmt.Errorf("观看进度缺少 campaign 或 drop id")
	}
	if expiresAt.IsZero() {
		return fmt.Errorf("观看进度缺少到期时间")
	}
	if minutesWatched <= 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	key := entryKey(campaignID, dropID)
	if current, exists := s.data.Entries[key]; exists && minutesWatched <= current.MinutesWatched {
		return nil
	}

	previous := s.data.Entries
	s.data.Entries = cloneEntries(previous)
	s.data.Entries[key] = Entry{
		CampaignID:     campaignID,
		DropID:         dropID,
		MinutesWatched: minutesWatched,
		ExpiresAt:      expiresAt.UTC(),
		UpdatedAt:      now.UTC(),
	}

	if err := storage.SaveJSONFile(s.path, s.data); err != nil {
		s.data.Entries = previous
		return fmt.Errorf("保存观看进度失败: %w", err)
	}
	return nil
}

func (s *FileStore) PruneExpired(now time.Time) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("观看进度存储未初始化")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now = now.UTC()
	kept := make(map[string]Entry, len(s.data.Entries))
	for key, entry := range s.data.Entries {
		if entry.ExpiresAt.IsZero() || !now.Before(entry.ExpiresAt) {
			continue
		}
		kept[key] = entry
	}

	removed := len(s.data.Entries) - len(kept)
	if removed == 0 {
		return 0, nil
	}

	previous := s.data.Entries
	s.data.Entries = kept
	if err := storage.SaveJSONFile(s.path, s.data); err != nil {
		s.data.Entries = previous
		return 0, fmt.Errorf("清理观看进度失败: %w", err)
	}
	return removed, nil
}

func entryKey(campaignID string, dropID string) string {
	return campaignID + "#" + dropID
}

func cloneEntries(source map[string]Entry) map[string]Entry {
	cloned := make(map[string]Entry, len(source)+1)
	maps.Copy(cloned, source)
	return cloned
}

func defaultPersisted() persisted {
	return persisted{SchemaVersion: schemaVersion, Entries: make(map[string]Entry)}
}
