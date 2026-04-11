package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"twitchdropsminergo/internal/config"
)

const maxSettingsBodyBytes int64 = 1 << 20

type Options struct {
	Logger          *slog.Logger
	Now             func() time.Time
	ListenAddress   string
	Health          func(context.Context) HealthResponse
	Status          func(context.Context) (StatusResponse, error)
	CurrentSettings func(context.Context) (config.Settings, error)
	UpdateSettings  func(context.Context, config.Settings) (config.Settings, error)
	Reload          func(context.Context) error
}

type Handler struct {
	logger          *slog.Logger
	now             func() time.Time
	health          func(context.Context) HealthResponse
	status          func(context.Context) (StatusResponse, error)
	currentSettings func(context.Context) (config.Settings, error)
	updateSettings  func(context.Context, config.Settings) (config.Settings, error)
	reload          func(context.Context) error

	metricsMu sync.Mutex
	metrics   RequestMetrics
}

type HealthResponse struct {
	Status string    `json:"status"`
	Time   time.Time `json:"time"`
}

type StatusResponse struct {
	Time     time.Time       `json:"time"`
	Healthy  bool            `json:"healthy"`
	Runtime  RuntimeStatus   `json:"runtime"`
	Auth     AuthStatus      `json:"auth"`
	Schedule ScheduleStatus  `json:"schedule"`
	Settings config.Settings `json:"settings"`
	API      RequestMetrics  `json:"api"`
}

type RuntimeStatus struct {
	SchemaVersion int       `json:"schema_version"`
	RunCount      int       `json:"run_count"`
	LastStartedAt time.Time `json:"last_started_at,omitempty"`
	LastStoppedAt time.Time `json:"last_stopped_at,omitempty"`
}

type AuthStatus struct {
	LoggedIn bool  `json:"logged_in"`
	UserID   int64 `json:"user_id,omitempty"`
}

type ScheduleStatus struct {
	State                  string          `json:"state"`
	WantedGames            []GameStatus    `json:"wanted_games,omitempty"`
	WatchingChannelID      int64           `json:"watching_channel_id,omitempty"`
	SelectedChannelID      int64           `json:"selected_channel_id,omitempty"`
	FullCleanup            bool            `json:"full_cleanup"`
	LastProgressAt         time.Time       `json:"last_progress_at,omitempty"`
	ChannelCount           int             `json:"channel_count"`
	Channels               []ChannelStatus `json:"channels,omitempty"`
	InventoryCampaignCount int             `json:"inventory_campaign_count"`
	InventoryDropCount     int             `json:"inventory_drop_count"`
	UserTopicUserID        int64           `json:"user_topic_user_id,omitempty"`
	PubSub                 PubSubStatus    `json:"pubsub"`
}

type GameStatus struct {
	ID   int64  `json:"id,omitempty"`
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

type ChannelStatus struct {
	ID            int64         `json:"id"`
	Login         string        `json:"login"`
	DisplayName   string        `json:"display_name,omitempty"`
	ACLBased      bool          `json:"acl_based"`
	PendingStream bool          `json:"pending_stream"`
	Online        bool          `json:"online"`
	Stream        *StreamStatus `json:"stream,omitempty"`
}

type StreamStatus struct {
	BroadcastID  int64       `json:"broadcast_id,omitempty"`
	Game         *GameStatus `json:"game,omitempty"`
	Viewers      int         `json:"viewers,omitempty"`
	Title        string      `json:"title,omitempty"`
	DropsEnabled bool        `json:"drops_enabled"`
}

type PubSubStatus struct {
	Running    bool                `json:"running"`
	Endpoint   string              `json:"endpoint,omitempty"`
	TopicCount int                 `json:"topic_count"`
	Shards     []PubSubShardStatus `json:"shards,omitempty"`
}

type PubSubShardStatus struct {
	Index          int    `json:"index"`
	State          string `json:"state"`
	Connected      bool   `json:"connected"`
	TopicCount     int    `json:"topic_count"`
	SubmittedCount int    `json:"submitted_count"`
}

type RequestMetrics struct {
	ListenAddress string            `json:"listen_address,omitempty"`
	StartedAt     time.Time         `json:"started_at,omitempty"`
	LastRequestAt time.Time         `json:"last_request_at,omitempty"`
	TotalRequests uint64            `json:"total_requests"`
	ErrorCount    uint64            `json:"error_count"`
	Routes        map[string]uint64 `json:"routes,omitempty"`
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func NewHandler(options Options) (*Handler, error) {
	if options.Status == nil {
		return nil, fmt.Errorf("status 回调不能为空")
	}
	if options.CurrentSettings == nil {
		return nil, fmt.Errorf("settings 读取回调不能为空")
	}
	if options.UpdateSettings == nil {
		return nil, fmt.Errorf("settings 更新回调不能为空")
	}
	if options.Reload == nil {
		return nil, fmt.Errorf("reload 回调不能为空")
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	now := options.Now
	if now == nil {
		now = time.Now
	}

	health := options.Health
	if health == nil {
		health = func(context.Context) HealthResponse {
			return HealthResponse{
				Status: "ok",
				Time:   now().UTC(),
			}
		}
	}

	return &Handler{
		logger:          logger,
		now:             now,
		health:          health,
		status:          options.Status,
		currentSettings: options.CurrentSettings,
		updateSettings:  options.UpdateSettings,
		reload:          options.Reload,
		metrics: RequestMetrics{
			ListenAddress: options.ListenAddress,
			StartedAt:     now().UTC(),
			Routes:        make(map[string]uint64),
		},
	}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	recorder := &statusRecorder{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
	}

	startedAt := h.now().UTC()
	h.route(recorder, r)
	h.recordRequest(r.Method+" "+r.URL.Path, startedAt, recorder.statusCode)
	h.logger.Info(
		"处理运维请求",
		"method", r.Method,
		"path", r.URL.Path,
		"status", recorder.statusCode,
		"duration", h.now().UTC().Sub(startedAt).String(),
	)
}

func (h *Handler) route(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		h.handleHealth(w, r)
	case "/status":
		h.handleStatus(w, r)
	case "/settings":
		h.handleSettings(w, r)
	case "/reload":
		h.handleReload(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "未找到接口",
		})
	}
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	response := h.health(r.Context())
	if response.Status == "" {
		response.Status = "ok"
	}
	if response.Time.IsZero() {
		response.Time = h.now().UTC()
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	response, err := h.status(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	response.Time = h.now().UTC()
	response.Settings = response.Settings.Sanitized()
	response.API = h.metricsSnapshot()
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := h.currentSettings(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, settings.Sanitized())
	case http.MethodPut:
		body := http.MaxBytesReader(w, r.Body, maxSettingsBodyBytes)
		defer body.Close()

		var settings config.Settings
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&settings); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "配置 JSON 无效",
			})
			return
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "配置 JSON 只能包含一个对象",
			})
			return
		}
		if err := settings.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": err.Error(),
			})
			return
		}

		updated, err := h.updateSettings(r.Context(), settings)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, updated.Sanitized())
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (h *Handler) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	if err := h.reload(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "reload_requested",
		"time":   h.now().UTC(),
	})
}

func (h *Handler) recordRequest(route string, stamp time.Time, statusCode int) {
	h.metricsMu.Lock()
	defer h.metricsMu.Unlock()

	h.metrics.TotalRequests++
	h.metrics.LastRequestAt = stamp
	h.metrics.Routes[route]++
	if statusCode >= http.StatusBadRequest {
		h.metrics.ErrorCount++
	}
}

func (h *Handler) metricsSnapshot() RequestMetrics {
	h.metricsMu.Lock()
	defer h.metricsMu.Unlock()

	snapshot := h.metrics
	snapshot.Routes = make(map[string]uint64, len(h.metrics.Routes))
	for route, count := range h.metrics.Routes {
		snapshot.Routes[route] = count
	}
	return snapshot
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.statusCode = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	if len(methods) > 0 {
		w.Header().Set("Allow", methods[0])
		for _, method := range methods[1:] {
			w.Header().Add("Allow", method)
		}
	}
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{
		"error": "请求方法不受支持",
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"error":"序列化响应失败"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_, _ = w.Write(append(data, '\n'))
}
