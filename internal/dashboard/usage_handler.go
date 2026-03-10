package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"go.uber.org/zap"
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

	user, err := h.userRepo.GetByUsername(username)
	if err != nil || user == nil {
		writeDashJSONError(w, http.StatusNotFound, "user not found")
		return
	}

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

	user, err := h.userRepo.GetByUsername(username)
	if err != nil || user == nil {
		writeDashJSONError(w, http.StatusNotFound, "user not found")
		return
	}

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

// writeDashJSONError 写入简单 JSON 错误响应
func writeDashJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
