package db

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// SemanticRouteRepo 语义路由规则数据库操作
type SemanticRouteRepo struct {
	db     *gorm.DB
	logger *zap.Logger
}

// NewSemanticRouteRepo 创建 SemanticRouteRepo
func NewSemanticRouteRepo(db *gorm.DB, logger *zap.Logger) *SemanticRouteRepo {
	return &SemanticRouteRepo{
		db:     db,
		logger: logger.Named("semantic_route_repo"),
	}
}

// ListAll 返回所有语义路由规则（含禁用），按 priority 降序排列
func (r *SemanticRouteRepo) ListAll() ([]SemanticRoute, error) {
	var routes []SemanticRoute
	if err := r.db.Order("priority DESC, created_at ASC").Find(&routes).Error; err != nil {
		r.logger.Error("failed to list semantic routes", zap.Error(err))
		return nil, fmt.Errorf("list semantic routes: %w", err)
	}
	return routes, nil
}

// GetByID 根据 ID 查询规则
func (r *SemanticRouteRepo) GetByID(id string) (*SemanticRoute, error) {
	var route SemanticRoute
	if err := r.db.Where("id = ?", id).First(&route).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, err
		}
		r.logger.Error("failed to get semantic route by id",
			zap.String("id", id), zap.Error(err))
		return nil, fmt.Errorf("get semantic route: %w", err)
	}
	return &route, nil
}

// GetByName 根据 name 查询规则
func (r *SemanticRouteRepo) GetByName(name string) (*SemanticRoute, error) {
	var route SemanticRoute
	if err := r.db.Where("name = ?", name).First(&route).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, err
		}
		r.logger.Error("failed to get semantic route by name",
			zap.String("name", name), zap.Error(err))
		return nil, fmt.Errorf("get semantic route by name: %w", err)
	}
	return &route, nil
}

// Create 新建语义路由规则
func (r *SemanticRouteRepo) Create(name, description string, targetURLs []string, priority int) (*SemanticRoute, error) {
	urlsJSON, err := json.Marshal(targetURLs)
	if err != nil {
		return nil, fmt.Errorf("marshal target urls: %w", err)
	}

	route := &SemanticRoute{
		ID:             uuid.New().String(),
		Name:           name,
		Description:    description,
		TargetURLsJSON: string(urlsJSON),
		Priority:       priority,
		IsActive:       true,
		Source:         "database",
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	if err := r.db.Create(route).Error; err != nil {
		r.logger.Error("failed to create semantic route",
			zap.String("name", name), zap.Error(err))
		return nil, fmt.Errorf("create semantic route: %w", err)
	}

	r.logger.Info("semantic route created",
		zap.String("id", route.ID),
		zap.String("name", name),
		zap.Int("priority", priority))
	return route, nil
}

// Update 更新语义路由规则（部分更新）
func (r *SemanticRouteRepo) Update(id string, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now()
	if err := r.db.Model(&SemanticRoute{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		r.logger.Error("failed to update semantic route",
			zap.String("id", id), zap.Error(err))
		return fmt.Errorf("update semantic route: %w", err)
	}
	r.logger.Info("semantic route updated", zap.String("id", id))
	return nil
}

// Delete 删除语义路由规则
func (r *SemanticRouteRepo) Delete(id string) error {
	if err := r.db.Where("id = ?", id).Delete(&SemanticRoute{}).Error; err != nil {
		r.logger.Error("failed to delete semantic route",
			zap.String("id", id), zap.Error(err))
		return fmt.Errorf("delete semantic route: %w", err)
	}
	r.logger.Info("semantic route deleted", zap.String("id", id))
	return nil
}

// SetActive 启用或禁用语义路由规则
func (r *SemanticRouteRepo) SetActive(id string, active bool) error {
	updates := map[string]interface{}{
		"is_active":  active,
		"updated_at": time.Now(),
	}
	if err := r.db.Model(&SemanticRoute{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		r.logger.Error("failed to set semantic route active state",
			zap.String("id", id), zap.Bool("active", active), zap.Error(err))
		return fmt.Errorf("set semantic route active: %w", err)
	}
	r.logger.Info("semantic route active state changed",
		zap.String("id", id), zap.Bool("active", active))
	return nil
}

// TargetURLs 从 SemanticRoute 解析 TargetURLsJSON 为 []string（向后兼容别名）
func (sr *SemanticRoute) TargetURLs() []string {
	urls, _ := sr.DecodeTargetURLs()
	return urls
}

// DecodeTargetURLs 从 SemanticRoute 解析 TargetURLsJSON 为 []string，返回错误信息
func (sr *SemanticRoute) DecodeTargetURLs() ([]string, error) {
	var urls []string
	if err := json.Unmarshal([]byte(sr.TargetURLsJSON), &urls); err != nil {
		return nil, fmt.Errorf("decode target_urls: %w", err)
	}
	return urls, nil
}
