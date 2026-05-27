package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/proxy"
)

// SetLLMBindingRepo 设置 LLM 绑定仓库（启用 LLM binding API）。
func (h *AdminHandler) SetLLMBindingRepo(repo *db.LLMBindingRepo) {
	h.llmBindingRepo = repo
}

// SetLLMHealthFn 设置 LLM 健康状态查询函数（启用 /api/admin/llm/targets 端点）。
func (h *AdminHandler) SetLLMHealthFn(fn func() []proxy.LLMTargetStatus) {
	h.llmHealthFn = fn
}

// RegisterLLMRoutes 注册 LLM 相关管理路由（须在 RegisterRoutes 之后调用）。
func (h *AdminHandler) RegisterLLMRoutes(mux *http.ServeMux) {
	w := h.RequireWritableNode
	mux.Handle("GET /api/admin/llm/targets/status", h.RequireAdmin(http.HandlerFunc(h.handleLLMTargets)))
	mux.Handle("GET /api/admin/llm/bindings", h.RequireAdmin(http.HandlerFunc(h.handleListLLMBindings)))
	mux.Handle("POST /api/admin/llm/bindings", h.RequireAdmin(w(http.HandlerFunc(h.handleCreateLLMBinding))))
	mux.Handle("DELETE /api/admin/llm/bindings/{id}", h.RequireAdmin(w(http.HandlerFunc(h.handleDeleteLLMBinding))))
	mux.Handle("POST /api/admin/llm/distribute", h.RequireAdmin(w(http.HandlerFunc(h.handleLLMDistribute))))
}

// ---------------------------------------------------------------------------
// Request / Response 结构体
// ---------------------------------------------------------------------------

type llmTargetResponse struct {
	URL      string `json:"url"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Weight   int    `json:"weight"`
	Healthy  bool   `json:"healthy"`
}

type llmBindingResponse struct {
	ID        string  `json:"id"`
	TargetID  string  `json:"target_id"`
	TargetURL string  `json:"target_url"` // 展示用（通过 JOIN 填充，可能为空）
	UserID    *string `json:"user_id,omitempty"`
	GroupID   *string `json:"group_id,omitempty"`
	CreatedAt string  `json:"created_at"`
}

type createLLMBindingRequest struct {
	TargetID  string  `json:"target_id"`  // 推荐：LLM target UUID
	TargetURL string  `json:"target_url"` // 兼容旧版：通过 URL 查找 target_id
	UserID    *string `json:"user_id,omitempty"`
	GroupID   *string `json:"group_id,omitempty"`
}

type llmDistributeRequest struct {
	UserIDs   []string `json:"user_ids,omitempty"`   // 若为空则使用全体活跃用户
	TargetIDs []string `json:"target_ids,omitempty"` // 推荐：target UUID 列表
	TargetURLs []string `json:"target_urls,omitempty"` // 兼容旧版
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleLLMTargets GET /api/admin/llm/targets/status
// 返回当前已配置的 LLM target 列表及其健康状态。
func (h *AdminHandler) handleLLMTargets(w http.ResponseWriter, r *http.Request) {
	if h.llmHealthFn == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "LLM health status not configured")
		return
	}
	statuses := h.llmHealthFn()
	result := make([]llmTargetResponse, len(statuses))
	for i, s := range statuses {
		result[i] = llmTargetResponse{
			URL:      s.URL,
			Name:     s.Name,
			Provider: s.Provider,
			Weight:   s.Weight,
			Healthy:  s.Healthy,
		}
	}
	h.logger.Debug("listed LLM targets", zap.Int("count", len(result)))
	writeJSON(w, http.StatusOK, result)
}

// handleListLLMBindings GET /api/admin/llm/bindings
func (h *AdminHandler) handleListLLMBindings(w http.ResponseWriter, r *http.Request) {
	if h.llmBindingRepo == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "LLM binding not configured")
		return
	}
	bindings, err := h.llmBindingRepo.List()
	if err != nil {
		h.logger.Error("list LLM bindings failed", zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}
	result := make([]llmBindingResponse, len(bindings))
	for i, b := range bindings {
		result[i] = llmBindingResponse{
			ID:        b.ID,
			TargetID:  b.TargetID,
			TargetURL: b.TargetURL,
			UserID:    b.UserID,
			GroupID:   b.GroupID,
			CreatedAt: b.CreatedAt.Format(time.RFC3339),
		}
	}
	writeJSON(w, http.StatusOK, result)
}

// handleCreateLLMBinding POST /api/admin/llm/bindings
// body: {"target_url":"https://...", "user_id":"u1"} 或 {"target_url":"https://...", "group_id":"g1"}
func (h *AdminHandler) handleCreateLLMBinding(w http.ResponseWriter, r *http.Request) {
	if h.llmBindingRepo == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "LLM binding not configured")
		return
	}
	var req createLLMBindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.TargetID == "" && req.TargetURL == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_field", "target_id or target_url is required")
		return
	}
	if req.UserID == nil && req.GroupID == nil {
		writeJSONError(w, http.StatusBadRequest, "missing_field", "user_id or group_id is required")
		return
	}
	if req.UserID != nil && req.GroupID != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "user_id and group_id are mutually exclusive")
		return
	}

	// 解析 target_id：若未提供，则通过 URL 查找（URL 现为全局唯一）。
	targetID := req.TargetID
	if targetID == "" && h.llmTargetRepo != nil {
		match, err := h.llmTargetRepo.GetByURL(req.TargetURL)
		if err != nil || match == nil {
			writeJSONError(w, http.StatusBadRequest, "target_not_found", "LLM target not found for given target_url")
			return
		}
		targetID = match.ID
	}
	if targetID == "" {
		// 无 llmTargetRepo 时退化到 URL（兼容测试环境）
		targetID = req.TargetURL
	}

	if req.UserID != nil {
		// 用户级绑定：1:1 语义，Set() 会先删除旧绑定再插入新绑定
		if err := h.llmBindingRepo.Set(targetID, req.UserID, nil); err != nil {
			h.logger.Error("create user LLM binding failed", zap.Error(err))
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
			return
		}
	} else {
		// 分组级绑定：1:N 语义，AddGroupBinding() 追加绑定（相同 provider 约束）
		if err := h.llmBindingRepo.AddGroupBinding(targetID, *req.GroupID); err != nil {
			// provider 冲突 / target 不存在 → 400；其余 → 500
			if strings.Contains(err.Error(), "provider conflict") || strings.Contains(err.Error(), "not found") {
				writeJSONError(w, http.StatusBadRequest, "binding_error", err.Error())
			} else {
				h.logger.Error("create group LLM binding failed", zap.Error(err))
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
			}
			return
		}
	}
	h.logger.Info("LLM binding created",
		zap.String("target_id", targetID),
		zap.Any("user_id", req.UserID),
		zap.Any("group_id", req.GroupID),
	)
	// 审计日志
	if detailBytes, jerr := json.Marshal(map[string]interface{}{
		"target_id": targetID,
		"user_id":   req.UserID,
		"group_id":  req.GroupID,
	}); jerr == nil {
		target := ""
		if req.UserID != nil {
			target = *req.UserID
		} else if req.GroupID != nil {
			target = *req.GroupID
		}
		if aerr := h.auditRepo.Create("admin", "llm.bind", target, string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	w.WriteHeader(http.StatusCreated)
}

// handleDeleteLLMBinding DELETE /api/admin/llm/bindings/{id}
func (h *AdminHandler) handleDeleteLLMBinding(w http.ResponseWriter, r *http.Request) {
	if h.llmBindingRepo == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "LLM binding not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_field", "id is required")
		return
	}
	if err := h.llmBindingRepo.Delete(id); err != nil {
		h.logger.Error("delete LLM binding failed", zap.String("id", id), zap.Error(err))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
		return
	}
	h.logger.Info("LLM binding deleted", zap.String("id", id))
	// 审计日志
	if aerr := h.auditRepo.Create("admin", "llm.unbind", id, ""); aerr != nil {
		h.logger.Warn("audit write failed", zap.Error(aerr))
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleLLMDistribute POST /api/admin/llm/distribute
// 将用户均分到 LLM target（轮询分配）。
// body（可选）: {"user_ids":["u1","u2"],"target_urls":["https://..."]}
// 若 user_ids 为空则使用全体活跃用户；若 target_urls 为空则使用 llmHealthFn 返回的所有 target。
func (h *AdminHandler) handleLLMDistribute(w http.ResponseWriter, r *http.Request) {
	if h.llmBindingRepo == nil {
		writeJSONError(w, http.StatusNotImplemented, "not_implemented", "LLM binding not configured")
		return
	}

	var req llmDistributeRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
	}

	// 若 target_ids 为空，尝试从 target_urls 解析（兼容旧版）
	if len(req.TargetIDs) == 0 && len(req.TargetURLs) > 0 && h.llmTargetRepo != nil {
		for _, url := range req.TargetURLs {
			match, err := h.llmTargetRepo.GetByURL(url)
			if err == nil && match != nil {
				req.TargetIDs = append(req.TargetIDs, match.ID)
			}
		}
	}
	// 若 target_ids 仍为空，从 llmHealthFn 获取全部 target
	if len(req.TargetIDs) == 0 && h.llmHealthFn != nil {
		statuses := h.llmHealthFn()
		for _, s := range statuses {
			req.TargetIDs = append(req.TargetIDs, s.ID)
		}
	}
	if len(req.TargetIDs) == 0 {
		writeJSONError(w, http.StatusBadRequest, "no_targets", "no LLM targets configured or provided")
		return
	}

	// 若 user_ids 为空，从 userRepo 获取全体活跃用户
	if len(req.UserIDs) == 0 && h.userRepo != nil {
		users, err := h.userRepo.ListByGroup("")
		if err != nil {
			h.logger.Error("list users failed for distribute", zap.Error(err))
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "an internal error occurred")
			return
		}
		for _, u := range users {
			if u.IsActive {
				req.UserIDs = append(req.UserIDs, u.ID)
			}
		}
	}

	if err := h.llmBindingRepo.EvenDistribute(req.UserIDs, req.TargetIDs); err != nil {
		h.logger.Error("LLM distribute failed", zap.Error(err))
		writeJSONError(w, http.StatusBadRequest, "distribute_error", err.Error())
		return
	}

	h.logger.Info("LLM even distribution completed",
		zap.Int("users", len(req.UserIDs)),
		zap.Int("targets", len(req.TargetIDs)),
	)
	// 审计日志
	if detailBytes, jerr := json.Marshal(map[string]interface{}{
		"user_count":   len(req.UserIDs),
		"target_count": len(req.TargetIDs),
	}); jerr == nil {
		if aerr := h.auditRepo.Create("admin", "llm.distribute", "all_users", string(detailBytes)); aerr != nil {
			h.logger.Warn("audit write failed", zap.Error(aerr))
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"assigned": len(req.UserIDs)})
}
