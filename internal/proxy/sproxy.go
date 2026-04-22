package proxy

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/corpus"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/l17728/pairproxy/internal/metrics"
	"github.com/l17728/pairproxy/internal/quota"
	"github.com/l17728/pairproxy/internal/router"
	"github.com/l17728/pairproxy/internal/tap"
	"github.com/l17728/pairproxy/internal/track"
	"github.com/l17728/pairproxy/internal/version"
)

// ErrNoLLMBinding 在未分配 LLM 目标时返回。
// 在管理员显式配置分配关系之前，请求将被拒绝。
var ErrNoLLMBinding = errors.New("no LLM target assigned for this user/group")

// ErrBoundTargetUnavailable 在已绑定的 LLM 目标不可用时返回。
// 目标处于 unhealthy 状态或已完成重试时触发。
var ErrBoundTargetUnavailable = errors.New("assigned LLM target is currently unavailable")

// LLMTarget 代表一个 LLM 后端（含 API Key 和 provider 类型）。
type LLMTarget struct {
	ID           string // UUID（来自 DB，运行时路由标识）
	URL          string
	APIKey       string
	Provider     string            // "anthropic"（默认）| "openai" | "ollama"
	Name         string            // 可选显示名，空则用 URL
	Weight       int               // 负载均衡权重（≥1）
	ModelMapping map[string]string // Anthropic→Ollama 模型名映射（可选）
}

// LLMTargetStatus 向 Admin/Dashboard 暴露的 LLM 目标运行时状态。
type LLMTargetStatus struct {
	ID       string
	URL      string
	Name     string
	Provider string
	Weight   int
	Healthy  bool
	Draining bool // 是否处于排水模式
}

// SProxy s-proxy 核心处理器
type SProxy struct {
	logger         *zap.Logger
	jwtMgr         *auth.Manager
	writer         *db.UsageWriter
	targets        []LLMTarget
	idx            atomic.Uint32 // 轮询计数器（无 LLM 均衡器时使用）
	transport      http.RoundTripper
	clusterMgr     *cluster.Manager                                // 可选，nil 表示单节点模式（不注入路由头）
	sourceNode     string                                          // 来源节点标识（用于 usage_logs）
	quotaChecker   *quota.Checker                                  // 可选，nil 表示不检查配额
	startTime      time.Time                                       // 进程启动时间（供 /health 返回 uptime）
	activeRequests atomic.Int64                                    // 当前正在处理的代理请求数
	sqlDB          *sql.DB                                         // 可选，用于 /health 检查 DB 可达性
	apiKeyResolver func(userID, groupID string) (apiKey string, found bool) // 可选，动态 API Key 解析

	// 排水模式控制
	draining     atomic.Bool // 排水模式标志
	drainReason  string      // 排水原因（用于日志和状态查询）
	drainStarted time.Time   // 排水开始时间

	// LLM 均衡 + 绑定（可选）
	llmBalancer     *lb.WeightedRandomBalancer                  // 加权随机负载均衡
	llmHC           *lb.HealthChecker                           // 健康检查（被动熔断 + 自动恢复）
	bindingResolver func(userID, groupID string) (string, bool) // 用户/分组 → target URL
	maxRetries      int                                         // RetryTransport 最大重试次数
	retryOnStatus   []int                                       // 额外触发 try-next 的 HTTP 状态码（如 [429]）

	debugLogger    atomic.Pointer[zap.Logger]    // 可选，非 nil 时将转发内容写入独立 debug 文件
	notifier       *alert.Notifier               // 可选，非 nil 时发送 high_load/load_recovered 告警
	convTracker    atomic.Pointer[track.Tracker] // 可选，非 nil 时记录指定用户对话内容
	corpusWriter   atomic.Pointer[corpus.Writer] // 可选，非 nil 时采集训练语料
	semanticRouter *router.SemanticRouter        // 可选，非 nil 时对无绑定请求做语义路由

	// 配置和数据库（用于 config target sync）
	cfg           *config.SProxyFullConfig     // 可选，用于同步配置文件中的 LLM targets
	db            *gorm.DB                     // 可选，用于同步配置文件中的 LLM targets
	keyDecryptFn  func(string) (string, error) // 可选，当配置了 key_encryption_key 时用于解密 AES key
}

// NewSProxy 创建 SProxy。
// targets 至少需要一个 LLM 后端。
func NewSProxy(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	writer *db.UsageWriter,
	targets []LLMTarget,
) (*SProxy, error) {
	return newSProxy(logger, jwtMgr, writer, targets, nil, "local")
}

// NewSProxyWithCluster 创建带集群管理器的 SProxy（sp-1 模式）。
func NewSProxyWithCluster(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	writer *db.UsageWriter,
	targets []LLMTarget,
	clusterMgr *cluster.Manager,
	sourceNode string,
) (*SProxy, error) {
	return newSProxy(logger, jwtMgr, writer, targets, clusterMgr, sourceNode)
}

func newSProxy(
	logger *zap.Logger,
	jwtMgr *auth.Manager,
	writer *db.UsageWriter,
	targets []LLMTarget,
	clusterMgr *cluster.Manager,
	sourceNode string,
) (*SProxy, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one LLM target is required")
	}
	sp := &SProxy{
		logger:     logger.Named("sproxy"),
		jwtMgr:     jwtMgr,
		writer:     writer,
		targets:    targets,
		transport:  http.DefaultTransport,
		clusterMgr: clusterMgr,
		sourceNode: sourceNode,
		startTime:  time.Now(),
		maxRetries: 2,
	}
	return sp, nil
}

// SetQuotaChecker 设置配额检查器（可选；设置后每次请求前检查配额）。
func (sp *SProxy) SetQuotaChecker(checker *quota.Checker) {
	sp.quotaChecker = checker
}

// SetDB 设置数据库连接供健康检查使用（可选）。
// 健康检查时会通过 PingContext 验证数据库可达性。
func (sp *SProxy) SetDB(gormDB interface{ DB() (*sql.DB, error) }) {
	if sqlDB, err := gormDB.DB(); err == nil {
		sp.sqlDB = sqlDB
		sp.logger.Debug("health check: database connection set for ping")
	} else {
		sp.logger.Warn("health check: failed to get underlying sql.DB", zap.Error(err))
	}
}

// SetAPIKeyResolver 设置动态 API Key 解析器（可选）。
// fn 根据 userID 和 groupID 返回解密后的 API Key；found=false 时回退到配置文件中的静态 Key。
// groupID 直接来自 JWT claims，无需再查询 UserRepo。
func (sp *SProxy) SetAPIKeyResolver(fn func(userID, groupID string) (string, bool)) {
	sp.apiKeyResolver = fn
}

// SetKeyDecryptFn 设置 AES 密钥解密函数（BUG-4 修复）。
// 当配置了 admin.key_encryption_key 时，resolveAPIKey 优先使用此函数解密 AES 密文；
// 未设置时退回到 obfuscateKey（兼容 config-sync 路径）。
func (sp *SProxy) SetKeyDecryptFn(fn func(string) (string, error)) {
	sp.keyDecryptFn = fn
}

// SetLLMHealthChecker 设置 LLM 负载均衡器和健康检查器（可选）。
// 设置后启用基于健康状态的加权随机路由和被动熔断；不设置则退化为简单轮询。
func (sp *SProxy) SetLLMHealthChecker(bal *lb.WeightedRandomBalancer, hc *lb.HealthChecker) {
	sp.llmBalancer = bal
	sp.llmHC = hc
}

// SetBindingResolver 设置用户/分组 LLM 绑定解析器（可选）。
// fn 根据 userID + groupID 返回绑定的 target URL；未绑定时 found=false，请求将被拒绝（403）。
// 未设置 bindingResolver 时（如单元测试），回退到负载均衡自动选取。
func (sp *SProxy) SetBindingResolver(fn func(userID, groupID string) (string, bool)) {
	sp.bindingResolver = fn
}

// SetMaxRetries 设置 RetryTransport 的最大重试次数（默认 2）。
func (sp *SProxy) SetMaxRetries(n int) {
	sp.maxRetries = n
}

// SetRetryOnStatus 设置触发 try-next 的额外 HTTP 状态码列表（如 []int{429}）。
// 空列表（默认）表示仅对 5xx 和连接错误重试，行为与旧版本完全一致。
func (sp *SProxy) SetRetryOnStatus(codes []int) {
	sp.retryOnStatus = codes
}

// SetTransport 设置底层 HTTP transport（测试用；默认 http.DefaultTransport）。
func (sp *SProxy) SetTransport(t http.RoundTripper) {
	sp.transport = t
}

// SetDebugLogger 设置 debug 文件日志器。
// 非 nil 时，每个请求的转发内容（请求体、响应体、SSE chunks）均会写入该 logger。
func (sp *SProxy) SetDebugLogger(l *zap.Logger) {
	sp.debugLogger.Store(l)
}

// SetSemanticRouter 设置语义路由器（可选）。
// 非 nil 时，对无显式 LLM 绑定的请求（LB 路径）进行语义分类，缩窄候选 target 池。
func (sp *SProxy) SetSemanticRouter(r *router.SemanticRouter) {
	sp.semanticRouter = r
}

// SyncAndSetDebugLogger 先 Sync 旧 logger（flush 缓冲区），再原子切换为新 logger。
// 供 SIGHUP 热重载时调用；传入 nil 表示关闭 debug 日志。
func (sp *SProxy) SyncAndSetDebugLogger(l *zap.Logger) {
	if old := sp.debugLogger.Load(); old != nil {
		_ = old.Sync()
	}
	sp.debugLogger.Store(l)
}

// SetConvTracker 设置用户对话内容跟踪器。
// 非 nil 时，对已启用跟踪的用户，每次请求的输入消息和 LLM 回复均会写入文件。
func (sp *SProxy) SetConvTracker(t *track.Tracker) {
	sp.convTracker.Store(t)
}

// SetCorpusWriter 设置训练语料采集写入器。
// 非 nil 时，每次代理请求的输入消息和 LLM 回复均会异步写入 JSONL 语料文件。
func (sp *SProxy) SetCorpusWriter(w *corpus.Writer) {
	sp.corpusWriter.Store(w)
}

// SetConfigAndDB 设置配置和数据库（用于 config target sync）。
func (sp *SProxy) SetConfigAndDB(cfg *config.SProxyFullConfig, gormDB *gorm.DB) {
	sp.cfg = cfg
	sp.db = gormDB
}

// resolveAPIKeyID 解析 API Key 字符串为 API Key ID。
// 按 (provider, encrypted_value) 去重：相同 key 值复用已有记录，不同 key 值创建新记录。
// targetURL 用于生成唯一的 Name，避免 uniqueIndex 冲突。
// 如果 API Key 不存在，创建新记录。
func (sp *SProxy) resolveAPIKeyID(apiKey, provider, targetURL string) (*string, error) {
	if apiKey == "" {
		return nil, nil // API Key 可选
	}

	obfuscated := obfuscateKey(apiKey)

	// 查询是否已存在（按 provider + encrypted_value 匹配，支持多个不同 key 值）
	var existingKey db.APIKey
	err := sp.db.Where("provider = ? AND encrypted_value = ?", provider, obfuscated).First(&existingKey).Error
	if err == nil {
		// 已存在相同 key 值的记录，直接复用（不覆盖）
		sp.logger.Debug("reusing existing api key for config target",
			zap.String("provider", provider),
			zap.String("target_url", targetURL),
			zap.String("key_id", existingKey.ID))
		return &existingKey.ID, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("query api key: %w", err)
	}

	// 不存在，创建新记录（混淆存储）
	// Name 使用 "Auto-{provider}-{uuid[:8]}" 保证全局唯一，不依赖 key 值的后缀。
	// 之前使用混淆后缀会导致两个 key 混淆后恰好有相同后 8 位时产生 UNIQUE 冲突。
	newKeyID := uuid.NewString()
	autoName := fmt.Sprintf("Auto-%s-%s", provider, newKeyID[:8])
	newKey := &db.APIKey{
		ID:             newKeyID,
		Name:           autoName,
		Provider:       provider,
		EncryptedValue: obfuscated,
		IsActive:       true,
		CreatedAt:      time.Now().UTC(),
	}

	if err := sp.db.Create(newKey).Error; err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}

	sp.logger.Info("auto-created api key for config target",
		zap.String("provider", provider),
		zap.String("target_url", targetURL),
		zap.String("key_id", newKey.ID))

	return &newKey.ID, nil
}

// syncConfigTargetsToDatabase 将配置文件中的 targets 同步到数据库。
func (sp *SProxy) syncConfigTargetsToDatabase(repo *db.LLMTargetRepo) error {
	logger := sp.logger.Named("sync")

	// 1. 加载配置文件中的 targets
	configTargets := sp.cfg.LLM.Targets
	logger.Info("syncing config targets to database",
		zap.Int("count", len(configTargets)))

	// 2. 同步到数据库
	keepKeys := make([]db.ConfigTargetKey, 0, len(configTargets))
	for _, ct := range configTargets {
		// 解析 API Key ID
		apiKeyID, err := sp.resolveAPIKeyID(ct.APIKey, ct.Provider, ct.URL)
		if err != nil {
			logger.Warn("failed to resolve api key",
				zap.String("url", ct.URL),
				zap.Error(err))
			continue
		}

		// UPSERT
		modelMappingJSON := "{}"
		if len(ct.ModelMapping) > 0 {
			if jsonBytes, err := json.Marshal(ct.ModelMapping); err == nil {
				modelMappingJSON = string(jsonBytes)
			}
		}
		// 序列化 SupportedModels
		supportedModelsJSON := "[]"
		if len(ct.SupportedModels) > 0 {
			if jsonBytes, err := json.Marshal(ct.SupportedModels); err == nil {
				supportedModelsJSON = string(jsonBytes)
			}
		}

		target := &db.LLMTarget{
			URL:                 ct.URL,
			APIKeyID:            apiKeyID,
			Provider:            ct.Provider,
			Name:                ct.Name,
			Weight:              ct.Weight,
			HealthCheckPath:     ct.HealthCheckPath,
			ModelMappingJSON:    modelMappingJSON,
			SupportedModelsJSON: supportedModelsJSON,
			AutoModel:           ct.AutoModel,
			Source:              "config",
			IsEditable:          false,
			IsActive:            true,
		}

		err = repo.Seed(target)
		if err != nil {
			logger.Error("failed to sync config target",
				zap.String("url", ct.URL),
				zap.Error(err))
			continue
		}

		keepKeys = append(keepKeys, db.ConfigTargetKey{URL: ct.URL})
		logger.Debug("config target synced",
			zap.String("url", ct.URL))
	}

	// 3. 清理：删除数据库中 source='config' 但不在配置文件中的记录
	// 按 URL 匹配（URL 现为全局唯一）
	deleted, err := repo.DeleteConfigTargetsNotInList(keepKeys)
	if err != nil {
		logger.Error("failed to clean up config targets", zap.Error(err))
	} else if deleted > 0 {
		logger.Info("cleaned up removed config targets", zap.Int("count", deleted))
	}

	logger.Info("config targets sync completed",
		zap.Int("synced", len(keepKeys)),
		zap.Int("deleted", deleted))

	return nil
}

// SyncConfigTargets 同步配置文件中的 LLM targets 到数据库。
// 必须先调用 SetConfigAndDB 设置配置和数据库。
func (sp *SProxy) SyncConfigTargets() error {
	if sp.cfg == nil || sp.db == nil {
		return fmt.Errorf("config and db must be set before syncing targets")
	}

	repo := db.NewLLMTargetRepo(sp.db, sp.logger)
	return sp.syncConfigTargetsToDatabase(repo)
}

// loadAllTargets 从数据库加载所有活跃的 LLM targets
func (sp *SProxy) loadAllTargets(repo *db.LLMTargetRepo) ([]config.LLMTarget, error) {
	// 从数据库加载所有 targets（包括 config 和 database 来源的）
	dbTargets, err := repo.ListAll()
	if err != nil {
		return nil, fmt.Errorf("list targets: %w", err)
	}

	targets := make([]config.LLMTarget, 0, len(dbTargets))
	for _, dt := range dbTargets {
		if !dt.IsActive {
			sp.logger.Debug("skipping inactive target", zap.String("url", dt.URL))
			continue // 跳过禁用的 targets
		}

		// 解密 API Key
		apiKey, keyErr := sp.resolveAPIKey(dt.APIKeyID)
		apiKeyError := false
		if keyErr != nil {
			// ERROR 级别：API Key 解析失败是配置错误，不会自动恢复，必须人工介入。
			// 包含 target_id 以便管理员直接用 UUID 定位 target。
			sp.logger.Error("api key resolution failed for llm target; target will remain in balancer but forced unhealthy until fixed",
				zap.String("target_id", dt.ID),
				zap.String("target_url", dt.URL),
				zap.String("api_key_id", ptrToString(dt.APIKeyID)),
				zap.Error(keyErr))
			apiKeyError = true
			// 不 continue：继续将 target 加入列表，SyncLLMTargets 会将其强制标为不健康。
			// 这样 dashboard 能显示正确的不健康状态，绑定该 target 的请求也会得到
			// "unhealthy" 错误而非 "not found in balancer" 错误。
		}

		// 反序列化 ModelMappingJSON
		var modelMapping map[string]string
		if dt.ModelMappingJSON != "" && dt.ModelMappingJSON != "{}" {
			_ = json.Unmarshal([]byte(dt.ModelMappingJSON), &modelMapping)
		}

		// 反序列化 SupportedModelsJSON
		var supportedModels []string
		if dt.SupportedModelsJSON != "" && dt.SupportedModelsJSON != "[]" {
			if err := json.Unmarshal([]byte(dt.SupportedModelsJSON), &supportedModels); err != nil {
				sp.logger.Warn("failed to parse supported_models, treating as unrestricted",
					zap.String("url", dt.URL),
					zap.String("raw", dt.SupportedModelsJSON),
					zap.Error(err),
				)
			}
		}

		targets = append(targets, config.LLMTarget{
			ID:              dt.ID,
			URL:             dt.URL,
			APIKey:          apiKey,
			Provider:        dt.Provider,
			Name:             dt.Name,
			Weight:          dt.Weight,
			HealthCheckPath: dt.HealthCheckPath,
			ModelMapping:    modelMapping,
			SupportedModels: supportedModels,
			AutoModel:       dt.AutoModel,
			APIKeyError:     apiKeyError,
		})
	}

	// 统计
	configCount := 0
	databaseCount := 0
	for _, dt := range dbTargets {
		if !dt.IsActive {
			continue
		}
		if dt.Source == "config" {
			configCount++
		} else {
			databaseCount++
		}
	}

	sp.logger.Info("loaded LLM targets",
		zap.Int("total", len(targets)),
		zap.Int("config", configCount),
		zap.Int("database", databaseCount))

	return targets, nil
}

// resolveAPIKey 根据 API Key ID 查询 API Key 值。
// 根据 key_scheme 字段选择解密方式：
//   - "aes"：使用 keyDecryptFn（AES-256-GCM）解密，由 Admin API 或 CLI apikey add 写入；
//   - "obfuscated"：使用 obfuscateKey 还原，由 config-sync 路径写入；
//   - ""（空，迁移前历史记录）：先尝试 AES，失败则回退 obfuscateKey，兼容旧数据。
func (sp *SProxy) resolveAPIKey(apiKeyID *string) (string, error) {
	if apiKeyID == nil || *apiKeyID == "" {
		return "", nil // API Key 可选
	}

	var apiKey db.APIKey
	if err := sp.db.Where("id = ?", *apiKeyID).First(&apiKey).Error; err != nil {
		return "", fmt.Errorf("query api key: %w", err)
	}

	switch apiKey.KeyScheme {
	case "aes":
		// 明确标记为 AES（Admin API / CLI 新建），硬错误，不静默降级
		if sp.keyDecryptFn == nil {
			return "", fmt.Errorf("key %s uses AES scheme but keyDecryptFn is not configured", *apiKeyID)
		}
		plain, err := sp.keyDecryptFn(apiKey.EncryptedValue)
		if err != nil {
			return "", fmt.Errorf("AES decrypt api key %s: %w", *apiKeyID, err)
		}
		return plain, nil

	case "obfuscated":
		// 明确标记为混淆（config-sync），直接还原
		return obfuscateKey(apiKey.EncryptedValue), nil

	default:
		// key_scheme 为空：迁移前的历史记录，来源未知。
		// 先尝试 AES（Admin API 路径），失败则回退 obfuscateKey（config-sync 路径）。
		if sp.keyDecryptFn != nil {
			if plain, err := sp.keyDecryptFn(apiKey.EncryptedValue); err == nil {
				return plain, nil
			}
			// AES 解密失败（正常：该 key 可能是 obfuscated 格式），静默回退
		}
		return obfuscateKey(apiKey.EncryptedValue), nil
	}
}

// swapFirstLast 交换字符串的首尾字符（对称操作）。
// 长度 <= 1 时原样返回。
func swapFirstLast(s string) string {
	if len(s) <= 1 {
		return s
	}
	b := []byte(s)
	b[0], b[len(b)-1] = b[len(b)-1], b[0]
	return string(b)
}

// obfuscateKey 混淆 API Key，保留前缀（最后一个 "-" 之前的部分），
// 仅对 body 部分（最后一个 "-" 之后）执行 swapFirstLast。
// 例如 "sk-ant-api03-XXXX" → "sk-ant-api03-" + swapFirstLast("XXXX")。
// 无 "-" 时对整个字符串执行 swapFirstLast（如纯随机 token）。
// 对称操作：obfuscateKey(obfuscateKey(s)) == s。
func obfuscateKey(key string) string {
	idx := strings.LastIndex(key, "-")
	if idx < 0 || idx == len(key)-1 {
		return swapFirstLast(key)
	}
	return key[:idx+1] + swapFirstLast(key[idx+1:])
}

// ptrToString 辅助函数：将 *string 转为 string（用于日志）
func ptrToString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// LoadAllTargets 从数据库加载所有活跃的 LLM targets（公开方法）
func (sp *SProxy) LoadAllTargets() ([]config.LLMTarget, error) {
	if sp.db == nil {
		return nil, fmt.Errorf("database must be set before loading targets")
	}

	repo := db.NewLLMTargetRepo(sp.db, sp.logger)
	return sp.loadAllTargets(repo)
}

// SyncLLMTargets 从数据库重新加载所有活跃的 LLM targets，原子更新 llmBalancer 和 llmHC。
// 在通过 WebUI / API 增删改启停 target 后调用，使变更立即生效，无需重启。
//
// 新 target 入场策略：
//   - 有 HealthCheckPath：以 Healthy=false 加入，立即触发一次主动检查，检查通过后变 healthy，
//     不会用真实用户请求试错坏节点，也不需要等待 30s ticker
//   - 无 HealthCheckPath（无主动检查）：以 Healthy=true 加入，依赖被动熔断
//
// 存量 target 策略：保留其当前健康/排水状态，不干扰运行中的熔断逻辑。
func (sp *SProxy) SyncLLMTargets() {
	if sp.llmBalancer == nil || sp.llmHC == nil || sp.db == nil {
		return
	}

	// 记录同步开始时间，用于 MarkSyncedBefore：仅将 updated_at <= syncTime 的 target
	// 标记为已同步，避免同步期间发生的新写操作被误标记为已同步。
	syncTime := time.Now()

	loadedTargets, err := sp.LoadAllTargets()
	if err != nil {
		sp.logger.Error("SyncLLMTargets: failed to load targets from db", zap.Error(err))
		return
	}

	// 快照现有 balancer 状态，用于保留存量 target 的健康/排水标志
	existingTargets := sp.llmBalancer.Targets()
	existingHealth := make(map[string]bool, len(existingTargets))
	existingDrain := make(map[string]bool, len(existingTargets))
	for _, t := range existingTargets {
		existingHealth[t.ID] = t.Healthy
		existingDrain[t.ID] = t.Draining
	}

	lbTargets := make([]lb.Target, 0, len(loadedTargets))
	healthPaths := make(map[string]string, len(loadedTargets))
	credentials := make(map[string]lb.TargetCredential, len(loadedTargets))
	var newTargetsWithPath []string // 新加入且有 health path 的 target，需立即检查

	for _, t := range loadedTargets {
		w := t.Weight
		if w <= 0 {
			w = 1
		}

		isNew := false
		healthy := true
		draining := false
		targetID := t.ID
		if targetID == "" {
			// config-sourced targets without DB ID: fall back to URL as key
			targetID = t.URL
		}
		if t.APIKeyError {
			// API Key 解析失败：强制标为不健康，阻止流量路由到无法认证的节点。
			// 即使该 target 之前是健康的，也必须重置，因为缺少凭证无法正常转发。
			healthy = false
			draining = false
			sp.logger.Warn("SyncLLMTargets: forcing target unhealthy due to API key error",
				zap.String("target_id", targetID),
				zap.String("target_url", t.URL),
			)
		} else if h, exists := existingHealth[targetID]; exists {
			// 存量 target：保留当前健康/排水状态
			healthy = h
			draining = existingDrain[targetID]
		} else {
			// 新 target
			isNew = true
			if t.HealthCheckPath != "" {
				// 有主动检查路径：先标 false，等检查通过再变 healthy
				// 避免用真实用户请求试错可能根本不通的节点
				healthy = false
			}
			// 无主动检查路径：healthy=true（只能依赖被动熔断，行为与启动时一致）
		}

		lbTargets = append(lbTargets, lb.Target{
			ID:              targetID,
			Addr:            t.URL,
			Weight:          w,
			Healthy:         healthy,
			Draining:        draining,
			SupportedModels: t.SupportedModels,
			AutoModel:       t.AutoModel,
		})
		sp.logger.Debug("SyncLLMTargets: adding target",
			zap.String("id", targetID),
			zap.String("url", t.URL),
			zap.String("name", t.Name),
			zap.Bool("is_new", isNew),
			zap.Bool("api_key_error", t.APIKeyError),
		)
		if t.HealthCheckPath != "" {
			healthPaths[targetID] = t.HealthCheckPath
			if isNew {
				newTargetsWithPath = append(newTargetsWithPath, targetID)
			}
		}
		// 构建认证凭证（用于主动健康检查认证）
		if t.APIKey != "" {
			credentials[targetID] = lb.TargetCredential{
				APIKey:   t.APIKey,
				Provider: t.Provider,
			}
		}
	}

	// Fix Bug A: 同步 sp.targets，保证 llmTargetInfoForID 可通过 UUID 找到正确 APIKey。
	// 原来 sp.targets 只在启动时从 cfg 初始化，ID 均为 ""，导致 sync 后的请求走到
	// fallback 分支返回空 APIKey，所有转发请求携带空 Bearer token。
	newSpTargets := make([]LLMTarget, 0, len(loadedTargets))
	for _, t := range loadedTargets {
		newSpTargets = append(newSpTargets, LLMTarget{
			ID:           t.ID,
			URL:          t.URL,
			APIKey:       t.APIKey,
			Provider:     t.Provider,
			Name:         t.Name,
			Weight:       t.Weight,
			ModelMapping: t.ModelMapping,
		})
	}
	sp.targets = newSpTargets

	sp.llmBalancer.UpdateTargets(lbTargets)
	sp.llmHC.UpdateHealthPaths(healthPaths)
	sp.llmHC.UpdateCredentials(credentials)

	// 对新加入且有 health path 的 target 立即发起一次主动检查（异步）
	// 检查通过后 MarkHealthy，不需要等下一个 30s ticker
	for _, id := range newTargetsWithPath {
		sp.llmHC.CheckTarget(id)
	}

	// 统计带新字段的 target 数量
	countWithModels := 0
	countWithAutoModel := 0
	for _, t := range lbTargets {
		if len(t.SupportedModels) > 0 {
			countWithModels++
		}
		if t.AutoModel != "" {
			countWithAutoModel++
		}
	}

	// 将 updated_at <= syncTime 的 target 标记为已同步
	syncRepo := db.NewLLMTargetRepo(sp.db, sp.logger)
	if err := syncRepo.MarkSyncedBefore(syncTime); err != nil {
		sp.logger.Warn("SyncLLMTargets: failed to mark targets synced", zap.Error(err))
	}

	sp.logger.Info("SyncLLMTargets: balancer and health checker updated",
		zap.Int("targets", len(lbTargets)),
		zap.Int("health_check_paths", len(healthPaths)),
		zap.Int("credentials", len(credentials)),
		zap.Int("with_model_filter", countWithModels),
		zap.Int("with_auto_model", countWithAutoModel),
		zap.Int("new_targets_checking", len(newTargetsWithPath)),
	)
}

// Drain 进入排水模式。
// 排水模式下，节点仍可处理现有请求，但不再接受新流量（通过集群路由表通知其他节点）。
func (sp *SProxy) Drain() error {
	if sp.draining.Load() {
		return nil // 已经在排水模式
	}
	sp.draining.Store(true)
	sp.drainStarted = time.Now()
	sp.drainReason = "admin requested"

	sp.logger.Info("node entering drain mode",
		zap.Int64("active_requests", sp.activeRequests.Load()),
	)

	// 如果有集群管理器，通知其他节点
	if sp.clusterMgr != nil && sp.sourceNode != "" {
		sp.clusterMgr.DrainNode(sp.sourceNode)
	}

	return nil
}

// Undrain 退出排水模式，恢复正常流量接收。
func (sp *SProxy) Undrain() error {
	if !sp.draining.Load() {
		return nil // 不在排水模式
	}
	sp.draining.Store(false)
	sp.drainReason = ""

	sp.logger.Info("node exited drain mode",
		zap.Int64("active_requests", sp.activeRequests.Load()),
	)

	// 如果有集群管理器，通知其他节点
	if sp.clusterMgr != nil && sp.sourceNode != "" {
		sp.clusterMgr.UndrainNode(sp.sourceNode)
	}

	return nil
}

// IsDraining 返回当前是否处于排水模式。
func (sp *SProxy) IsDraining() bool {
	return sp.draining.Load()
}

// DrainStatus 返回排水模式的详细状态。
type DrainStatus struct {
	Draining       bool      `json:"draining"`
	ActiveRequests int64     `json:"active_requests"`
	DrainStarted   time.Time `json:"drain_started,omitempty"`
	DrainReason    string    `json:"drain_reason,omitempty"`
}

// GetDrainStatus 返回排水模式的详细状态。
func (sp *SProxy) GetDrainStatus() DrainStatus {
	return DrainStatus{
		Draining:       sp.draining.Load(),
		ActiveRequests: sp.activeRequests.Load(),
		DrainStarted:   sp.drainStarted,
		DrainReason:    sp.drainReason,
	}
}

// ActiveRequests 返回当前活跃请求数。
func (sp *SProxy) ActiveRequests() int64 {
	return sp.activeRequests.Load()
}

// SetNotifier 设置告警通知器（可选）。
// 设置后，StartActiveRequestsMonitor 会在活跃请求数越过/恢复阈值时触发 webhook 告警。
func (sp *SProxy) SetNotifier(n *alert.Notifier) {
	sp.notifier = n
}

// StartActiveRequestsMonitor 启动活跃请求数阈值监控。
// threshold=0 或 notifier=nil 时为 no-op（不启动 goroutine）。
// 内部每 10 秒采样一次；越过阈值时触发 EventHighLoad，恢复后触发 EventLoadRecovered。
// 边沿触发：持续超载只触发一次告警，不产生告警风暴。
func StartActiveRequestsMonitor(
	ctx context.Context,
	sp *SProxy,
	threshold int64,
	notifier *alert.Notifier,
	sourceNode string,
	logger *zap.Logger,
) {
	startActiveRequestsMonitor(ctx, sp, threshold, notifier, sourceNode, logger, 10*time.Second)
}

// startActiveRequestsMonitor 是可测试的内部实现，interval 可由测试注入短周期。
func startActiveRequestsMonitor(
	ctx context.Context,
	sp *SProxy,
	threshold int64,
	notifier *alert.Notifier,
	sourceNode string,
	logger *zap.Logger,
	interval time.Duration,
) {
	if threshold <= 0 || notifier == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var overThreshold bool
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current := sp.activeRequests.Load()
				if !overThreshold && current >= threshold {
					overThreshold = true
					logger.Warn("active_requests exceeded alert threshold",
						zap.Int64("active_requests", current),
						zap.Int64("threshold", threshold),
					)
					notifier.Notify(alert.Event{
						Kind:    alert.EventHighLoad,
						Message: fmt.Sprintf("active requests %d exceeded threshold %d", current, threshold),
						Labels: map[string]string{
							"node":      sourceNode,
							"current":   strconv.FormatInt(current, 10),
							"threshold": strconv.FormatInt(threshold, 10),
						},
					})
				} else if overThreshold && current < threshold {
					overThreshold = false
					logger.Info("active_requests recovered below alert threshold",
						zap.Int64("active_requests", current),
						zap.Int64("threshold", threshold),
					)
					notifier.Notify(alert.Event{
						Kind:    alert.EventLoadRecovered,
						Message: fmt.Sprintf("active requests %d recovered below threshold %d", current, threshold),
						Labels: map[string]string{
							"node":      sourceNode,
							"current":   strconv.FormatInt(current, 10),
							"threshold": strconv.FormatInt(threshold, 10),
						},
					})
				}
			}
		}
	}()
	logger.Info("active requests monitor started", zap.Int64("threshold", threshold))
}

// LLMTargetStatuses 返回当前所有 LLM 目标的运行时状态（含健康状态）。
// 若未配置均衡器，则所有目标视为健康（无主动/被动检查）。
func (sp *SProxy) LLMTargetStatuses() []LLMTargetStatus {
	if sp.llmBalancer == nil {
		result := make([]LLMTargetStatus, len(sp.targets))
		for i, t := range sp.targets {
			w := t.Weight
			if w <= 0 {
				w = 1
			}
			id := t.ID
			if id == "" {
				id = t.URL
			}
			result[i] = LLMTargetStatus{
				ID:       id,
				URL:      t.URL,
				Name:     t.Name,
				Provider: t.Provider,
				Weight:   w,
				Healthy:  true,
				Draining: false,
			}
		}
		return result
	}

	lbTargets := sp.llmBalancer.Targets()
	result := make([]LLMTargetStatus, 0, len(lbTargets))
	for _, t := range lbTargets {
		st := LLMTargetStatus{
			ID:       t.ID,
			URL:      t.Addr, // t.Addr is the actual network URL
			Weight:   t.Weight,
			Healthy:  t.Healthy,
			Draining: t.Draining,
		}
		for _, lt := range sp.targets {
			ltID := lt.ID
			if ltID == "" {
				ltID = lt.URL
			}
			if ltID == t.ID {
				st.Name = lt.Name
				st.Provider = lt.Provider
				st.URL = lt.URL // ensure URL from target, not lb.Target.Addr
				break
			}
		}
		result = append(result, st)
	}
	return result
}

// Handler 构建并返回完整的 s-proxy HTTP 处理链：
//
//	RecoveryMiddleware → RequestIDMiddleware → AuthMiddleware → [QuotaMiddleware] → ActiveRequestCounter → SProxyHandler
func (sp *SProxy) Handler() http.Handler {
	core := http.HandlerFunc(sp.serveProxy)

	var afterAuth http.Handler = core
	if sp.quotaChecker != nil {
		// QuotaMiddleware 放在 AuthMiddleware 之后，此时 context 中已有 claims
		quotaMW := quota.NewMiddleware(sp.logger, sp.quotaChecker, func(r *http.Request) string {
			if claims := ClaimsFromContext(r.Context()); claims != nil {
				return claims.UserID
			}
			return ""
		})
		afterAuth = quotaMW(core)
	}

	// 活跃请求计数器：在配额检查之后、实际代理之前开始计数。
	// 计数范围包含认证、配额检查和实际代理的全部时间（代表"正在处理的请求"）。
	withCounter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sp.activeRequests.Add(1)
		defer sp.activeRequests.Add(-1)
		afterAuth.ServeHTTP(w, r)
	})

	withAuth := AuthMiddleware(sp.logger, sp.jwtMgr, withCounter)
	withReqID := RequestIDMiddleware(sp.logger, withAuth)
	return RecoveryMiddleware(sp.logger, withReqID)
}

// healthResponse /health 响应结构
type healthResponse struct {
	Status         string `json:"status"`            // "ok" | "degraded"
	Version        string `json:"version"`           // 版本字符串
	UptimeSeconds  int64  `json:"uptime_seconds"`    // 进程运行时长（秒）
	ActiveRequests int64  `json:"active_requests"`   // 当前正在处理的代理请求数
	QueueDepth     int    `json:"usage_queue_depth"` // 用量写入 channel 中的待处理记录数
	DBReachable    bool   `json:"db_reachable"`      // 数据库是否可达
}

// HealthHandler 返回 s-proxy 健康检查处理器，供 /health 注册使用。
//
// 响应示例（全部正常）：
//
//	HTTP 200 {"status":"ok","version":"v1.5.0 (abc1234) ...","uptime_seconds":3600,
//	           "active_requests":5,"usage_queue_depth":12,"db_reachable":true}
//
// 响应示例（DB 不可达）：
//
//	HTTP 503 {"status":"degraded","db_reachable":false,...}
func (sp *SProxy) HealthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uptime := int64(time.Since(sp.startTime).Seconds())
		activeReqs := sp.activeRequests.Load()

		queueDepth := 0
		if sp.writer != nil {
			queueDepth = sp.writer.QueueDepth()
		}

		dbReachable := true
		if sp.sqlDB != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := sp.sqlDB.PingContext(ctx); err != nil {
				dbReachable = false
				sp.logger.Warn("health check: database ping failed",
					zap.Error(err),
				)
			}
		}

		status := "ok"
		httpStatus := http.StatusOK
		if !dbReachable {
			status = "degraded"
			httpStatus = http.StatusServiceUnavailable
			sp.logger.Warn("health check: reporting degraded status",
				zap.Int64("uptime_seconds", uptime),
				zap.Bool("db_reachable", dbReachable),
			)
		}

		resp := healthResponse{
			Status:         status,
			Version:        version.Short(),
			UptimeSeconds:  uptime,
			ActiveRequests: activeReqs,
			QueueDepth:     queueDepth,
			DBReachable:    dbReachable,
		}

		sp.logger.Debug("health check requested",
			zap.String("status", status),
			zap.Int64("uptime_seconds", uptime),
			zap.Int64("active_requests", activeReqs),
			zap.Int("queue_depth", queueDepth),
			zap.Bool("db_reachable", dbReachable),
		)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// pickLLMTarget 选择下一个 LLM target，支持用户/分组绑定和负载均衡。
//
// 选择优先级：
//  1. 用户/分组绑定（bindingResolver）→ 若绑定 target 健康且未尝试过
//  2. 加权随机负载均衡（llmBalancer）→ 过滤已尝试 + 不健康 + provider 不匹配
//  3. 回退简单轮询（无均衡器时）
func (sp *SProxy) pickLLMTarget(path, userID, groupID, requestedModel string, tried []string, candidateFilter []string) (*lb.LLMTargetInfo, error) {
	triedSet := make(map[string]bool, len(tried))
	for _, u := range tried {
		triedSet[u] = true
	}

	// 1. 用户/分组绑定优先（当 bindingResolver 已设置时必须有绑定，否则拒绝）
	if sp.bindingResolver != nil {
		boundID, found := sp.bindingResolver(userID, groupID)
		if !found {
			// 无绑定 → 直接拒绝，不 fall through 到负载均衡
			sp.logger.Warn("request rejected: no LLM binding configured for user/group",
				zap.String("user_id", userID),
				zap.String("group_id", groupID),
			)
			return nil, ErrNoLLMBinding
		}

		// 有绑定 → 必须使用该 target，不允许 fall through
		//
		// RetryTransport 的 tried 列表使用 URL 格式；boundID 可能是 UUID 或 URL（取决于绑定创建方式）。
		// 因此同时检查 triedSet[boundID]（UUID 绑定）和目标的 Addr（URL 绑定）。
		alreadyTried := triedSet[boundID]
		if !alreadyTried {
			healthy := true
			targetFound := false
			targetAddr := ""
			if sp.llmBalancer != nil {
				healthy = false
				for _, t := range sp.llmBalancer.Targets() {
					// 同时匹配 ID（UUID）和 Addr（URL），兼容 YAML 导入或旧版创建的 URL 格式绑定
					if t.ID == boundID || t.Addr == boundID {
						targetFound = true
						targetAddr = t.Addr
						if triedSet[t.Addr] {
							// RetryTransport 使用 URL 加入 tried，此 target 已被重试过
							alreadyTried = true
							break
						}
						healthy = t.Healthy
						break
					}
				}
			}
			if !alreadyTried && healthy {
				info := sp.llmTargetInfoForID(boundID)
				sp.logger.Debug("using bound LLM target",
					zap.String("user_id", userID),
					zap.String("group_id", groupID),
					zap.String("target_id", boundID),
					zap.String("url", info.URL),
				)
				return info, nil
			}
			// 绑定 target 不健康或已试过 → 报错，不 fall through
			if alreadyTried {
				sp.logger.Warn("assigned LLM target already tried, request rejected",
					zap.String("bound_id", boundID),
					zap.String("target_addr", targetAddr),
					zap.String("user_id", userID),
				)
			} else if !targetFound {
				sp.logger.Warn("assigned LLM target not found in balancer (disabled or removed), request rejected",
					zap.String("bound_id", boundID),
					zap.String("user_id", userID),
				)
			} else {
				sp.logger.Warn("assigned LLM target is unhealthy, request rejected",
					zap.String("bound_id", boundID),
					zap.String("target_addr", targetAddr),
					zap.String("user_id", userID),
				)
			}
			return nil, ErrBoundTargetUnavailable
		}

		// alreadyTried=true（由 triedSet[boundID] 直接命中，未进入上方分支）
		sp.logger.Warn("assigned LLM target already tried, request rejected",
			zap.String("bound_id", boundID),
			zap.String("user_id", userID),
		)
		return nil, ErrBoundTargetUnavailable
	}

	// 构建语义候选集（过滤 candidateFilter）
	filterSet := make(map[string]bool, len(candidateFilter))
	for _, u := range candidateFilter {
		filterSet[u] = true
	}
	if len(filterSet) > 0 {
		sp.logger.Debug("pickLLMTarget: semantic candidateFilter active",
			zap.Int("filter_size", len(filterSet)),
		)
	}

	// 2. 加权随机均衡（支持 tried 过滤 + provider 过滤 + candidateFilter）
	if sp.llmBalancer != nil {
		return sp.weightedPickExcluding(path, requestedModel, triedSet, filterSet)
	}

	// 3. 回退：简单轮询（未配置均衡器时）
	candidates := sp.candidatesByPath(path)
	if len(candidates) == 0 {
		candidates = sp.targets
	}
	// 过滤已尝试目标 + candidateFilter（若非空则只保留在 filter 内的）
	var available []LLMTarget
	for _, t := range candidates {
		tid := t.ID
		if tid == "" {
			tid = t.URL
		}
		if triedSet[tid] {
			continue
		}
		if len(filterSet) > 0 && !filterSet[tid] {
			continue
		}
		available = append(available, t)
	}
	if len(available) == 0 {
		return nil, lb.ErrNoHealthyTarget
	}
	n := sp.idx.Add(1)
	t := available[int(n-1)%len(available)]
	sp.logger.Debug("picked LLM target (round-robin)",
		zap.String("url", t.URL),
		zap.String("path", path),
	)
	return &lb.LLMTargetInfo{URL: t.URL, APIKey: t.APIKey}, nil
}

// weightedPickExcluding 从 llmBalancer 中选取健康 target，排除 tried，并应用 provider 过滤、语义候选集过滤和模型过滤。
// 执行顺序：provider 过滤 → 模型过滤（fail-open）→ 加权随机
func (sp *SProxy) weightedPickExcluding(path, requestedModel string, tried map[string]bool, candidateFilter map[string]bool) (*lb.LLMTargetInfo, error) {
	all := sp.llmBalancer.Targets()
	preferred := preferredProvidersByPath(path)

	// 步骤 1: provider + tried + candidateFilter 过滤（基础过滤）
	filter := func(targets []lb.Target, providerFilter map[string]bool) []lb.Target {
		var out []lb.Target
		for _, t := range targets {
			if !t.Healthy || tried[t.ID] {
				continue
			}
			// 语义路由候选集过滤（非空时只保留在 candidateFilter 内的 target）
			if len(candidateFilter) > 0 && !candidateFilter[t.ID] {
				continue
			}
			if providerFilter != nil {
				prov := sp.providerForID(t.ID)
				if !providerFilter[prov] {
					continue
				}
			}
			out = append(out, t)
		}
		return out
	}

	// 第一次尝试：带 provider 偏好的过滤
	candidates := filter(all, preferred)
	usedFallback := false
	if len(candidates) == 0 && preferred != nil {
		// provider 没结果，回退到无 provider 限制
		candidates = filter(all, nil)
		usedFallback = true
		sp.logger.Debug("routing: no preferred-provider candidates, falling back to all healthy targets",
			zap.String("path", path),
			zap.Any("preferred_providers", preferred),
		)
	}
	if len(candidates) == 0 {
		return nil, lb.ErrNoHealthyTarget
	}

	// 步骤 2: 模型过滤（两级 fail-open）
	// auto 模式和空 model 不过滤（让所有 target 参与负载均衡）
	if requestedModel != "" && requestedModel != "auto" {
		modelFiltered := filterByModel(candidates, requestedModel)
		if len(modelFiltered) > 0 {
			candidates = modelFiltered
		} else {
			// model 过滤无结果，fail-open 回退到 provider 过滤后的候选集
			sp.logger.Warn("weightedPickExcluding: no LLM target supports requested model, using fail-open routing",
				zap.String("requested_model", requestedModel),
				zap.Int("available_targets_before_model_filter", len(candidates)),
			)
			// candidates 保持原值（fail-open）
		}
	}

	// 步骤 3: 加权随机选取
	total := 0
	for _, c := range candidates {
		total += c.Weight
	}
	r := rand.IntN(total)
	for i := range candidates {
		r -= candidates[i].Weight
		if r < 0 {
			sp.logger.Debug("picked LLM target (weighted random)",
				zap.String("url", candidates[i].ID),
				zap.String("path", path),
				zap.String("id", candidates[i].ID),
				zap.String("provider", sp.providerForID(candidates[i].ID)),
				zap.String("requested_model", requestedModel),
				zap.Int("candidates", len(candidates)),
				zap.Bool("used_provider_fallback", usedFallback),
			)
			return sp.llmTargetInfoForID(candidates[i].ID), nil
		}
	}
	// 理论上不会到达
	return sp.llmTargetInfoForID(candidates[0].ID), nil
}

// llmTargetInfoForID 根据 UUID 查找对应的 LLMTargetInfo（含 APIKey 和实际 URL）。
func (sp *SProxy) llmTargetInfoForID(targetID string) *lb.LLMTargetInfo {
	for _, t := range sp.targets {
		id := t.ID
		if id == "" {
			id = t.URL
		}
		if id == targetID {
			return &lb.LLMTargetInfo{URL: t.URL, APIKey: t.APIKey}
		}
	}
	sp.logger.Warn("llmTargetInfoForID: target not found in sp.targets",
		zap.String("target_id", targetID),
	)
	return &lb.LLMTargetInfo{URL: targetID}
}

// llmTargetInfoForURL 根据 URL 查找对应的 LLMTargetInfo（含 APIKey）。
// Deprecated: prefer llmTargetInfoForID; kept for fallback (no-balancer path).
func (sp *SProxy) llmTargetInfoForURL(targetURL string) *lb.LLMTargetInfo {
	for _, t := range sp.targets {
		if t.URL == targetURL {
			return &lb.LLMTargetInfo{URL: t.URL, APIKey: t.APIKey}
		}
	}
	return &lb.LLMTargetInfo{URL: targetURL}
}

// providerForID 根据 UUID 查找对应 target 的 Provider。
func (sp *SProxy) providerForID(targetID string) string {
	for _, t := range sp.targets {
		id := t.ID
		if id == "" {
			id = t.URL
		}
		if id == targetID {
			return t.Provider
		}
	}
	return ""
}

// providerForURL 根据 URL 查找对应 target 的 Provider。
// Deprecated: prefer providerForID; kept for fallback path.
func (sp *SProxy) providerForURL(targetURL string) string {
	for _, t := range sp.targets {
		if t.URL == targetURL {
			return t.Provider
		}
	}
	return ""
}

// modelMappingForURL 根据 URL 查找对应 target 的模型名映射表。
// 返回 nil 表示该 target 未配置模型映射（不进行名称转换）。
func (sp *SProxy) modelMappingForURL(targetURL string) map[string]string {
	for _, t := range sp.targets {
		if t.URL == targetURL {
			return t.ModelMapping
		}
	}
	return nil
}

// candidatesByPath 按请求路径过滤匹配 provider 的 targets（legacy 路径使用）。
func (sp *SProxy) candidatesByPath(path string) []LLMTarget {
	preferred := preferredProvidersByPath(path)
	if preferred == nil {
		return nil
	}
	var out []LLMTarget
	for _, t := range sp.targets {
		if preferred[t.Provider] {
			out = append(out, t)
		}
	}
	return out
}

// preferredProvidersByPath 根据 API 路径返回期望的 provider 集合。
func preferredProvidersByPath(path string) map[string]bool {
	switch {
	case strings.HasPrefix(path, "/v1/chat/completions"):
		return map[string]bool{"openai": true, "ollama": true}
	case strings.HasPrefix(path, "/v1/messages"):
		return map[string]bool{"": true, "anthropic": true}
	}
	return nil
}

// buildRetryTransport 构建 RetryTransport（当 llmBalancer 已配置时）。
// effectivePath 是传递给 PickNext 的路径（OtoA 时为 "/v1/messages"，其他为 r.URL.Path）。
// 这是次要防御机制：确保 retry 时 pickLLMTarget 从 /v1/messages 对应的 preferredProvidersByPath
// 中选 Anthropic targets。主要机制是 Director 将出向请求路径改写为 convertedPath（Step 5.10）。
func (sp *SProxy) buildRetryTransport(userID, groupID, effectivePath, requestedModel string) http.RoundTripper {
	if sp.llmBalancer == nil {
		return sp.transport
	}
	maxRetries := sp.maxRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	return &lb.RetryTransport{
		Inner:         sp.transport,
		MaxRetries:    maxRetries,
		RetryOnStatus: sp.retryOnStatus,
		PickNext: func(_ string, tried []string) (*lb.LLMTargetInfo, error) {
			// The `path` parameter passed by RetryTransport is intentionally shadowed by the
			// captured `effectivePath`. This is correct: for OtoA retries, effectivePath is
			// "/v1/messages" (Anthropic targets); for all other cases it equals r.URL.Path
			// (same as what RetryTransport would pass). RetryTransport's `path` arg is the
			// original request path before Director rewrite — we don't want that here.
			return sp.pickLLMTarget(effectivePath, userID, groupID, requestedModel, tried, nil)
		},
		OnSuccess: func(targetURL string) {
			if sp.llmHC != nil {
				sp.llmHC.RecordSuccess(targetURL)
			}
		},
		OnFailure: func(targetURL string) {
			if sp.llmHC != nil {
				sp.llmHC.RecordFailure(targetURL)
			}
		},
		Logger: sp.logger,
	}
}

// serveProxy 核心代理逻辑：
//  1. 从 context 取 claims（已由 AuthMiddleware 验证）
//  2. 删除 X-PairProxy-Auth，注入真实 Authorization
//  3. 用 TeeResponseWriter 包装 ResponseWriter（同时转发 + 解析 token）
//  4. 反向代理到 LLM
//  5. （sp-1 模式）在响应中注入路由表更新头
func (sp *SProxy) serveProxy(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	claims := ClaimsFromContext(r.Context())
	if claims == nil {
		sp.logger.Error("claims missing in context", zap.String("request_id", reqID))
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "claims missing")
		return
	}

	// OTel span：记录代理请求的完整生命周期
	ctx, span := otel.Tracer("pairproxy.sproxy").Start(r.Context(), "pairproxy.proxy")
	defer span.End()
	span.SetAttributes(
		attribute.String("user_id", claims.UserID),
		attribute.String("path", r.URL.Path),
	)
	r = r.WithContext(ctx)
	clientRoutingVersion := parseRoutingVersion(r.Header.Get("X-Routing-Version"))
	// 移除路由版本头，不转发给 LLM
	r.Header.Del("X-Routing-Version")

	// debug 日志：记录客户端发来的原始请求（body 可能在下面被读取）
	var debugReqBody []byte

	// F-3: 单次请求大小限制 + 并发请求限制
	// OpenAI 兼容：同时在此阶段注入 stream_options（无论是否有 quotaChecker）
	var bodyBytes []byte
	needBodyRead := sp.quotaChecker != nil ||
		strings.HasPrefix(r.URL.Path, "/v1/chat/completions") ||
		strings.HasPrefix(r.URL.Path, "/v1/messages")

	if needBodyRead && r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, 10<<20) // 10MB limit
		var readErr error
		bodyBytes, readErr = io.ReadAll(r.Body)
		_ = r.Body.Close()

		if readErr == nil {
			debugReqBody = bodyBytes

			// OpenAI 流式请求：注入 stream_options.include_usage: true
			if strings.HasPrefix(r.URL.Path, "/v1/chat/completions") {
				originalSize := len(bodyBytes)
				bodyBytes = injectOpenAIStreamOptions(r.URL.Path, bodyBytes, sp.logger, reqID)
				if len(bodyBytes) != originalSize {
					sp.logger.Debug("OpenAI streaming request detected, stream_options injected",
						zap.String("request_id", reqID),
						zap.String("path", r.URL.Path),
						zap.Int("original_size", originalSize),
						zap.Int("modified_size", len(bodyBytes)),
					)
				}
			}

			// Quota checker: max_tokens 检查
			if sp.quotaChecker != nil {
				var reqBody struct {
					MaxTokens int64 `json:"max_tokens"`
				}
				if jsonErr := json.Unmarshal(bodyBytes, &reqBody); jsonErr == nil && reqBody.MaxTokens > 0 {
					if sizeErr := sp.quotaChecker.CheckRequestSize(claims.UserID, reqBody.MaxTokens); sizeErr != nil {
						sp.logger.Warn("request rejected: request size limit",
							zap.String("request_id", reqID),
							zap.String("user_id", claims.UserID),
							zap.Int64("max_tokens", reqBody.MaxTokens),
						)
						if qErr, ok := sizeErr.(*quota.ExceededError); ok {
							writeQuotaError(w, qErr.Kind, qErr.Current, qErr.Limit, qErr.ResetAt)
						} else {
							writeJSONError(w, http.StatusTooManyRequests, "quota_exceeded", sizeErr.Error())
						}
						return
					}
				}
			}

			// 还原 body（可能已被 injectOpenAIStreamOptions 修改）
			r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			r.ContentLength = int64(len(bodyBytes))
		}
	}

	if sp.quotaChecker != nil {
		// 2. 并发请求限制：TryAcquire 槽，请求结束后自动 Release
		release, concErr := sp.quotaChecker.TryAcquireConcurrent(claims.UserID)
		if concErr != nil {
			sp.logger.Warn("request rejected: concurrent limit",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
			)
			if qErr, ok := concErr.(*quota.ExceededError); ok {
				writeQuotaError(w, qErr.Kind, qErr.Current, qErr.Limit, qErr.ResetAt)
			} else {
				writeJSONError(w, http.StatusTooManyRequests, "quota_exceeded", concErr.Error())
			}
			return
		}
		defer release()
	}

	// 每次请求捕获一次 debug logger 快照，保证单请求内行为一致（SIGHUP 切换时不会半途改变）。
	dl := sp.debugLogger.Load()

	// debug 日志：← client request（body 未被上面读取时，在此补读）
	if dl != nil {
		if debugReqBody == nil && r.Body != nil {
			debugReqBody, _ = io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(debugReqBody))
		} else if len(bodyBytes) > 0 {
			// 使用已读取的 bodyBytes（可能已被 injectOpenAIStreamOptions 修改）
			debugReqBody = bodyBytes
		}
		dl.Debug("← client request",
			zap.String("request_id", reqID),
			zap.String("user_id", claims.UserID),
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			sanitizeHeaders(r.Header),
			zap.ByteString("body", truncate(debugReqBody, debugBodyMaxBytes)),
		)
	}

	// 语义路由：仅对无绑定用户（bindingResolver==nil）的 LB 路径生效
	var semanticCandidates []string
	if sp.semanticRouter != nil && sp.bindingResolver == nil && len(bodyBytes) > 0 {
		if msgs := extractMessagesFromBody(bodyBytes); len(msgs) > 0 {
			semanticCandidates = sp.semanticRouter.Route(r.Context(), msgs)
			if len(semanticCandidates) > 0 {
				sp.logger.Info("semantic router: candidate pool narrowed",
					zap.String("request_id", reqID),
					zap.Int("candidates", len(semanticCandidates)),
				)
			}
		} else {
			sp.logger.Debug("semantic router: skipped, no messages extracted from body",
				zap.String("request_id", reqID),
			)
		}
	} else if sp.semanticRouter != nil && sp.bindingResolver != nil {
		sp.logger.Debug("semantic router: skipped, binding resolver active",
			zap.String("request_id", reqID),
			zap.String("user_id", claims.UserID),
		)
	}

	// 提取客户端请求的模型名（用于模型感知路由 F2 + auto 模式 F3）
	// 注意：此提取在 pickLLMTarget 之前完成，以便路由层按模型过滤
	requestedModel := extractModel(r)
	if requestedModel == "" && len(bodyBytes) > 0 {
		requestedModel = extractModelFromBody(bodyBytes)
	}
	if requestedModel != "" {
		sp.logger.Debug("model-aware routing: extracted model from request",
			zap.String("request_id", reqID),
			zap.String("model", requestedModel),
		)
	}

	firstInfo, pickErr := sp.pickLLMTarget(r.URL.Path, claims.UserID, claims.GroupID, requestedModel, nil, semanticCandidates)
	if pickErr != nil {
		switch {
		case errors.Is(pickErr, ErrNoLLMBinding):
			sp.logger.Warn("request rejected: no LLM binding for user",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
			)
			span.SetStatus(codes.Error, "no LLM binding")
			writeJSONError(w, http.StatusForbidden, "no_llm_assigned",
				"no LLM target is assigned to your account; contact an administrator")
		case errors.Is(pickErr, ErrBoundTargetUnavailable):
			sp.logger.Error("assigned LLM target unavailable",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
			)
			span.SetStatus(codes.Error, "assigned LLM target unavailable")
			writeJSONError(w, http.StatusServiceUnavailable, "upstream_unavailable",
				"your assigned LLM target is currently unavailable; contact an administrator")
		default:
			sp.logger.Error("no LLM target available",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
				zap.Error(pickErr),
			)
			span.SetStatus(codes.Error, "no upstream available")
			writeJSONError(w, http.StatusBadGateway, "no_upstream", "no healthy LLM target available")
		}
		return
	}
	targetURL, err := url.Parse(firstInfo.URL)
	if err != nil {
		sp.logger.Error("invalid LLM target URL",
			zap.String("request_id", reqID),
			zap.String("url", firstInfo.URL),
			zap.Error(err),
		)
		span.SetStatus(codes.Error, "invalid upstream URL")
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "invalid upstream URL")
		return
	}
	targetProvider := sp.providerForURL(firstInfo.URL)
	// 当 target 未配置 provider 时，从请求路径推断（OpenAI 兼容路径兜底）
	if targetProvider == "" && strings.HasPrefix(r.URL.Path, "/v1/chat/completions") {
		targetProvider = "openai"
	}

	// requestedModel 已在上方 pickLLMTarget 调用前提取
	// AtoO 场景：此值为客户端发送的 Anthropic 模型名（如 "claude-3-5-sonnet-20241022"），
	//   转换后 bodyBytes 会变成 OpenAI 格式，模型名可能已被 mapping 替换，
	//   在响应时需要用原始 Anthropic 模型名覆盖 OpenAI 响应的 model 字段。
	// OtoA 场景：此值为 OpenAI 客户端发送的模型名（如 "gpt-4o"），
	//   不是 Anthropic 模型名，响应时用于日志记录和 UsageRecord.Model 字段。

	// recordedActualModel 追踪经过 auto/model-mapping 等变换后实际发往上游的模型名。
	// 空字符串表示与 requestedModel 相同（无变换）。
	var recordedActualModel string

	// Auto 模式处理（F3）：选定 target 后，用 target 的 auto_model 重写请求体中的 model 字段
	if requestedModel == "auto" && sp.llmBalancer != nil && len(bodyBytes) > 0 {
		actualModel := sp.autoModelFromURL(firstInfo.URL)
		if actualModel != "" {
			rewritten := rewriteModelInBody(bodyBytes, "auto", actualModel)
			if len(rewritten) != len(bodyBytes) || string(rewritten) != string(bodyBytes) {
				bodyBytes = rewritten
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				r.ContentLength = int64(len(bodyBytes))
				recordedActualModel = actualModel
				sp.logger.Info("auto mode: rewrote model in request body",
					zap.String("request_id", reqID),
					zap.String("target_url", firstInfo.URL),
					zap.String("actual_model", actualModel),
				)
			}
		} else {
			sp.logger.Debug("auto mode: no auto_model configured, passing through 'auto' to LLM",
				zap.String("request_id", reqID),
				zap.String("target_url", firstInfo.URL),
			)
		}
	}

	convDir := detectConversionDirection(r.URL.Path, targetProvider)
	effectivePath := r.URL.Path
	if convDir == conversionOtoA {
		effectivePath = "/v1/messages"
		// Re-pick using effectivePath so preferredProvidersByPath["/v1/messages"] matches
		// Anthropic targets correctly (spec §"Target Routing Fix for OtoA").
		if repicked, pickErr := sp.pickLLMTarget(effectivePath, claims.UserID, claims.GroupID, requestedModel, nil, nil); pickErr == nil {
			firstInfo = repicked
			targetProvider = sp.providerForURL(firstInfo.URL)
			// Update targetURL so the Director closure (captured at line ~1164) uses the right host.
			// For bound users re-pick returns the same target — this is a no-op in that case.
			if newURL, parseErr := url.Parse(firstInfo.URL); parseErr == nil {
				targetURL = newURL
			}
			// Safety check: if re-pick somehow returned a non-Anthropic target (misconfiguration),
			// reset convDir to avoid applying OtoA conversion to an OpenAI/Ollama endpoint.
			if sp.providerForURL(firstInfo.URL) != "anthropic" {
				convDir = conversionNone
			}
		} else {
			sp.logger.Warn("OtoA re-pick failed, continuing with initial target",
				zap.String("request_id", reqID),
				zap.Error(pickErr),
			)
		}
	}
	var convertedPath string
	if convDir == conversionAtoO {
		sp.logger.Info("protocol conversion required",
			zap.String("request_id", reqID),
			zap.String("from", "anthropic"),
			zap.String("to", "openai"),
			zap.String("target_provider", targetProvider),
			zap.String("target_url", firstInfo.URL),
			zap.String("original_path", r.URL.Path),
		)

		// 转换请求 body
		if len(bodyBytes) > 0 {
			modelMapping := sp.modelMappingForURL(firstInfo.URL)
			converted, newPath, convErr := convertAnthropicToOpenAIRequest(bodyBytes, sp.logger, reqID, modelMapping)
			if convErr == nil {
				bodyBytes = converted
				convertedPath = newPath
				// 更新 body
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				r.ContentLength = int64(len(bodyBytes))
				// 记录实际模型（AtoO 转换内部应用了 model mapping）
				if len(modelMapping) > 0 {
					if mapped := mapModelName(requestedModel, modelMapping); mapped != requestedModel {
						recordedActualModel = mapped
					}
				}

				sp.logger.Info("request converted successfully",
					zap.String("request_id", reqID),
					zap.String("new_path", newPath),
					zap.Int("converted_size", len(converted)),
				)
			} else if errors.Is(convErr, ErrPrefillNotSupported) {
				sp.logger.Warn("protocol conversion rejected: prefill not supported",
					zap.String("request_id", reqID),
				)
				writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", convErr.Error())
				return
			} else {
				sp.logger.Warn("protocol conversion failed, forwarding original request",
					zap.String("request_id", reqID),
					zap.Error(convErr),
				)
				convDir = conversionNone // 降级：不转换响应
			}
		} else {
			sp.logger.Warn("protocol conversion skipped: empty request body",
				zap.String("request_id", reqID),
			)
			convDir = conversionNone
		}
	}
	if convDir == conversionOtoA {
		sp.logger.Info("protocol conversion required (OtoA)",
			zap.String("request_id", reqID),
			zap.String("from", "openai"),
			zap.String("to", "anthropic"),
			zap.String("target_provider", targetProvider),
			zap.String("target_url", firstInfo.URL),
			zap.String("original_path", r.URL.Path),
		)
		if len(bodyBytes) > 0 {
			modelMapping := sp.modelMappingForURL(firstInfo.URL)
			converted, newPath, convErr := convertOpenAIToAnthropicRequest(bodyBytes, sp.logger, reqID, modelMapping)
			if convErr == nil {
				bodyBytes = converted
				convertedPath = newPath
				r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				r.ContentLength = int64(len(bodyBytes))
				// 记录实际模型（OtoA 转换内部应用了 model mapping）
				if len(modelMapping) > 0 {
					if mapped := mapModelName(requestedModel, modelMapping); mapped != requestedModel {
						recordedActualModel = mapped
					}
				}
				sp.logger.Info("OtoA request converted successfully",
					zap.String("request_id", reqID),
					zap.String("new_path", newPath),
					zap.Int("converted_size", len(converted)),
				)
			} else {
				sp.logger.Warn("OtoA request conversion failed, returning 400",
					zap.String("request_id", reqID),
					zap.Error(convErr),
				)
				writeJSONError(w, http.StatusBadRequest, "invalid_request_error",
					"failed to convert OpenAI request to Anthropic format: "+convErr.Error())
				return
			}
		} else {
			sp.logger.Warn("OtoA conversion skipped: empty request body",
				zap.String("request_id", reqID),
			)
			writeJSONError(w, http.StatusBadRequest, "invalid_request_error",
				"request body is required for OpenAI to Anthropic conversion")
			return
		}
	}

	// conversionNone 时仍需应用 model mapping（直传场景：客户端与 target 同协议）
	if convDir == conversionNone && len(bodyBytes) > 0 && requestedModel != "" {
		modelMapping := sp.modelMappingForURL(firstInfo.URL)
		if len(modelMapping) > 0 {
			mappedModel := mapModelName(requestedModel, modelMapping)
			if mappedModel != requestedModel {
				rewritten := rewriteModelInBody(bodyBytes, requestedModel, mappedModel)
				if len(rewritten) > 0 {
					bodyBytes = rewritten
					r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
					r.ContentLength = int64(len(bodyBytes))
					recordedActualModel = mappedModel
					sp.logger.Debug("model name mapped (passthrough)",
						zap.String("request_id", reqID),
						zap.String("original_model", requestedModel),
						zap.String("mapped_model", mappedModel),
					)
				}
			}
		}
	}

	// 补充 span attributes（target 确定后）
	span.SetAttributes(
		attribute.String("provider", targetProvider),
		attribute.String("upstream_url", firstInfo.URL),
	)
	if convDir != conversionNone {
		span.SetAttributes(attribute.Bool("protocol_converted", true))
	}

	startTime := time.Now()

	// 预填充 UsageRecord 模板（除 token 数/状态码/时长外的字段）
	// model 优先从 X-PairProxy-Model 头取（cproxy 注入），其次从请求 body 取（OpenAI 格式客户端）
	// 注意：requestedModel 在协议转换前已提取，含客户端发送的原始模型名：
	//   AtoO 场景为 Anthropic 模型名（非 mapped 名）；OtoA 场景为 OpenAI 模型名（如 "gpt-4o"）
	model := requestedModel
	usageRecord := db.UsageRecord{
		RequestID:   reqID,
		UserID:      claims.UserID,
		Model:       model,
		ActualModel: recordedActualModel,
		UpstreamURL: firstInfo.URL,
		SourceNode:  sp.sourceNode,
		CreatedAt:   time.Now().UTC(),
	}
	if usageRecord.Model != "" {
		span.SetAttributes(attribute.String("model", usageRecord.Model))
	}

	// 用 TeeResponseWriter 包装（streaming + non-streaming 均适用）
	// provider 决定解析器类型（Anthropic SSE / OpenAI SSE / Ollama SSE）

	// 对话内容跟踪：仅在该用户已启用跟踪时创建 CaptureSession
	var captureSession *track.CaptureSession
	if t := sp.convTracker.Load(); t != nil && t.IsTracked(claims.Username) {
		captureSession = track.NewCaptureSession(
			t.UserConvDir(claims.Username),
			reqID,
			claims.Username,
			bodyBytes,
			targetProvider,
		)
		sp.logger.Debug("conversation tracking active",
			zap.String("request_id", reqID),
			zap.String("username", claims.Username),
		)
	}

	// 训练语料采集：创建 CorpusCollector（如果 corpus writer 已启用）
	var corpusCollector *corpus.Collector
	if cw := sp.corpusWriter.Load(); cw != nil {
		corpusCollector = corpus.NewCollector(
			cw,
			sp.sourceNode,
			reqID,
			claims.Username,
			claims.GroupID,
			requestedModel,
			firstInfo.URL,
			targetProvider,
			bodyBytes,
			startTime,
		)
		sp.logger.Debug("corpus collector active",
			zap.String("request_id", reqID),
			zap.String("username", claims.Username),
		)
	}

	// 构建 onChunk 回调（debug 日志 + 对话跟踪 + 语料采集）
	var chunkHandlers []func([]byte)
	if dl != nil {
		dlRef := dl // capture for closure
		chunkHandlers = append(chunkHandlers, func(chunk []byte) {
			dlRef.Debug("← LLM stream chunk",
				zap.String("request_id", reqID),
				zap.ByteString("data", truncate(chunk, debugBodyMaxBytes)),
			)
		})
	}
	if captureSession != nil {
		chunkHandlers = append(chunkHandlers, captureSession.FeedChunk)
	}
	if corpusCollector != nil {
		chunkHandlers = append(chunkHandlers, corpusCollector.FeedChunk)
	}
	var onChunk func([]byte)
	switch len(chunkHandlers) {
	case 1:
		onChunk = chunkHandlers[0]
	default:
		if len(chunkHandlers) > 1 {
			onChunk = func(chunk []byte) {
				for _, h := range chunkHandlers {
					h(chunk)
				}
			}
		}
	}

	// 创建 TeeResponseWriter
	var finalWriter http.ResponseWriter = w
	switch convDir {
	case conversionAtoO:
		streamConverter := NewOpenAIToAnthropicStreamConverter(w, sp.logger, reqID, model)
		finalWriter = streamConverter
		sp.logger.Debug("AtoO stream converter inserted",
			zap.String("request_id", reqID),
		)
	case conversionOtoA:
		streamConverter := NewAnthropicToOpenAIStreamConverter(w, sp.logger, reqID, model)
		finalWriter = streamConverter
		sp.logger.Debug("OtoA stream converter inserted",
			zap.String("request_id", reqID),
		)
	}

	tw := tap.NewTeeResponseWriter(finalWriter, sp.logger, sp.writer, usageRecord, targetProvider, startTime, onChunk)

	// 构建 transport（配置均衡器时使用 RetryTransport；否则使用基础 transport）
	transport := sp.buildRetryTransport(claims.UserID, claims.GroupID, effectivePath, requestedModel)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host

			// 协议转换：修改请求路径
			// convertedPath 是不含版本前缀的路径后缀（如 /chat/completions）。
			// 若 target URL 带有自定义 base path（如 /v2、/openai/v1），则拼接在前面；
			// 若无 base path（标准 OpenAI 端点），则补全为 /v1/chat/completions。
			if convDir != conversionNone && convertedPath != "" {
				originalPath := req.URL.Path
				basePath := strings.TrimRight(targetURL.Path, "/")
				if basePath == "" {
					req.URL.Path = "/v1" + convertedPath
				} else {
					req.URL.Path = basePath + convertedPath
				}
				sp.logger.Debug("request path converted for protocol conversion",
					zap.String("request_id", reqID),
					zap.String("original_path", originalPath),
					zap.String("converted_path", convertedPath),
					zap.String("target", firstInfo.URL),
				)
			}

			// 删除客户端认证头（X-PairProxy-Auth 或 Authorization），注入真实 API Key
			// F-5: 优先使用 DB 中的动态 API Key，未找到则回退到配置文件中的静态 Key
			req.Header.Del("X-PairProxy-Auth")
			req.Header.Del("Authorization") // 清理客户端的 Bearer JWT，避免泄漏给上游
			// 清理直连模式的 Anthropic 认证头（防止泄露给上游）
			req.Header.Del("x-api-key")
			apiKey := firstInfo.APIKey
			if sp.apiKeyResolver != nil {
				if k, ok := sp.apiKeyResolver(claims.UserID, claims.GroupID); ok {
					apiKey = k
					sp.logger.Debug("using dynamic api key for user",
						zap.String("user_id", claims.UserID),
					)
				}
			}
			req.Header.Set("Authorization", "Bearer "+apiKey)
			req.Header.Del("X-Forwarded-For")

			sp.logger.Debug("proxying request to LLM",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
				zap.String("target", firstInfo.URL),
				zap.String("path", req.URL.Path),
				zap.String("method", req.Method),
			)
			if dl != nil {
				dl.Debug("→ LLM request",
					zap.String("request_id", reqID),
					zap.String("method", req.Method),
					zap.String("target", firstInfo.URL+req.URL.Path),
					sanitizeHeaders(req.Header),
				)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			durationMs := time.Since(startTime).Milliseconds()
			ct := resp.Header.Get("Content-Type")
			isStreaming := strings.Contains(ct, "text/event-stream")

			// 记录延迟到 metrics 追踪器
			if tracker := metrics.GetGlobalLatencyTracker(); tracker != nil {
				tracker.ObserveProxyLatency(durationMs)
				tracker.ObserveLLMLatency(durationMs)
			}

			sp.logger.Info("LLM response received",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
				zap.Int("status", resp.StatusCode),
				zap.Bool("streaming", isStreaming),
				zap.Int64("duration_ms", durationMs),
			)

			if dl != nil {
				dl.Debug("← LLM response",
					zap.String("request_id", reqID),
					zap.Int("status", resp.StatusCode),
					zap.Bool("streaming", isStreaming),
					sanitizeHeaders(resp.Header),
				)
			}

			otoaRecorded := false
			if !isStreaming {
				// 非 streaming：读取完整 body，解析 token，然后重新放回（ReverseProxy 需要）
				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()

				if readErr != nil {
					sp.logger.Warn("failed to read non-streaming body",
						zap.String("request_id", reqID),
						zap.Error(readErr),
					)
				}

				// 协议転換：非流式響応処理
				if readErr == nil && len(body) > 0 {
					switch convDir {
					case conversionAtoO:
						// AtoO: RECORD FIRST from raw OpenAI body so OpenAISSEParser parses correctly.
						// After conversion the body is Anthropic JSON; OpenAISSEParser cannot parse it
						// and would return (0,0). Mirror the OtoA pattern: record before converting.
						sp.logger.Debug("AtoO: converting non-streaming response",
							zap.String("request_id", reqID),
							zap.Int("original_size", len(body)),
						)
						tw.RecordNonStreaming(body, resp.StatusCode, durationMs)
						otoaRecorded = true
						if resp.StatusCode >= 400 {
							body = convertOpenAIErrorResponse(body, sp.logger, reqID)
							sp.logger.Info("AtoO: error response converted to Anthropic format",
								zap.String("request_id", reqID),
							)
						} else {
							converted, convErr := convertOpenAIToAnthropicResponse(body, sp.logger, reqID, model)
							if convErr == nil {
								body = converted
								sp.logger.Info("AtoO: non-streaming response converted successfully",
									zap.String("request_id", reqID),
								)
							} else {
								sp.logger.Warn("AtoO: response conversion failed, forwarding original",
									zap.String("request_id", reqID),
									zap.Error(convErr),
								)
							}
						}

					case conversionOtoA:
						// OtoA: RECORD FIRST with raw Anthropic body so AnthropicSSEParser parses correctly.
						// Then convert to OpenAI format for the client.
						// Use otoaRecorded=true to prevent double-recording at the default location below.
						sp.logger.Debug("OtoA: converting non-streaming response",
							zap.String("request_id", reqID),
							zap.Int("original_size", len(body)),
						)
						tw.RecordNonStreaming(body, resp.StatusCode, durationMs)
						otoaRecorded = true
						if resp.StatusCode >= 400 {
							body = convertAnthropicErrorResponseToOpenAI(body, sp.logger, reqID)
							sp.logger.Info("OtoA: error response converted to OpenAI format",
								zap.String("request_id", reqID),
							)
						} else {
							converted, convErr := convertAnthropicToOpenAIResponseReverse(body, sp.logger, reqID, requestedModel)
							if convErr == nil {
								body = converted
								sp.logger.Info("OtoA: non-streaming response converted successfully",
									zap.String("request_id", reqID),
								)
							} else {
								sp.logger.Warn("OtoA: response conversion failed, forwarding original Anthropic response",
									zap.String("request_id", reqID),
									zap.Error(convErr),
								)
							}
						}
					}
				}

				resp.Body = io.NopCloser(bytes.NewReader(body))
				resp.ContentLength = int64(len(body))
				resp.Header.Set("Content-Length", strconv.Itoa(len(body)))

				if dl != nil {
					dl.Debug("← LLM response body",
						zap.String("request_id", reqID),
						zap.ByteString("body", truncate(body, debugBodyMaxBytes)),
					)
				}

				// 尝试从 body 补充 model 字段（Director 阶段请求 body 已转发，只能在此处补充）
				if usageRecord.Model == "" {
					if m := extractModelFromBody(body); m != "" {
						usageRecord.Model = m
						tw.UpdateModel(m)
						sp.logger.Debug("model extracted from response body",
							zap.String("request_id", reqID),
							zap.String("model", m),
						)
					} else {
						sp.logger.Debug("model field not found in request or response",
							zap.String("request_id", reqID),
						)
					}
				}

				// 通过 TeeWriter 记录（token 解析 + 写入 UsageWriter）
				if !otoaRecorded {
					tw.RecordNonStreaming(body, resp.StatusCode, durationMs)
				}

				// 对话跟踪：记录非流式响应内容
				if captureSession != nil {
					captureSession.SetNonStreamingResponse(body)
					captureSession.Flush()
				}
				// 训练语料采集：记录非流式响应
				if corpusCollector != nil {
					corpusCollector.SetNonStreamingResponse(body)
					corpusCollector.Finish(resp.StatusCode, durationMs)
				}
			} else if convDir != conversionNone {
				// 協議転換（streaming）：
				// AtoO: 上游返回 OpenAI SSE → OpenAIToAnthropicStreamConverter 転為 Anthropic SSE 給客户端
				// OtoA: 上游返回 Anthropic SSE → AnthropicToOpenAIStreamConverter 転為 OpenAI SSE 給客户端
				// 注意：実際転換在 TeeResponseWriter.Write() 調用 streamConverter.Write() 中処理
				sp.logger.Debug("streaming response will be converted",
					zap.String("request_id", reqID),
				)
			}
			// streaming 情况：TeeResponseWriter.Write() 会自动 Feed SSE 解析器，
			// 在 message_stop 事件时异步记录；onChunk 回调已记录每条 chunk

			// sp-1 模式：向 c-proxy 注入路由表更新
			if sp.clusterMgr != nil {
				sp.clusterMgr.InjectResponseHeaders(resp.Header, clientRoutingVersion)
				if resp.Header.Get("X-Routing-Update") != "" {
					sp.logger.Debug("routing table injected into response",
						zap.String("request_id", reqID),
						zap.Int64("client_version", clientRoutingVersion),
					)
				}
			}

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			durationMs := time.Since(startTime).Milliseconds()
			sp.logger.Error("upstream request failed",
				zap.String("request_id", reqID),
				zap.String("user_id", claims.UserID),
				zap.String("target", firstInfo.URL),
				zap.Int64("duration_ms", durationMs),
				zap.Error(err),
			)
			// 记录失败请求（token 数为 0）
			errRecord := usageRecord
			errRecord.StatusCode = http.StatusBadGateway
			errRecord.DurationMs = durationMs
			sp.writer.Record(errRecord)

			writeJSONError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		},
		// FlushInterval=-1：立即刷新（SSE 流式响应必须）
		FlushInterval: -1,
		Transport:     transport,
	}

	proxy.ServeHTTP(tw, r)

	// 请求完成后立即失效该用户的配额缓存。
	// 此时用量记录已写入 UsageWriter channel（异步，5s 内刷入 DB）。
	// 提前失效缓存可确保下一个请求重新查询 DB，避免缓存过期前的超额使用。
	// DB flush 完成后 onFlush 回调也会再次失效，消除 flush 延迟窗口。
	if sp.quotaChecker != nil {
		sp.quotaChecker.InvalidateCache(claims.UserID)
	}

	// 训练语料采集：流式响应完成后提交记录
	if corpusCollector != nil {
		corpusCollector.Finish(tw.StatusCode(), time.Since(startTime).Milliseconds())
	}
}

// parseRoutingVersion 将字符串版本号解析为 int64，解析失败返回 0。
func parseRoutingVersion(s string) int64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// extractModel 从请求头部或 JSON body 中提取模型名称。
// 优先级：X-PairProxy-Model 头 > 请求 body 中的 model 字段。
func extractModel(r *http.Request) string {
	if m := r.Header.Get("X-PairProxy-Model"); m != "" {
		return m
	}
	return ""
}

// extractModelFromBody 从 JSON body 中提取 model 字段（供未来扩展使用）。
// body 必须已被完整读取。
func extractModelFromBody(body []byte) string {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return req.Model
	}
	return ""
}

// extractMessagesFromBody 从请求 body 中提取 messages 字段，用于语义路由分类。
func extractMessagesFromBody(body []byte) []corpus.Message {
	var req struct {
		Messages []corpus.Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err == nil {
		return req.Messages
	}
	return nil
}

// ServeDirect 处理直连模式（API Key 认证）的代理请求。
//
// 前提：请求 context 中已由 KeyAuthMiddleware 注入 *auth.JWTClaims。
// ServeDirect 是直连模式（API Key 认证）的入口，直接委托给 serveProxy。
// x-api-key 认证头的清理已在 serveProxy 的 Director 中统一处理（对所有调用路径生效）。
// 路径重写（/anthropic/* → /v1/*）由 DirectProxyHandler 在调用前完成。
func (sp *SProxy) ServeDirect(w http.ResponseWriter, r *http.Request) {
	sp.logger.Debug("serving direct proxy request",
		zap.String("path", r.URL.Path),
		zap.String("method", r.Method),
	)
	sp.serveProxy(w, r)
}

// ======================== Model-Aware Routing Functions (F2+F3) ========================

// matchModel 检查 model 是否匹配 patterns 中的任一模式。
// 模式支持：精确匹配 | 前缀通配（"claude-*"）| 全通配（"*"）
func matchModel(model string, patterns []string) bool {
	for _, p := range patterns {
		if p == "*" || p == model {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(model, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

// rewriteModelInBody 将 JSON body 中的 model 字段从 old 值替换为 newModel。
// 解析失败或无 model 字段时原样返回 body。
func rewriteModelInBody(body []byte, old, newModel string) []byte {
	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body // 非 JSON，原样返回
	}
	if m, ok := req["model"].(string); ok && m == old {
		req["model"] = newModel
		if newBody, err := json.Marshal(req); err == nil {
			return newBody
		}
	}
	return body // 无 model 字段或编码失败，原样返回
}

// filterByModel 过滤 targets，仅保留支持 model 的（或未配置 supported_models 的）。
// 未配置 supported_models 的 target 视为支持所有模型。
func filterByModel(targets []lb.Target, model string) []lb.Target {
	var out []lb.Target
	for _, t := range targets {
		// 未配置 = 不限制，始终参与候选
		if len(t.SupportedModels) == 0 || matchModel(model, t.SupportedModels) {
			out = append(out, t)
		}
	}
	return out
}

// autoModelFromURL 从当前 balancer 快照中查询 target URL 的 auto_model。
// 降级策略：auto_model > supported_models[0] > ""（透传）
// 查询 sp.llmBalancer.Targets() 而非 sp.targets，支持 WebUI 热更新。
func (sp *SProxy) autoModelFromURL(targetURL string) string {
	if sp.llmBalancer == nil {
		return ""
	}
	for _, t := range sp.llmBalancer.Targets() {
		// t.ID 可能是 UUID 或 URL（fallback）；t.Addr 始终是 URL
		if t.Addr == targetURL || t.ID == targetURL {
			if t.AutoModel != "" {
				return t.AutoModel
			}
			if len(t.SupportedModels) > 0 {
				return t.SupportedModels[0]
			}
			return ""
		}
	}
	return ""
}
