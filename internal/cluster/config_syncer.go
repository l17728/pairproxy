package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/l17728/pairproxy/internal/db"
)

const (
	defaultSyncInterval    = 30 * time.Second
	configSnapshotPath     = "/api/internal/config-snapshot"
	configSyncHTTPTimeout  = 15 * time.Second
)

// ConfigSyncerConfig 配置同步器的启动配置。
type ConfigSyncerConfig struct {
	PrimaryAddr  string        // Primary 节点地址，如 "http://sp-1:9000"
	SharedSecret string        // 内部 API 共享密钥（Bearer token）
	Interval     time.Duration // 拉取间隔，0 使用默认值（30s）
}

// ConfigSyncer 运行在 Worker 节点上，定期从 Primary 拉取配置快照并同步到本地 DB。
//
// 解决的问题：
//   - P0-1：用户禁用传播（IsActive=false 30s 内同步至 Worker）
//   - P0-2：刷新 Token 撤销（禁用用户的 refresh_tokens 被删除）
//   - P1-1：Primary 创建的用户同步至 Worker，Worker 可正常处理这些用户的请求
//   - P1-2：保留 Primary 的 user_id，避免 usage_logs 归属错乱
//   - P1-3：分组配额变更同步至 Worker
//   - P1-4：LLM 绑定关系同步至 Worker
//   - P1-5：动态添加的 LLM Target 同步至 Worker
type ConfigSyncer struct {
	logger       *zap.Logger
	primaryAddr  string
	sharedSecret string
	interval     time.Duration
	client       *http.Client

	// 底层数据库连接（用于事务 upsert）
	database *gorm.DB

	// 指向本地 repos（用于 Primary 不可达时读取本地缓存）
	userRepo       *db.UserRepo
	groupRepo      *db.GroupRepo
	llmTargetRepo  *db.LLMTargetRepo
	llmBindingRepo *db.LLMBindingRepo

	// 可观测性指标（原子操作，供 /metrics 读取）
	pullFailures atomic.Int64 // 拉取失败累计次数
	lastSyncAt   atomic.Int64 // 最近一次成功同步的 Unix timestamp（秒）

	done chan struct{} // closed when loop exits
}

// NewConfigSyncer 创建 ConfigSyncer。
func NewConfigSyncer(
	logger *zap.Logger,
	cfg ConfigSyncerConfig,
	database *gorm.DB,
	userRepo *db.UserRepo,
	groupRepo *db.GroupRepo,
	llmTargetRepo *db.LLMTargetRepo,
	llmBindingRepo *db.LLMBindingRepo,
) *ConfigSyncer {
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultSyncInterval
	}
	s := &ConfigSyncer{
		logger:         logger.Named("config_syncer"),
		primaryAddr:    cfg.PrimaryAddr,
		sharedSecret:   cfg.SharedSecret,
		interval:       interval,
		client:         &http.Client{Timeout: configSyncHTTPTimeout},
		database:       database,
		userRepo:       userRepo,
		groupRepo:      groupRepo,
		llmTargetRepo:  llmTargetRepo,
		llmBindingRepo: llmBindingRepo,
		done:           make(chan struct{}),
	}
	return s
}

// Start 启动后台配置同步 goroutine。
// 启动时立即执行一次同步，之后按 interval 周期执行。
func (s *ConfigSyncer) Start(ctx context.Context) {
	go func() {
		s.loop(ctx)
		close(s.done)
	}()
}

// Wait 阻塞直到后台 goroutine 退出（用于 graceful shutdown）。
func (s *ConfigSyncer) Wait() {
	<-s.done
}

// PullFailures 返回累计拉取失败次数。
func (s *ConfigSyncer) PullFailures() int64 { return s.pullFailures.Load() }

// LastSyncAt 返回最近一次成功同步的 Unix timestamp（秒），0 表示从未成功。
func (s *ConfigSyncer) LastSyncAt() int64 { return s.lastSyncAt.Load() }

func (s *ConfigSyncer) loop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// 启动时立即执行一次同步
	s.pull(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.pull(ctx)
		}
	}
}

// pull 从 Primary 拉取配置快照并同步到本地 DB。
// 拉取失败时记录 WARN 日志（不影响 Worker 继续服务），下次周期重试。
func (s *ConfigSyncer) pull(ctx context.Context) {
	url := s.primaryAddr + configSnapshotPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		s.logger.Error("config sync: failed to create request",
			zap.String("primary", s.primaryAddr),
			zap.Error(err),
		)
		s.pullFailures.Add(1)
		return
	}
	if s.sharedSecret != "" {
		req.Header.Set("Authorization", "Bearer "+s.sharedSecret)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		s.logger.Warn("config sync: failed to reach primary (will retry next cycle)",
			zap.String("primary", s.primaryAddr),
			zap.Int64("total_fails", s.pullFailures.Load()+1),
			zap.Error(err),
		)
		s.pullFailures.Add(1)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		s.logger.Warn("config sync: primary returned non-200",
			zap.String("primary", s.primaryAddr),
			zap.Int("status", resp.StatusCode),
		)
		s.pullFailures.Add(1)
		return
	}

	var snap ConfigSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		s.logger.Error("config sync: failed to decode snapshot",
			zap.String("primary", s.primaryAddr),
			zap.Error(err),
		)
		s.pullFailures.Add(1)
		return
	}

	if err := s.upsertSnapshot(snap); err != nil {
		s.logger.Error("config sync: failed to upsert snapshot to local DB",
			zap.String("primary", s.primaryAddr),
			zap.Error(err),
		)
		s.pullFailures.Add(1)
		return
	}

	s.lastSyncAt.Store(time.Now().Unix())
	s.logger.Info("config sync: snapshot applied successfully",
		zap.String("primary", s.primaryAddr),
		zap.Time("snapshot_version", snap.Version),
		zap.Int("users", len(snap.Users)),
		zap.Int("groups", len(snap.Groups)),
		zap.Int("targets", len(snap.LLMTargets)),
		zap.Int("bindings", len(snap.LLMBindings)),
	)
}

// upsertSnapshot 在单个事务中将快照数据 upsert 到本地 DB。
//
// 策略：
//   - users/groups/llm_targets：按 primary key upsert（ON CONFLICT DO UPDATE），保留 Primary 的 ID
//   - llm_bindings：按 ID upsert
//   - 禁用用户（IsActive=false）的 refresh_tokens 被删除（P0-2 Token 撤销）
//
// 注意：GORM 的 bool 零值陷阱（IsActive=false, IsEditable=false）通过
// clause.OnConflict DoUpdates 显式列名列表解决，确保 false 值正确写入。
func (s *ConfigSyncer) upsertSnapshot(snap ConfigSnapshot) error {
	return s.database.Transaction(func(tx *gorm.DB) error {
		// --- 1. Upsert Groups ---
		if len(snap.Groups) > 0 {
			if err := tx.Select("*").Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"name", "daily_token_limit", "monthly_token_limit",
					"requests_per_minute", "max_tokens_per_request", "concurrent_requests",
				}),
			}).CreateInBatches(snap.Groups, 200).Error; err != nil {
				return fmt.Errorf("upsert groups: %w", err)
			}
			s.logger.Debug("config sync: groups upserted", zap.Int("count", len(snap.Groups)))
		}

		// --- 2. Upsert Users ---
		// 使用 ON CONFLICT(id) DO UPDATE 保留 Primary 的 user_id，
		// 确保 Worker 推送的 usage_logs 中 user_id 与 Primary 一致（P1-2 修复）。
		if len(snap.Users) > 0 {
			// 移除 Group 关联对象，避免 GORM 尝试 upsert 关联表
			usersOnly := make([]db.User, len(snap.Users))
			for i, u := range snap.Users {
				u.Group = db.Group{} // 清除关联，只保留 GroupID FK
				usersOnly[i] = u
			}
			// Select("*") 强制 GORM 包含所有字段（含 false 的 bool 字段），
			// 避免 is_active=false 的零值被 GORM 跳过（default:true 陷阱）。
			if err := tx.Select("*").Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"username", "password_hash", "group_id", "is_active",
					"auth_provider", "external_id", "last_login_at",
				}),
			}).CreateInBatches(usersOnly, 200).Error; err != nil {
				return fmt.Errorf("upsert users: %w", err)
			}

			// 显式同步 is_active：GORM 的 default:true 标签可能导致 CreateInBatches
			// 对已存在的记录不正确更新 is_active=false。使用两组批量 UPDATE 修正。
			var activeIDs, disabledIDs []string
			for _, u := range snap.Users {
				if u.IsActive {
					activeIDs = append(activeIDs, u.ID)
				} else {
					disabledIDs = append(disabledIDs, u.ID)
				}
			}
			if len(activeIDs) > 0 {
				if err := tx.Model(&db.User{}).Where("id IN ?", activeIDs).Update("is_active", true).Error; err != nil {
					return fmt.Errorf("activate users: %w", err)
				}
			}
			if len(disabledIDs) > 0 {
				if err := tx.Model(&db.User{}).Where("id IN ?", disabledIDs).Update("is_active", false).Error; err != nil {
					return fmt.Errorf("deactivate users: %w", err)
				}
			}
			s.logger.Debug("config sync: users upserted", zap.Int("count", len(snap.Users)))

			// P0-2：删除已禁用用户的 refresh_tokens，使其无法刷新 JWT
			if len(disabledIDs) > 0 {
				if err := tx.Where("user_id IN ?", disabledIDs).Delete(&db.RefreshToken{}).Error; err != nil {
					s.logger.Warn("config sync: failed to revoke refresh tokens for disabled users",
						zap.Strings("disabled_user_ids", disabledIDs),
						zap.Error(err),
					)
					// 非致命错误：继续同步，refresh_token TTL 自然过期
				} else {
					s.logger.Info("config sync: revoked refresh tokens for disabled users",
						zap.Int("disabled_count", len(disabledIDs)),
					)
				}
			}
		}

		// --- 3. Upsert LLM Targets ---
		// IsActive 和 IsEditable 均为 bool，显式包含在 DoUpdates 中避免零值丢失。
		// 先按 URL 删除本地与 Primary ID 不同的旧记录，避免 SQLite 更新主键时触发 UNIQUE 冲突。
		if len(snap.LLMTargets) > 0 {
			targets := make([]*db.LLMTarget, len(snap.LLMTargets))
			copy(targets, snap.LLMTargets)
			for _, t := range targets {
				if err := tx.Where("url = ? AND id != ?", t.URL, t.ID).Delete(&db.LLMTarget{}).Error; err != nil {
					return fmt.Errorf("delete stale llm target by url: %w", err)
				}
			}
			if err := tx.Select("*").Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"url", "provider", "name", "weight",
					"health_check_path", "model_mapping", "source",
					"is_editable", "is_active", "updated_at",
				}),
			}).CreateInBatches(targets, 100).Error; err != nil {
				return fmt.Errorf("upsert llm targets: %w", err)
			}
			s.logger.Debug("config sync: llm targets upserted", zap.Int("count", len(snap.LLMTargets)))
		}

		// --- 4. Upsert LLM Bindings ---
		if len(snap.LLMBindings) > 0 {
			if err := tx.Select("*").Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"target_id", "user_id", "group_id",
				}),
			}).CreateInBatches(snap.LLMBindings, 200).Error; err != nil {
				return fmt.Errorf("upsert llm bindings: %w", err)
			}
			s.logger.Debug("config sync: llm bindings upserted", zap.Int("count", len(snap.LLMBindings)))
		}

		return nil
	})
}
