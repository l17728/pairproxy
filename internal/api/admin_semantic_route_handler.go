package api

import (
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/router"
)

// AdminSemanticRouteHandler 处理语义路由规则管理 REST API
type AdminSemanticRouteHandler struct {
	logger            *zap.Logger
	jwtMgr            *auth.Manager
	repo              *db.SemanticRouteRepo
	auditRepo         *db.AuditRepo
	semanticRouter    *router.SemanticRouter // 写操作后热更新
	adminPasswordHash string
	tokenTTL          time.Duration
}

// NewAdminSemanticRouteHandler 创建 AdminSemanticRouteHandler
func NewAdminSemanticRouteHandler(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	repo *db.SemanticRouteRepo,
	auditRepo *db.AuditRepo,
	semanticRouter *router.SemanticRouter,
	adminPasswordHash string,
	tokenTTL time.Duration,
) *AdminSemanticRouteHandler {
	return &AdminSemanticRouteHandler{
		logger:            logger.Named("admin_semantic_route_handler"),
		jwtMgr:            jwtMgr,
		repo:              repo,
		auditRepo:         auditRepo,
		semanticRouter:    semanticRouter,
		adminPasswordHash: adminPasswordHash,
		tokenTTL:          tokenTTL,
	}
}

// RegisterRoutes 注册路由到 mux
func (h *AdminSemanticRouteHandler) RegisterRoutes(
	mux *http.ServeMux,
	requireAdmin func(http.Handler) http.Handler,
	requireWritable func(http.Handler) http.Handler,
) {
	w := requireWritable
	mux.Handle("GET /api/admin/semantic-routes", requireAdmin(http.HandlerFunc(h.handleList)))
	mux.Handle("POST /api/admin/semantic-routes", requireAdmin(w(http.HandlerFunc(h.handleCreate))))
	mux.Handle("GET /api/admin/semantic-routes/{id}", requireAdmin(http.HandlerFunc(h.handleGet)))
	mux.Handle("PUT /api/admin/semantic-routes/{id}", requireAdmin(w(http.HandlerFunc(h.handleUpdate))))
	mux.Handle("DELETE /api/admin/semantic-routes/{id}", requireAdmin(w(http.HandlerFunc(h.handleDelete))))
	mux.Handle("POST /api/admin/semantic-routes/{id}/enable", requireAdmin(w(http.HandlerFunc(h.handleEnable))))
	mux.Handle("POST /api/admin/semantic-routes/{id}/disable", requireAdmin(w(http.HandlerFunc(h.handleDisable))))
}

// ---------------------------------------------------------------------------
// Request / Response 结构体
// ---------------------------------------------------------------------------

type semanticRouteResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	TargetURLs  []string `json:"target_urls"`
	Priority    int      `json:"priority"`
	IsActive    bool     `json:"is_active"`
	Source      string   `json:"source"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type createSemanticRouteRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	TargetURLs  []string `json:"target_urls"`
	Priority    int      `json:"priority"`
}

type updateSemanticRouteRequest struct {
	Description *string  `json:"description,omitempty"`
	TargetURLs  []string `json:"target_urls,omitempty"`
	Priority    *int     `json:"priority,omitempty"`
}

func toSemanticRouteResponse(r *db.SemanticRoute) *semanticRouteResponse {
	urls, _ := r.DecodeTargetURLs()
	return &semanticRouteResponse{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		TargetURLs:  urls,
		Priority:    r.Priority,
		IsActive:    r.IsActive,
		Source:      r.Source,
		CreatedAt:   r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   r.UpdatedAt.Format(time.RFC3339),
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// GET /api/admin/semantic-routes
func (h *AdminSemanticRouteHandler) handleList(w http.ResponseWriter, r *http.Request) {
	routes, err := h.repo.ListAll()
	if err != nil {
		h.logger.Error("failed to list semantic routes", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to list semantic routes")
		return
	}
	resp := make([]*semanticRouteResponse, 0, len(routes))
	for i := range routes {
		resp = append(resp, toSemanticRouteResponse(&routes[i]))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// POST /api/admin/semantic-routes
func (h *AdminSemanticRouteHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createSemanticRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_field", "name is required")
		return
	}
	if req.Description == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_field", "description is required")
		return
	}
	if len(req.TargetURLs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "missing_field", "target_urls must not be empty")
		return
	}

	route, err := h.repo.Create(req.Name, req.Description, req.TargetURLs, req.Priority)
	if err != nil {
		h.logger.Error("failed to create semantic route",
			zap.String("name", req.Name),
			zap.Error(err),
		)
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to create semantic route")
		return
	}

	// 审计日志
	_ = h.auditRepo.Create("admin", "create_semantic_route", route.Name, route.ID)

	h.reloadRouterRules()

	h.logger.Info("semantic route created",
		zap.String("id", route.ID),
		zap.String("name", route.Name),
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(toSemanticRouteResponse(route))
}

// GET /api/admin/semantic-routes/{id}
func (h *AdminSemanticRouteHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	route, err := h.repo.GetByID(id)
	if err != nil {
		h.logger.Warn("semantic route not found", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusNotFound, "not_found", "semantic route not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toSemanticRouteResponse(route))
}

// PUT /api/admin/semantic-routes/{id}
func (h *AdminSemanticRouteHandler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateSemanticRouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json", "invalid request body")
		return
	}

	updates := map[string]interface{}{
		"updated_at": time.Now(),
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.Priority != nil {
		updates["priority"] = *req.Priority
	}
	if len(req.TargetURLs) > 0 {
		b, _ := json.Marshal(req.TargetURLs)
		updates["target_urls"] = string(b)
	}

	if err := h.repo.Update(id, updates); err != nil {
		h.logger.Error("failed to update semantic route", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to update semantic route")
		return
	}

	_ = h.auditRepo.Create("admin", "update_semantic_route", id, "")
	h.reloadRouterRules()

	route, _ := h.repo.GetByID(id)
	if route == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	h.logger.Info("semantic route updated", zap.String("id", id))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toSemanticRouteResponse(route))
}

// DELETE /api/admin/semantic-routes/{id}
func (h *AdminSemanticRouteHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.repo.Delete(id); err != nil {
		h.logger.Error("failed to delete semantic route", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to delete semantic route")
		return
	}
	_ = h.auditRepo.Create("admin", "delete_semantic_route", id, "")
	h.reloadRouterRules()
	h.logger.Info("semantic route deleted", zap.String("id", id))
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/admin/semantic-routes/{id}/enable
func (h *AdminSemanticRouteHandler) handleEnable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.repo.SetActive(id, true); err != nil {
		h.logger.Error("failed to enable semantic route", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to enable semantic route")
		return
	}
	_ = h.auditRepo.Create("admin", "enable_semantic_route", id, "")
	h.reloadRouterRules()
	h.logger.Info("semantic route enabled", zap.String("id", id))
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/admin/semantic-routes/{id}/disable
func (h *AdminSemanticRouteHandler) handleDisable(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.repo.SetActive(id, false); err != nil {
		h.logger.Error("failed to disable semantic route", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "failed to disable semantic route")
		return
	}
	_ = h.auditRepo.Create("admin", "disable_semantic_route", id, "")
	h.reloadRouterRules()
	h.logger.Info("semantic route disabled", zap.String("id", id))
	w.WriteHeader(http.StatusNoContent)
}

// reloadRouterRules 写操作后重新从 DB 加载规则并热更新 SemanticRouter。
func (h *AdminSemanticRouteHandler) reloadRouterRules() {
	if h.semanticRouter == nil {
		return
	}
	rows, err := h.repo.ListAll()
	if err != nil {
		h.logger.Warn("semantic route reload: failed to list routes", zap.Error(err))
		return
	}
	rules := make([]router.RouteRule, 0, len(rows))
	for _, row := range rows {
		if !row.IsActive {
			continue
		}
		urls, err := row.DecodeTargetURLs()
		if err != nil {
			h.logger.Warn("semantic route reload: failed to decode target_urls",
				zap.String("id", row.ID),
				zap.Error(err),
			)
			continue
		}
		rules = append(rules, router.RouteRule{
			ID:          row.ID,
			Name:        row.Name,
			Description: row.Description,
			TargetURLs:  urls,
			Priority:    row.Priority,
			IsActive:    row.IsActive,
		})
	}
	h.semanticRouter.SetRules(rules)
	h.logger.Info("semantic router rules reloaded from DB", zap.Int("active_rules", len(rules)))
}
