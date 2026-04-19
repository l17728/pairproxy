package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// dashboardQuotaResponse 配额状态响应（含 remain 字段，供 WebUI 直接使用）
type dashboardQuotaResponse struct {
	DailyLimit    int64 `json:"daily_limit"`    // 0 = 不限
	DailyUsed     int64 `json:"daily_used"`
	DailyRemain   int64 `json:"daily_remain"`   // -1 = 不限
	MonthlyLimit  int64 `json:"monthly_limit"`  // 0 = 不限
	MonthlyUsed   int64 `json:"monthly_used"`
	MonthlyRemain int64 `json:"monthly_remain"` // -1 = 不限
	RPMLimit      int   `json:"rpm_limit"`       // 0 = 不限
}

// handleDashboardActiveUsers GET /api/dashboard/active-users?days=N
// 使用 session 认证，供 my-usage 页面加载用户下拉列表。
func (h *Handler) handleDashboardActiveUsers(w http.ResponseWriter, r *http.Request) {
	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 {
		days = d
	}

	users, err := h.userRepo.GetActiveUsers(days)
	if err != nil {
		h.logger.Error("dashboard: failed to get active users", zap.Error(err))
		writeDashJSONError(w, http.StatusInternalServerError, "failed to get active users")
		return
	}

	type activeUser struct {
		ID        string  `json:"id"`
		Username  string  `json:"username"`
		GroupID   *string `json:"group_id,omitempty"`
		GroupName *string `json:"group_name,omitempty"`
	}

	resp := make([]activeUser, 0, len(users))
	for _, u := range users {
		au := activeUser{
			ID:       u.ID,
			Username: u.Username,
			GroupID:  u.GroupID,
		}
		if u.GroupID != nil && u.Group.Name != "" {
			name := u.Group.Name
			au.GroupName = &name
		}
		resp = append(resp, au)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Error("dashboard: encode active users response", zap.Error(err))
	}
}

// handleDashboardUserQuota GET /api/dashboard/user-quota?username=xxx
// 使用 session 认证，返回指定用户的配额和用量（含 remain 字段）。
func (h *Handler) handleDashboardUserQuota(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		writeDashJSONError(w, http.StatusBadRequest, "username is required")
		return
	}

	users, err := h.userRepo.ListByUsername(username)
	if err != nil || len(users) == 0 {
		writeDashJSONError(w, http.StatusNotFound, "user not found")
		return
	}
	if len(users) > 1 {
		writeDashJSONError(w, http.StatusConflict, "username matches multiple users; use user_id instead")
		return
	}
	user := &users[0]

	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	dailyIn, dailyOut, _ := h.usageRepo.SumTokens(user.ID, todayStart, now)
	monthlyIn, monthlyOut, _ := h.usageRepo.SumTokens(user.ID, monthStart, now)

	resp := dashboardQuotaResponse{
		DailyUsed:   dailyIn + dailyOut,
		MonthlyUsed: monthlyIn + monthlyOut,
	}

	if user.GroupID != nil {
		if group, err := h.groupRepo.GetByID(*user.GroupID); err == nil {
			if group.DailyTokenLimit != nil {
				resp.DailyLimit = *group.DailyTokenLimit
			}
			if group.MonthlyTokenLimit != nil {
				resp.MonthlyLimit = *group.MonthlyTokenLimit
			}
			if group.RequestsPerMinute != nil {
				resp.RPMLimit = *group.RequestsPerMinute
			}
		}
	}

	// 计算剩余量（limit=0 表示不限 → remain=-1）
	if resp.DailyLimit > 0 {
		resp.DailyRemain = resp.DailyLimit - resp.DailyUsed
	} else {
		resp.DailyRemain = -1
	}
	if resp.MonthlyLimit > 0 {
		resp.MonthlyRemain = resp.MonthlyLimit - resp.MonthlyUsed
	} else {
		resp.MonthlyRemain = -1
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		h.logger.Error("dashboard: encode user quota response", zap.Error(err))
	}
}

// handleDashboardUserHistory GET /api/dashboard/user-history?username=xxx&days=N
// 使用 session 认证，返回指定用户的每日用量历史。
func (h *Handler) handleDashboardUserHistory(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		writeDashJSONError(w, http.StatusBadRequest, "username is required")
		return
	}

	days := 30
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 365 {
		days = d
	}

	users2, err2 := h.userRepo.ListByUsername(username)
	if err2 != nil || len(users2) == 0 {
		writeDashJSONError(w, http.StatusNotFound, "user not found")
		return
	}
	if len(users2) > 1 {
		writeDashJSONError(w, http.StatusConflict, "username matches multiple users; use user_id instead")
		return
	}
	user := &users2[0]

	now := time.Now()
	from := now.AddDate(0, 0, -days)

	rows, err := h.usageRepo.DailyTokens(from, now, user.ID)
	if err != nil {
		h.logger.Error("dashboard: failed to get usage history", zap.Error(err))
		writeDashJSONError(w, http.StatusInternalServerError, "failed to get usage history")
		return
	}

	type historyResp struct {
		History interface{} `json:"history"`
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(historyResp{History: rows}); err != nil {
		h.logger.Error("dashboard: encode user history response", zap.Error(err))
	}
}

// handleDashboardUserLogs GET /api/dashboard/user-logs?username=xxx&days=7&page=1&page_size=10
// 返回指定用户最近 N 天的请求日志（分页）。
func (h *Handler) handleDashboardUserLogs(w http.ResponseWriter, r *http.Request) {
	username := r.URL.Query().Get("username")
	if username == "" {
		writeDashJSONError(w, http.StatusBadRequest, "username is required")
		return
	}

	days := 7
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 365 {
		days = d
	}

	pageSize := 10
	if ps, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil {
		switch ps {
		case 10, 20, 50:
			pageSize = ps
		}
	}

	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}

	users, err := h.userRepo.ListByUsername(username)
	if err != nil || len(users) == 0 {
		writeDashJSONError(w, http.StatusNotFound, "user not found")
		return
	}
	if len(users) > 1 {
		writeDashJSONError(w, http.StatusConflict, "username matches multiple users; use user_id instead")
		return
	}
	user := &users[0]

	now := time.Now()
	from := now.AddDate(0, 0, -days)

	filter := db.UsageFilter{
		UserID: user.ID,
		From:   &from,
		To:     &now,
	}

	total, err := h.usageRepo.CountLogs(filter)
	if err != nil {
		h.logger.Error("dashboard: failed to count user logs", zap.Error(err))
		writeDashJSONError(w, http.StatusInternalServerError, "failed to count logs")
		return
	}

	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}

	filter.Limit = pageSize
	filter.Offset = (page - 1) * pageSize

	logs, err := h.usageRepo.Query(filter)
	if err != nil {
		h.logger.Error("dashboard: failed to query user logs", zap.Error(err))
		writeDashJSONError(w, http.StatusInternalServerError, "failed to query logs")
		return
	}

	type logEntry struct {
		CreatedAt    string  `json:"created_at"`
		Model        string  `json:"model"`
		ActualModel  string  `json:"actual_model"`
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		CostUSD      float64 `json:"cost_usd"`
		StatusCode   int     `json:"status_code"`
		IsStreaming  bool    `json:"is_streaming"`
		DurationMs   int64   `json:"duration_ms"`
	}

	entries := make([]logEntry, 0, len(logs))
	for _, l := range logs {
		entries = append(entries, logEntry{
			CreatedAt:    l.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			Model:        l.Model,
			ActualModel:  l.ActualModel,
			InputTokens:  l.InputTokens,
			OutputTokens: l.OutputTokens,
			CostUSD:      l.CostUSD,
			StatusCode:   l.StatusCode,
			IsStreaming:  l.IsStreaming,
			DurationMs:   l.DurationMs,
		})
	}

	type resp struct {
		Logs       []logEntry `json:"logs"`
		Total      int64      `json:"total"`
		Page       int        `json:"page"`
		PageSize   int        `json:"page_size"`
		TotalPages int        `json:"total_pages"`
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp{
		Logs:       entries,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}); err != nil {
		h.logger.Error("dashboard: encode user logs response", zap.Error(err))
	}
}

// writeDashJSONError 写入简单 JSON 错误响应
func writeDashJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
