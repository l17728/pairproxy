package dashboard

import (
	"net/http"
	neturl "net/url"
	"strconv"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/proxy"
)

// llmPageData LLM 管理页数据
type llmPageData struct {
	baseData
	Targets        []proxy.LLMTargetStatus
	AllTargets     []llmTargetWithMeta // 合并后的目标列表（含 Source/IsEditable）
	Bindings       []db.LLMBinding
	BoundCount     map[string]int    // target URL → 绑定数量
	UserIDToName   map[string]string // user ID → username（用于绑定列表显示）
	GroupIDToName  map[string]string // group ID → group name
	Users          []db.User
	Groups         []db.Group
	APIKeys        []db.APIKey
	DrainStatus    proxy.DrainStatus // 排水状态
	// v2.20 新增：Target Set 支持
	ActiveTab      string                 // targets | targetsets | bindings
	TargetSets     []targetSetWithMembers
	SelectedSetID  string
	GroupsForBind  []db.Group // 未绑定的分组（用于创建目标集时选择）
}

// llmTargetWithMeta 扩展的目标信息（用于 WebUI 显示）
type llmTargetWithMeta struct {
	ID              string
	URL             string
	Provider        string
	Name            string
	Weight          int
	HealthCheckPath string
	APIKeyID        string
	Source          string // "config" | "database"
	IsEditable      bool
	Healthy         bool
	Draining        bool
}

// targetSetWithMembers 目标集及其成员信息
type targetSetWithMembers struct {
	db.GroupTargetSet
	Members        []db.GroupTargetSetMember
	BoundGroupName string
	MemberCount    int
}

// handleLLMPage GET /dashboard/llm
func (h *Handler) handleLLMPage(w http.ResponseWriter, r *http.Request) {
	flash := r.URL.Query().Get("flash")
	errMsg := r.URL.Query().Get("error")
	activeTab := r.URL.Query().Get("tab")
	selectedSetID := r.URL.Query().Get("selected")

	// 默认 Tab 为 targets
	if activeTab == "" {
		activeTab = "targets"
	}

	data := llmPageData{
		baseData:     baseData{Flash: flash, Error: errMsg, IsWorkerNode: h.isWorkerNode},
		ActiveTab:    activeTab,
		SelectedSetID: selectedSetID,
		BoundCount:   make(map[string]int),
	}

	// 获取健康状态（来自 proxy）
	var healthMap = make(map[string]proxy.LLMTargetStatus)
	if h.llmHealthFn != nil {
		data.Targets = h.llmHealthFn()
		for _, t := range data.Targets {
			healthMap[t.URL] = t
		}
	}

	// 获取数据库中的目标列表
	var allTargets []llmTargetWithMeta
	if h.llmTargetRepo != nil {
		dbTargets, err := h.llmTargetRepo.ListAll()
		if err != nil {
			h.logger.Error("list llm targets from db", zap.Error(err))
		} else {
			for _, t := range dbTargets {
				health := healthMap[t.URL]
				apiKeyID := ""
				if t.APIKeyID != nil {
					apiKeyID = *t.APIKeyID
				}
				allTargets = append(allTargets, llmTargetWithMeta{
					ID:              t.ID,
					URL:             t.URL,
					Provider:        t.Provider,
					Name:            t.Name,
					Weight:          t.Weight,
					HealthCheckPath: t.HealthCheckPath,
					APIKeyID:        apiKeyID,
					Source:          t.Source,
					IsEditable:      t.IsEditable,
					Healthy:         health.Healthy,
					Draining:        health.Draining,
				})
			}
		}
	}
	data.AllTargets = allTargets

	// 获取绑定关系
	if h.llmBindingRepo != nil {
		bindings, err := h.llmBindingRepo.List()
		if err != nil {
			h.logger.Error("list llm bindings", zap.Error(err))
		} else {
			data.Bindings = bindings
			for _, b := range bindings {
				data.BoundCount[b.TargetID]++
			}
		}
	}

	// 构建已绑定的 user/group ID 集合（用于过滤添加绑定下拉框）
	boundUserIDs := make(map[string]bool)
	boundGroupIDs := make(map[string]bool)
	for _, b := range data.Bindings {
		if b.UserID != nil {
			boundUserIDs[*b.UserID] = true
		}
		if b.GroupID != nil {
			boundGroupIDs[*b.GroupID] = true
		}
	}

	// 获取用户和分组列表，并构建 ID→名称映射（用于绑定列表显示）
	// Users/Groups 仅保留未绑定的，用于"添加绑定"下拉框
	data.UserIDToName = make(map[string]string)
	data.GroupIDToName = make(map[string]string)
	if h.userRepo != nil {
		allUsers, _ := h.userRepo.ListByGroup("")
		for _, u := range allUsers {
			data.UserIDToName[u.ID] = u.Username
			if !boundUserIDs[u.ID] {
				data.Users = append(data.Users, u)
			}
		}
	}
	if h.groupRepo != nil {
		allGroups, _ := h.groupRepo.List()
		for _, g := range allGroups {
			data.GroupIDToName[g.ID] = g.Name
			if !boundGroupIDs[g.ID] {
				data.Groups = append(data.Groups, g)
			}
		}
	}

	// 获取 API Keys
	if h.apiKeyRepo != nil {
		apiKeys, err := h.apiKeyRepo.List()
		if err != nil {
			h.logger.Error("list api keys", zap.Error(err))
		} else {
			data.APIKeys = apiKeys
		}
	}

	// 获取排水状态
	if h.drainStatusFn != nil {
		data.DrainStatus = h.drainStatusFn()
	}

	// v2.20：加载目标集（如果启用）
	if h.groupTargetSetRepo != nil {
		allSets, err := h.groupTargetSetRepo.ListAll()
		if err != nil {
			h.logger.Error("list target sets", zap.Error(err))
		} else {
			for _, set := range allSets {
				members, err := h.groupTargetSetRepo.ListMembers(set.ID)
				if err != nil {
					h.logger.Error("list target set members", zap.String("setID", set.ID), zap.Error(err))
					members = []db.GroupTargetSetMember{}
				}
				boundGroupName := ""
				if set.GroupID != nil {
					boundGroupName = data.GroupIDToName[*set.GroupID]
				}
				data.TargetSets = append(data.TargetSets, targetSetWithMembers{
					GroupTargetSet: set,
					Members:        members,
					BoundGroupName: boundGroupName,
					MemberCount:    len(members),
				})
			}
		}

		// 为 GroupsForBind 使用未绑定的分组
		if h.groupRepo != nil {
			allGroups, _ := h.groupRepo.List()
			data.GroupsForBind = allGroups
		}
	}

	h.renderPage(w, "llm.html", data)
}

// handleLLMCreateBinding POST /dashboard/llm/bindings
func (h *Handler) handleLLMCreateBinding(w http.ResponseWriter, r *http.Request) {
	if h.llmBindingRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=LLM+binding+not+configured", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/llm?error=invalid+form", http.StatusSeeOther)
		return
	}
	targetURL := r.FormValue("target_url")
	bindType := r.FormValue("bind_type")
	if targetURL == "" {
		http.Redirect(w, r, "/dashboard/llm?error=target_url+required", http.StatusSeeOther)
		return
	}

	// 解析 target URL → target ID
	// 同一 URL 可能有多个 target（Issue #6），此时重定向错误页提示用户用 UUID。
	targetID := targetURL
	if h.llmTargetRepo != nil {
		matches, err := h.llmTargetRepo.ListByURL(targetURL)
		if err == nil && len(matches) == 1 {
			targetID = matches[0].ID
		} else if err == nil && len(matches) > 1 {
			http.Redirect(w, r, "/dashboard/llm?error=target_url_ambiguous", http.StatusSeeOther)
			return
		}
	}

	var userID, groupID *string
	switch bindType {
	case "group":
		gid := r.FormValue("group_id")
		if gid == "" {
			http.Redirect(w, r, "/dashboard/llm?error=group_id+required", http.StatusSeeOther)
			return
		}
		groupID = &gid
	default:
		uid := r.FormValue("user_id")
		if uid == "" {
			http.Redirect(w, r, "/dashboard/llm?error=user_id+required", http.StatusSeeOther)
			return
		}
		userID = &uid
	}

	if err := h.llmBindingRepo.Set(targetID, userID, groupID); err != nil {
		h.logger.Error("create llm binding", zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error="+neturl.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	h.logger.Info("llm binding created via dashboard",
		zap.String("target_id", targetID),
		zap.String("target_url", targetURL),
		zap.Any("user_id", userID),
		zap.Any("group_id", groupID),
	)
	http.Redirect(w, r, "/dashboard/llm?flash=绑定已创建", http.StatusSeeOther)
}

// handleLLMDeleteBinding POST /dashboard/llm/bindings/{id}/delete
func (h *Handler) handleLLMDeleteBinding(w http.ResponseWriter, r *http.Request) {
	if h.llmBindingRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=LLM+binding+not+configured", http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Redirect(w, r, "/dashboard/llm?error=id+required", http.StatusSeeOther)
		return
	}
	if err := h.llmBindingRepo.Delete(id); err != nil {
		h.logger.Error("delete llm binding", zap.String("id", id), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error="+neturl.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	h.logger.Info("llm binding deleted via dashboard", zap.String("id", id))
	http.Redirect(w, r, "/dashboard/llm?flash=绑定已删除", http.StatusSeeOther)
}

// handleLLMDistribute POST /dashboard/llm/distribute
// 均分所有活跃用户到所有已配置 target。
func (h *Handler) handleLLMDistribute(w http.ResponseWriter, r *http.Request) {
	if h.llmBindingRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=LLM+binding+not+configured", http.StatusSeeOther)
		return
	}

	var targetIDs []string
	if h.llmHealthFn != nil {
		for _, s := range h.llmHealthFn() {
			targetIDs = append(targetIDs, s.ID)
		}
	}
	if len(targetIDs) == 0 {
		http.Redirect(w, r, "/dashboard/llm?error=no+LLM+targets+configured", http.StatusSeeOther)
		return
	}

	var userIDs []string
	if h.userRepo != nil {
		users, err := h.userRepo.ListByGroup("")
		if err != nil {
			h.logger.Error("list users for distribute", zap.Error(err))
			http.Redirect(w, r, "/dashboard/llm?error=failed+to+list+users", http.StatusSeeOther)
			return
		}
		for _, u := range users {
			if u.IsActive {
				userIDs = append(userIDs, u.ID)
			}
		}
	}

	if err := h.llmBindingRepo.EvenDistribute(userIDs, targetIDs); err != nil {
		h.logger.Error("llm distribute failed", zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error="+neturl.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	h.logger.Info("llm even distribution via dashboard",
		zap.Int("users", len(userIDs)),
		zap.Int("targets", len(targetIDs)),
	)
	http.Redirect(w, r, "/dashboard/llm?flash="+neturl.QueryEscape("均分完成，共分配"+strconv.Itoa(len(userIDs))+"个用户"), http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// 排水控制
// ---------------------------------------------------------------------------

// handleDrainEnter POST /dashboard/drain/enter
func (h *Handler) handleDrainEnter(w http.ResponseWriter, r *http.Request) {
	if h.drainFn == nil {
		http.Redirect(w, r, "/dashboard/llm?error=排水功能未配置", http.StatusSeeOther)
		return
	}
	if err := h.drainFn(); err != nil {
		h.logger.Error("drain enter failed", zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error="+neturl.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	h.logger.Info("drain mode entered via dashboard")
	http.Redirect(w, r, "/dashboard/llm?flash=已进入排水模式", http.StatusSeeOther)
}

// handleDrainExit POST /dashboard/drain/exit
func (h *Handler) handleDrainExit(w http.ResponseWriter, r *http.Request) {
	if h.undrainFn == nil {
		http.Redirect(w, r, "/dashboard/llm?error=排水功能未配置", http.StatusSeeOther)
		return
	}
	if err := h.undrainFn(); err != nil {
		h.logger.Error("drain exit failed", zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error="+neturl.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	h.logger.Info("drain mode exited via dashboard")
	http.Redirect(w, r, "/dashboard/llm?flash=已退出排水模式", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// LLM 目标管理
// ---------------------------------------------------------------------------

// handleLLMCreateTarget POST /dashboard/llm/targets
func (h *Handler) handleLLMCreateTarget(w http.ResponseWriter, r *http.Request) {
	if h.llmTargetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=LLM+target+management+not+configured", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/llm?error=invalid+form", http.StatusSeeOther)
		return
	}

	targetURL := r.FormValue("url")
	provider := r.FormValue("provider")
	name := r.FormValue("name")
	weightStr := r.FormValue("weight")
	healthCheckPath := r.FormValue("health_check_path")
	apiKeyID := r.FormValue("api_key_id")

	if targetURL == "" || provider == "" {
		http.Redirect(w, r, "/dashboard/llm?error=URL+and+provider+required", http.StatusSeeOther)
		return
	}

	// 检查 URL 冲突（考虑 api_key_id 组合）
	var apiKeyIDPtr *string
	if apiKeyID != "" {
		apiKeyIDPtr = &apiKeyID
	}
	exists, err := h.llmTargetRepo.ComboExists(targetURL, apiKeyIDPtr)
	if err != nil {
		h.logger.Error("failed to check combo exists",
			zap.String("url", targetURL),
			zap.Any("api_key_id", apiKeyIDPtr),
			zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error=internal+error", http.StatusSeeOther)
		return
	}
	if exists {
		h.logger.Warn("rejected duplicate llm target",
			zap.String("url", targetURL),
			zap.Any("api_key_id", apiKeyIDPtr),
		)
		http.Redirect(w, r, "/dashboard/llm?error=URL+already+exists", http.StatusSeeOther)
		return
	}

	weight := 1
	if weightStr != "" {
		if w, err := strconv.Atoi(weightStr); err == nil && w > 0 {
			weight = w
		}
	}

	target := &db.LLMTarget{
		ID:              generateID(),
		URL:             targetURL,
		Provider:        provider,
		Name:            name,
		Weight:          weight,
		HealthCheckPath: healthCheckPath,
		APIKeyID:        apiKeyIDPtr,
		Source:          "database",
		IsEditable:      true,
		IsActive:        true,
	}

	if err := h.llmTargetRepo.Create(target); err != nil {
		h.logger.Error("create llm target", zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error="+neturl.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	// 同步 balancer/HC（使新 target 立即参与健康检查）
	if h.llmSyncFn != nil {
		h.llmSyncFn()
	}

	h.logger.Info("llm target created via dashboard",
		zap.String("url", targetURL),
		zap.String("provider", provider),
	)
	http.Redirect(w, r, "/dashboard/llm?flash=目标已创建", http.StatusSeeOther)
}

// handleLLMUpdateTarget POST /dashboard/llm/targets/{id}/update
func (h *Handler) handleLLMUpdateTarget(w http.ResponseWriter, r *http.Request) {
	if h.llmTargetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=LLM+target+management+not+configured", http.StatusSeeOther)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Redirect(w, r, "/dashboard/llm?error=id+required", http.StatusSeeOther)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/dashboard/llm?error=invalid+form", http.StatusSeeOther)
		return
	}

	// 获取现有目标
	existing, err := h.llmTargetRepo.GetByID(id)
	if err != nil {
		h.logger.Error("get llm target", zap.String("id", id), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error=target+not+found", http.StatusSeeOther)
		return
	}

	// 检查是否可编辑
	if !existing.IsEditable {
		http.Redirect(w, r, "/dashboard/llm?error=config-sourced+targets+cannot+be+edited", http.StatusSeeOther)
		return
	}

	targetURL := r.FormValue("url")
	provider := r.FormValue("provider")
	name := r.FormValue("name")
	weightStr := r.FormValue("weight")
	healthCheckPath := r.FormValue("health_check_path")
	apiKeyID := r.FormValue("api_key_id")

	if targetURL == "" || provider == "" {
		http.Redirect(w, r, "/dashboard/llm?error=URL+and+provider+required", http.StatusSeeOther)
		return
	}

	// 检查URL或APIKeyID变更时的冲突（考虑完整的(url, api_key_id)组合）
	if targetURL != existing.URL || apiKeyID != (func() string {
		if existing.APIKeyID == nil {
			return ""
		}
		return *existing.APIKeyID
	}()) {
		var apiKeyIDPtr *string
		if apiKeyID != "" {
			apiKeyIDPtr = &apiKeyID
		}
		exists, err := h.llmTargetRepo.ComboExists(targetURL, apiKeyIDPtr)
		if err != nil {
			h.logger.Error("failed to check combo exists during update",
				zap.String("id", id),
				zap.String("new_url", targetURL),
				zap.Any("api_key_id", apiKeyIDPtr),
				zap.Error(err))
			http.Redirect(w, r, "/dashboard/llm?error=internal+error", http.StatusSeeOther)
			return
		}
		if exists {
			h.logger.Warn("rejected duplicate llm target during update",
				zap.String("id", id),
				zap.String("new_url", targetURL),
				zap.Any("api_key_id", apiKeyIDPtr),
			)
			http.Redirect(w, r, "/dashboard/llm?error=URL+already+exists", http.StatusSeeOther)
			return
		}
	}

	weight := 1
	if weightStr != "" {
		if w, err := strconv.Atoi(weightStr); err == nil && w > 0 {
			weight = w
		}
	}

	var apiKeyIDPtr *string
	if apiKeyID != "" {
		apiKeyIDPtr = &apiKeyID
	}

	existing.URL = targetURL
	existing.Provider = provider
	existing.Name = name
	existing.Weight = weight
	existing.HealthCheckPath = healthCheckPath
	existing.APIKeyID = apiKeyIDPtr

	if err := h.llmTargetRepo.Update(existing); err != nil {
		h.logger.Error("update llm target", zap.String("id", id), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error="+neturl.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	// 同步 balancer/HC（使变更立即生效）
	if h.llmSyncFn != nil {
		h.llmSyncFn()
	}

	h.logger.Info("llm target updated via dashboard",
		zap.String("id", id),
		zap.String("url", targetURL),
	)
	http.Redirect(w, r, "/dashboard/llm?flash=目标已更新", http.StatusSeeOther)
}

// handleLLMDeleteTarget POST /dashboard/llm/targets/{id}/delete
func (h *Handler) handleLLMDeleteTarget(w http.ResponseWriter, r *http.Request) {
	if h.llmTargetRepo == nil {
		http.Redirect(w, r, "/dashboard/llm?error=LLM+target+management+not+configured", http.StatusSeeOther)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		http.Redirect(w, r, "/dashboard/llm?error=id+required", http.StatusSeeOther)
		return
	}

	// 获取现有目标
	existing, err := h.llmTargetRepo.GetByID(id)
	if err != nil {
		h.logger.Error("get llm target", zap.String("id", id), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error=target+not+found", http.StatusSeeOther)
		return
	}

	// 检查是否可编辑
	if !existing.IsEditable {
		http.Redirect(w, r, "/dashboard/llm?error=config-sourced+targets+cannot+be+deleted", http.StatusSeeOther)
		return
	}

	if err := h.llmTargetRepo.Delete(id); err != nil {
		h.logger.Error("delete llm target", zap.String("id", id), zap.Error(err))
		http.Redirect(w, r, "/dashboard/llm?error="+neturl.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	// 同步 balancer/HC（移除已删除的 target）
	if h.llmSyncFn != nil {
		h.llmSyncFn()
	}

	h.logger.Info("llm target deleted via dashboard", zap.String("id", id))
	http.Redirect(w, r, "/dashboard/llm?flash=目标已删除", http.StatusSeeOther)
}

// generateID 生成唯一 ID
func generateID() string {
	return uuid.NewString()
}
