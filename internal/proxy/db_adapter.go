package proxy

import (
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/keygen"
)

// DBUserLister 将 *db.UserRepo 适配为 ActiveUserLister 接口。
// 负责将 db.User 切片转换为 keygen.UserEntry 切片，解耦 keygen 包与 db 包。
type DBUserLister struct {
	repo *db.UserRepo
}

// NewDBUserLister 创建 DBUserLister 适配器。
func NewDBUserLister(repo *db.UserRepo) *DBUserLister {
	return &DBUserLister{repo: repo}
}

// ListActive 实现 ActiveUserLister 接口。
func (d *DBUserLister) ListActive() ([]keygen.UserEntry, error) {
	users, err := d.repo.ListActive()
	if err != nil {
		return nil, err
	}
	entries := make([]keygen.UserEntry, 0, len(users))
	for _, u := range users {
		entries = append(entries, keygen.UserEntry{
			ID:               u.ID,
			Username:         u.Username,
			PasswordHash:     u.PasswordHash,
			IsActive:         u.IsActive,
			GroupID:          u.GroupID,
			LegacyKeyRevoked: u.LegacyKeyRevoked,
		})
	}
	return entries, nil
}

// IsUserActive 实现 ActiveUserLister 接口，校验单个用户的 is_active 状态。
func (d *DBUserLister) IsUserActive(userID string) (bool, error) {
	user, err := d.repo.GetByID(userID)
	if err != nil {
		return false, err
	}
	if user == nil {
		return false, nil
	}
	return user.IsActive, nil
}
