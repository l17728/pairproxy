package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/db"
)

// AdminTargetSetHandler Admin API: Group Target Set 管理
type AdminTargetSetHandler struct {
	repo   *db.GroupTargetSetRepo
	logger *zap.Logger
}

// NewAdminTargetSetHandler 创建处理器
func NewAdminTargetSetHandler(repo *db.GroupTargetSetRepo, logger *zap.Logger) *AdminTargetSetHandler {
	return &AdminTargetSetHandler{
		repo:   repo,
		logger: logger.Named("admin_targetset_handler"),
	}
}

// ListTargetSets 列出所有 target sets
func (h *AdminTargetSetHandler) ListTargetSets(w http.ResponseWriter, r *http.Request) {
	sets, err := h.repo.ListAll()
	if err != nil {
		h.logger.Error("failed to list target sets", zap.Error(err))
		http.Error(w, "failed to list target sets", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"sets": sets,
	})
}

// CreateTargetSetRequest 创建 target set 的请求
type CreateTargetSetRequest struct {
	Name        string `json:"name"`
	GroupID     *string `json:"group_id"`
	Strategy    string `json:"strategy"`
	RetryPolicy string `json:"retry_policy"`
	Targets     []struct {
		URL      string `json:"url"`
		Weight   int    `json:"weight"`
		Priority int    `json:"priority"`
	} `json:"targets"`
}

// CreateTargetSet 创建 target set
func (h *AdminTargetSetHandler) CreateTargetSet(w http.ResponseWriter, r *http.Request) {
	var req CreateTargetSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	set := &db.GroupTargetSet{
		ID:          uuid.New().String(),
		GroupID:     req.GroupID,
		Name:        req.Name,
		Strategy:    req.Strategy,
		RetryPolicy: req.RetryPolicy,
	}

	if err := h.repo.Create(set); err != nil {
		h.logger.Error("failed to create target set", zap.Error(err))
		http.Error(w, "failed to create target set", http.StatusInternalServerError)
		return
	}

	// 添加 targets
	for _, t := range req.Targets {
		member := &db.GroupTargetSetMember{
			ID:       uuid.New().String(),
			Weight:   t.Weight,
			Priority: t.Priority,
			IsActive: true,
		}
		if err := h.repo.AddMember(set.ID, member); err != nil {
			h.logger.Error("failed to add member", zap.Error(err))
			http.Error(w, "failed to add member", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(set)
}

// DeleteTargetSet 删除 target set
func (h *AdminTargetSetHandler) DeleteTargetSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	if err := h.repo.Delete(id); err != nil {
		h.logger.Error("failed to delete target set", zap.Error(err))
		http.Error(w, "failed to delete target set", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AddTargetRequest 添加 target 的请求
type AddTargetRequest struct {
	URL      string `json:"url"`
	Weight   int    `json:"weight"`
	Priority int    `json:"priority"`
}

// AddTarget 添加 target 到 set
func (h *AdminTargetSetHandler) AddTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	var req AddTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}

	member := &db.GroupTargetSetMember{
		ID:       uuid.New().String(),
		Weight:   req.Weight,
		Priority: req.Priority,
		IsActive: true,
	}

	if err := h.repo.AddMember(id, member); err != nil {
		h.logger.Error("failed to add member", zap.Error(err))
		http.Error(w, "failed to add member", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(member)
}

// RemoveTarget 从 set 移除 target
func (h *AdminTargetSetHandler) RemoveTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	targetURL := r.PathValue("target_url")

	if id == "" || targetURL == "" {
		http.Error(w, "id and target_url are required", http.StatusBadRequest)
		return
	}

	if err := h.repo.RemoveMember(id, targetURL); err != nil {
		h.logger.Error("failed to remove member", zap.Error(err))
		http.Error(w, "failed to remove member", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// UpdateTargetRequest 更新 target 的请求
type UpdateTargetRequest struct {
	Weight   int `json:"weight"`
	Priority int `json:"priority"`
	IsActive bool `json:"is_active"`
}

// UpdateTarget 更新 target 权重
func (h *AdminTargetSetHandler) UpdateTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	targetURL := r.PathValue("target_url")

	if id == "" || targetURL == "" {
		http.Error(w, "id and target_url are required", http.StatusBadRequest)
		return
	}

	var req UpdateTargetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.repo.UpdateMember(id, targetURL, req.Weight, req.Priority); err != nil {
		h.logger.Error("failed to update member", zap.Error(err))
		http.Error(w, "failed to update member", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AdminAlertHandler Admin API: Target Alert 管理
type AdminAlertHandler struct {
	repo   *db.TargetAlertRepo
	logger *zap.Logger
}

// NewAdminAlertHandler 创建处理器
func NewAdminAlertHandler(repo *db.TargetAlertRepo, logger *zap.Logger) *AdminAlertHandler {
	return &AdminAlertHandler{
		repo:   repo,
		logger: logger.Named("admin_alert_handler"),
	}
}

// ListActiveAlerts 获取活跃告警
func (h *AdminAlertHandler) ListActiveAlerts(w http.ResponseWriter, r *http.Request) {
	targetURL := r.URL.Query().Get("target")
	severity := r.URL.Query().Get("severity")
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}

	offset := 0
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			offset = o
		}
	}

	filters := db.AlertFilters{
		TargetURL: targetURL,
		Severity:  severity,
		Limit:     limit,
		Offset:    offset,
	}

	alerts, err := h.repo.ListActive(filters)
	if err != nil {
		h.logger.Error("failed to list active alerts", zap.Error(err))
		http.Error(w, "failed to list active alerts", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"alerts": alerts,
		"summary": map[string]interface{}{
			"total_active": len(alerts),
		},
	})
}

// ListAlertHistory 获取告警历史
func (h *AdminAlertHandler) ListAlertHistory(w http.ResponseWriter, r *http.Request) {
	daysStr := r.URL.Query().Get("days")
	pageStr := r.URL.Query().Get("page")
	pageSizeStr := r.URL.Query().Get("page_size")

	days := 7
	if daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil {
			days = d
		}
	}

	page := 1
	if pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil {
			page = p
		}
	}

	pageSize := 50
	if pageSizeStr != "" {
		if ps, err := strconv.Atoi(pageSizeStr); err == nil {
			pageSize = ps
		}
	}

	alerts, err := h.repo.ListHistory(days, page, pageSize)
	if err != nil {
		h.logger.Error("failed to list alert history", zap.Error(err))
		http.Error(w, "failed to list alert history", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"alerts": alerts,
		"pagination": map[string]interface{}{
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// ResolveAlertRequest 解决告警的请求
type ResolveAlertRequest struct {
	Reason string `json:"reason"`
}

// ResolveAlert 手动解决告警
func (h *AdminAlertHandler) ResolveAlert(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	if err := h.repo.Resolve(id); err != nil {
		h.logger.Error("failed to resolve alert", zap.Error(err))
		http.Error(w, "failed to resolve alert", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetAlertStats 获取告警统计
func (h *AdminAlertHandler) GetAlertStats(w http.ResponseWriter, r *http.Request) {
	daysStr := r.URL.Query().Get("days")

	days := 30
	if daysStr != "" {
		if d, err := strconv.Atoi(daysStr); err == nil {
			days = d
		}
	}

	stats, err := h.repo.GetStats(days)
	if err != nil {
		h.logger.Error("failed to get alert stats", zap.Error(err))
		http.Error(w, "failed to get alert stats", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// SSEAlertHandler SSE 告警流处理器
type SSEAlertHandler struct {
	alertManager interface {
		SubscribeEvents() <-chan alert.AlertEvent
	}
	logger *zap.Logger
}

// NewSSEAlertHandler 创建 SSE 处理器
func NewSSEAlertHandler(alertManager interface {
	SubscribeEvents() <-chan alert.AlertEvent
}, logger *zap.Logger) *SSEAlertHandler {
	return &SSEAlertHandler{
		alertManager: alertManager,
		logger:       logger.Named("sse_alert_handler"),
	}
}

// StreamAlerts SSE 告警流
func (h *SSEAlertHandler) StreamAlerts(w http.ResponseWriter, r *http.Request) {
	// 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// 订阅告警事件
	eventCh := h.alertManager.SubscribeEvents()

	// 发送初始连接消息
	fmt.Fprintf(w, "event: connected\ndata: {\"message\": \"connected to alert stream\"}\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// 监听事件
	for {
		select {
		case <-r.Context().Done():
			h.logger.Debug("SSE client disconnected")
			return
		case event := <-eventCh:
			// 序列化事件
			data, err := json.Marshal(event)
			if err != nil {
				h.logger.Error("failed to marshal event", zap.Error(err))
				continue
			}

			// 发送事件
			fmt.Fprintf(w, "event: alert_update\ndata: %s\n\n", string(data))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}
}
