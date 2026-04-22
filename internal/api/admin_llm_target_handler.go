package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
)

// AdminLLMTargetHandler 处理 LLM target 管理 REST API
type AdminLLMTargetHandler struct {
	logger            *zap.Logger
	jwtMgr            *auth.Manager
	llmTargetRepo     *db.LLMTargetRepo
	auditRepo         *db.AuditRepo
	adminPasswordHash string
	tokenTTL          time.Duration
	limiter           *LoginLimiter
	syncFn            func() // 可选，目标变更后同步 balancer/HC
}

// NewAdminLLMTargetHandler 创建 AdminLLMTargetHandler
func NewAdminLLMTargetHandler(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	llmTargetRepo *db.LLMTargetRepo,
	auditRepo *db.AuditRepo,
	adminPasswordHash string,
	tokenTTL time.Duration,
) *AdminLLMTargetHandler {
	return &AdminLLMTargetHandler{
		logger:            logger.Named("admin_llm_target_handler"),
		jwtMgr:            jwtMgr,
		llmTargetRepo:     llmTargetRepo,
		auditRepo:         auditRepo,
		adminPasswordHash: adminPasswordHash,
		tokenTTL:          tokenTTL,
		limiter:           NewLoginLimiter(5, time.Minute, 5*time.Minute),
	}
}

// SetSyncFn 设置目标变更后的同步回调（可选）。
// 每次 Create/Update/Delete/Enable/Disable 成功后会调用此函数，
// 使 llmBalancer 和 llmHC 立即感知变更，无需重启进程。
func (h *AdminLLMTargetHandler) SetSyncFn(fn func()) { h.syncFn = fn }

// RegisterRoutes 注册路由到 mux
func (h *AdminLLMTargetHandler) RegisterRoutes(mux *http.ServeMux, requireAdmin func(http.Handler) http.Handler, requireWritableNode func(http.Handler) http.Handler) {
	w := requireWritableNode
	mux.Handle("GET /api/admin/llm/targets", requireAdmin(http.HandlerFunc(h.handleListTargets)))
	mux.Handle("POST /api/admin/llm/targets", requireAdmin(w(http.HandlerFunc(h.handleCreateTarget))))
	mux.Handle("GET /api/admin/llm/targets/{id}", requireAdmin(http.HandlerFunc(h.handleGetTarget)))
	mux.Handle("PUT /api/admin/llm/targets/{id}", requireAdmin(w(http.HandlerFunc(h.handleUpdateTarget))))
	mux.Handle("DELETE /api/admin/llm/targets/{id}", requireAdmin(w(http.HandlerFunc(h.handleDeleteTarget))))
	mux.Handle("POST /api/admin/llm/targets/{id}/enable", requireAdmin(w(http.HandlerFunc(h.handleEnableTarget))))
	mux.Handle("POST /api/admin/llm/targets/{id}/disable", requireAdmin(w(http.HandlerFunc(h.handleDisableTarget))))
}

// handleListTargets GET /api/admin/llm/targets - 列出所有 LLM targets
func (h *AdminLLMTargetHandler) handleListTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := h.llmTargetRepo.ListAll()
	if err != nil {
		h.logger.Error("failed to list llm targets", zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	h.logger.Info("listed llm targets", zap.Int("count", len(targets)))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"targets": targets,
	})
}

// handleCreateTarget POST /api/admin/llm/targets - 创建新的 LLM target
func (h *AdminLLMTargetHandler) handleCreateTarget(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL             string            `json:"url"`
		APIKeyID        string            `json:"api_key_id"`
		Provider        string            `json:"provider"`
		Name            string            `json:"name"`
		Weight          int               `json:"weight"`
		HealthCheckPath string            `json:"health_check_path"`
		SupportedModels []string          `json:"supported_models"`
		AutoModel       string            `json:"auto_model"`
		ModelMapping    map[string]string `json:"model_mapping"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 验证必填字段
	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}
	if req.APIKeyID == "" {
		http.Error(w, "api_key_id is required", http.StatusBadRequest)
		return
	}

	// 检查 URL 是否已存在（URL 现为全局唯一）
	exists, err := h.llmTargetRepo.URLExists(req.URL)
	if err != nil {
		h.logger.Error("failed to check url exists", zap.String("url", req.URL), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if exists {
		http.Error(w, "target with this URL already exists", http.StatusConflict)
		return
	}

	// 设置默认值
	if req.Provider == "" {
		req.Provider = "anthropic"
	}
	if req.Weight == 0 {
		req.Weight = 1
	}

	// 转换 supported_models 为 JSON
	supportedModelsJSON := "[]"
	if len(req.SupportedModels) > 0 {
		if b, err := json.Marshal(req.SupportedModels); err == nil {
			supportedModelsJSON = string(b)
		}
	}

	// 转换 model_mapping 为 JSON
	modelMappingJSON := "{}"
	if len(req.ModelMapping) > 0 {
		if b, err := json.Marshal(req.ModelMapping); err == nil {
			modelMappingJSON = string(b)
		}
	}

	// 创建 target
	target := &db.LLMTarget{
		ID:                  uuid.NewString(),
		URL:                 req.URL,
		APIKeyID:            &req.APIKeyID,
		Provider:            req.Provider,
		Name:                req.Name,
		Weight:              req.Weight,
		HealthCheckPath:     req.HealthCheckPath,
		SupportedModelsJSON: supportedModelsJSON,
		ModelMappingJSON:    modelMappingJSON,
		AutoModel:           req.AutoModel,
		Source:              "database",
		IsEditable:          true,
		IsActive:            true,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	if err := h.llmTargetRepo.Create(target); err != nil {
		h.logger.Error("failed to create llm target", zap.String("url", req.URL), zap.Error(err))
		http.Error(w, "Failed to create target", http.StatusInternalServerError)
		return
	}

	// 记录审计日志
	auditDetails := fmt.Sprintf("provider=%s name=%s", req.Provider, req.Name)
	if len(req.SupportedModels) > 0 {
		auditDetails += fmt.Sprintf(" supported_models=%v", req.SupportedModels)
	}
	if req.AutoModel != "" {
		auditDetails += fmt.Sprintf(" auto_model=%s", req.AutoModel)
	}
	if len(req.ModelMapping) > 0 {
		auditDetails += fmt.Sprintf(" model_mapping=%s", modelMappingJSON)
	}
	_ = h.auditRepo.Create("admin", "llm_target.create", req.URL, auditDetails)

	// 标记为未同步（新 target 尚未被运行时加载）
	_ = h.llmTargetRepo.MarkUnsynced(target.ID)

	// 同步 balancer/HC（使新 target 立即参与健康检查）
	if h.syncFn != nil {
		h.syncFn()
	}

	h.logger.Info("llm target created",
		zap.String("id", target.ID),
		zap.String("url", target.URL),
		zap.String("provider", target.Provider),
		zap.Int("supported_models_count", len(req.SupportedModels)))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(target)
}

// handleGetTarget GET /api/admin/llm/targets/:id - 获取单个 LLM target 详情
func (h *AdminLLMTargetHandler) handleGetTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	target, err := h.llmTargetRepo.GetByID(id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			http.Error(w, "Target not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(target)
}

// handleUpdateTarget PUT /api/admin/llm/targets/:id - 更新 LLM target
func (h *AdminLLMTargetHandler) handleUpdateTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	// 查询现有 target
	target, err := h.llmTargetRepo.GetByID(id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			http.Error(w, "Target not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	var req struct {
		Provider        *string           `json:"provider"`
		APIKeyID        *string           `json:"api_key_id"`
		Name            *string           `json:"name"`
		Weight          *int              `json:"weight"`
		HealthCheckPath *string           `json:"health_check_path"`
		SupportedModels []string          `json:"supported_models"`
		AutoModel       *string           `json:"auto_model"`
		ModelMapping    map[string]string `json:"model_mapping"` // nil = no change; empty map = clear
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 记录变更
	changes := []string{}

	if req.Provider != nil && *req.Provider != target.Provider {
		changes = append(changes, fmt.Sprintf("provider: %s→%s", target.Provider, *req.Provider))
		target.Provider = *req.Provider
	}

	if req.APIKeyID != nil {
		oldKeyID := ""
		if target.APIKeyID != nil {
			oldKeyID = *target.APIKeyID
		}
		if *req.APIKeyID != oldKeyID {
			changes = append(changes, fmt.Sprintf("api_key_id: %s→%s", oldKeyID, *req.APIKeyID))
			target.APIKeyID = req.APIKeyID
		}
	}

	if req.Name != nil && *req.Name != target.Name {
		changes = append(changes, fmt.Sprintf("name: %s→%s", target.Name, *req.Name))
		target.Name = *req.Name
	}

	if req.Weight != nil && *req.Weight != target.Weight {
		changes = append(changes, fmt.Sprintf("weight: %d→%d", target.Weight, *req.Weight))
		target.Weight = *req.Weight
	}

	if req.HealthCheckPath != nil && *req.HealthCheckPath != target.HealthCheckPath {
		changes = append(changes, fmt.Sprintf("health_check_path: %s→%s", target.HealthCheckPath, *req.HealthCheckPath))
		target.HealthCheckPath = *req.HealthCheckPath
	}

	// 处理 supported_models（如果提供了值）
	if req.SupportedModels != nil {
		newSupportedModelsJSON := "[]"
		if len(req.SupportedModels) > 0 {
			if b, err := json.Marshal(req.SupportedModels); err == nil {
				newSupportedModelsJSON = string(b)
			}
		}
		if newSupportedModelsJSON != target.SupportedModelsJSON {
			changes = append(changes, fmt.Sprintf("supported_models: %s→%s", target.SupportedModelsJSON, newSupportedModelsJSON))
			target.SupportedModelsJSON = newSupportedModelsJSON
		}
	}

	// 处理 auto_model
	if req.AutoModel != nil && *req.AutoModel != target.AutoModel {
		changes = append(changes, fmt.Sprintf("auto_model: %s→%s", target.AutoModel, *req.AutoModel))
		target.AutoModel = *req.AutoModel
	}

	// 处理 model_mapping（nil 表示不修改；空 map 表示清除）
	if req.ModelMapping != nil {
		newModelMappingJSON := "{}"
		if len(req.ModelMapping) > 0 {
			if b, err := json.Marshal(req.ModelMapping); err == nil {
				newModelMappingJSON = string(b)
			}
		}
		if newModelMappingJSON != target.ModelMappingJSON {
			changes = append(changes, fmt.Sprintf("model_mapping: %s→%s", target.ModelMappingJSON, newModelMappingJSON))
			target.ModelMappingJSON = newModelMappingJSON
		}
	}

	if len(changes) == 0 {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "No changes detected",
			"target":  target,
		})
		return
	}

	// 更新时间戳
	target.UpdatedAt = time.Now()

	// 执行更新
	if err := h.llmTargetRepo.Update(target); err != nil {
		h.logger.Error("failed to update llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Failed to update target", http.StatusInternalServerError)
		return
	}

	// 记录审计日志
	changesSummary := ""
	for i, change := range changes {
		if i > 0 {
			changesSummary += ", "
		}
		changesSummary += change
	}
	_ = h.auditRepo.Create("admin", "llm_target.update", target.URL, changesSummary)

	// 标记为未同步
	_ = h.llmTargetRepo.MarkUnsynced(target.ID)

	// 同步 balancer/HC（使变更立即生效）
	if h.syncFn != nil {
		h.syncFn()
	}

	h.logger.Info("llm target updated",
		zap.String("id", target.ID),
		zap.String("url", target.URL),
		zap.Strings("changes", changes))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Target updated successfully",
		"target":  target,
		"changes": changes,
	})
}

// handleDeleteTarget DELETE /api/admin/llm/targets/:id - 删除 LLM target
func (h *AdminLLMTargetHandler) handleDeleteTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	// 查询现有 target
	target, err := h.llmTargetRepo.GetByID(id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			http.Error(w, "Target not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// 执行删除
	if err := h.llmTargetRepo.Delete(id); err != nil {
		h.logger.Error("failed to delete llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Failed to delete target", http.StatusInternalServerError)
		return
	}

	// 记录审计日志
	_ = h.auditRepo.Create("admin", "llm_target.delete", target.URL,
		fmt.Sprintf("id=%s name=%s", target.ID, target.Name))

	// 同步 balancer/HC（移除已删除的 target）
	if h.syncFn != nil {
		h.syncFn()
	}

	h.logger.Info("llm target deleted",
		zap.String("id", target.ID),
		zap.String("url", target.URL))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Target deleted successfully",
		"id":      id,
	})
}

// handleEnableTarget POST /api/admin/llm/targets/:id/enable - 启用 LLM target
func (h *AdminLLMTargetHandler) handleEnableTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	// 查询现有 target
	target, err := h.llmTargetRepo.GetByID(id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			http.Error(w, "Target not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// 检查当前状态
	if target.IsActive {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "Target is already enabled",
			"target":  target,
		})
		return
	}

	// 更新状态
	target.IsActive = true
	target.UpdatedAt = time.Now()

	if err := h.llmTargetRepo.Update(target); err != nil {
		h.logger.Error("failed to enable llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Failed to enable target", http.StatusInternalServerError)
		return
	}

	// 记录审计日志
	_ = h.auditRepo.Create("admin", "llm_target.enable", target.URL,
		fmt.Sprintf("id=%s name=%s", target.ID, target.Name))

	// 标记为未同步
	_ = h.llmTargetRepo.MarkUnsynced(target.ID)

	// 同步 balancer/HC（将启用的 target 加回轮询）
	if h.syncFn != nil {
		h.syncFn()
	}

	h.logger.Info("llm target enabled",
		zap.String("id", target.ID),
		zap.String("url", target.URL))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Target enabled successfully",
		"target":  target,
	})
}

// handleDisableTarget POST /api/admin/llm/targets/:id/disable - 禁用 LLM target
func (h *AdminLLMTargetHandler) handleDisableTarget(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}

	// 查询现有 target
	target, err := h.llmTargetRepo.GetByID(id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			http.Error(w, "Target not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// 检查当前状态
	if !target.IsActive {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"message": "Target is already disabled",
			"target":  target,
		})
		return
	}

	// 更新状态
	target.IsActive = false
	target.UpdatedAt = time.Now()

	if err := h.llmTargetRepo.Update(target); err != nil {
		h.logger.Error("failed to disable llm target", zap.String("id", id), zap.Error(err))
		http.Error(w, "Failed to disable target", http.StatusInternalServerError)
		return
	}

	// 记录审计日志
	_ = h.auditRepo.Create("admin", "llm_target.disable", target.URL,
		fmt.Sprintf("id=%s name=%s", target.ID, target.Name))

	// 标记为未同步
	_ = h.llmTargetRepo.MarkUnsynced(target.ID)

	// 同步 balancer/HC（将禁用的 target 从轮询中移除）
	if h.syncFn != nil {
		h.syncFn()
	}

	h.logger.Info("llm target disabled",
		zap.String("id", target.ID),
		zap.String("url", target.URL))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Target disabled successfully",
		"target":  target,
	})
}
