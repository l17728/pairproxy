package db

import (
	"time"

	"gorm.io/gorm"
)

// Group 用户分组（配额管理）
type Group struct {
	ID                  string `gorm:"primarykey"`
	Name                string `gorm:"uniqueIndex;not null"`
	DailyTokenLimit     *int64 // NULL = 无限制
	MonthlyTokenLimit   *int64 // NULL = 无限制
	RequestsPerMinute   *int   // NULL = 无限制（每分钟请求数 RPM）
	RequestsPer15Minutes *int  `gorm:"column:requests_per_15_minutes"` // NULL = 无限制（每15分钟请求数）
	RequestsPer30Minutes *int  `gorm:"column:requests_per_30_minutes"` // NULL = 无限制（每30分钟请求数）
	RequestsPerHour      *int  `gorm:"column:requests_per_hour"`       // NULL = 无限制（每小时请求数）
	MaxTokensPerRequest *int64 // NULL = 无限制（单次请求 max_tokens 上限）
	ConcurrentRequests  *int   // NULL = 无限制（每用户最大并发请求数）
	CreatedAt           time.Time
}

// User 系统用户
type User struct {
	ID               string  `gorm:"primarykey"`
	Username         string  `gorm:"not null;uniqueIndex:idx_user_authprovider_username"` // 与 AuthProvider 组合唯一，支持混合认证
	PasswordHash     string  `gorm:"not null"`
	GroupID          *string `gorm:"index"` // NULL 表示未分配分组
	Group            Group   `gorm:"foreignKey:GroupID"`
	IsActive         bool    `gorm:"default:true"`
	AuthProvider     string  `gorm:"default:'local';uniqueIndex:idx_user_authprovider_username;uniqueIndex:idx_user_authprovider_externalid"` // "local" | "ldap"；与 Username 和 ExternalID 各自组合唯一
	ExternalID       *string `gorm:"index;uniqueIndex:idx_user_authprovider_externalid"`                                                      // 外部系统唯一 ID（LDAP: uid）；与 AuthProvider 组合唯一；NULL 表示无外部 ID（本地用户）
	LegacyKeyRevoked bool    `gorm:"default:false"`                                                                                           // true = 用户已主动改密，旧版 keygenSecret 派生的 Key 不再有效
	CreatedAt        time.Time
	LastLoginAt      *time.Time
}

// RefreshToken 刷新令牌（用于撤销）
type RefreshToken struct {
	JTI       string    `gorm:"primarykey"`
	UserID    string    `gorm:"not null;index"`
	ExpiresAt time.Time `gorm:"not null"`
	Revoked   bool      `gorm:"default:false"`
	CreatedAt time.Time
}

// UsageLog 用量日志（核心统计表）
type UsageLog struct {
	ID                 uint   `gorm:"primarykey;autoIncrement"`
	RequestID          string `gorm:"uniqueIndex;not null"` // 幂等防重
	UserID             string `gorm:"not null;index"`
	Model              string
	ActualModel        string `gorm:"default:''"` // 实际转发的模型名（model mapping / auto 模式后）
	InputTokens        int    `gorm:"default:0"`
	OutputTokens       int    `gorm:"default:0"`
	TotalTokens        int    `gorm:"default:0"`
	IsStreaming        bool   `gorm:"default:false"`
	UpstreamURL        string
	StatusCode         int
	DurationMs         int64
	TtftMs             int64     `gorm:"default:0"`           // 首 token 延迟（ms）；仅 streaming 有效，0 表示未记录
	TpotMs             float64   `gorm:"default:0"`           // 每输出 token 耗时（ms/token）；0 表示无效或无输出
	CostUSD            float64   `gorm:"default:0"`           // 估算费用（USD）
	SourceNode         string    `gorm:"default:'local'"`     // 数据来源节点 ID
	Synced             bool      `gorm:"default:false;index"` // sp-2+ 用：是否已上报给 sp-1
	ClientIP           string    `gorm:"default:''"`          // 请求来源 IP
	SessionID          string    `gorm:"default:''"`          // 来自请求的真实 session_id，自动生成的留空
	EnteredModelRouter bool      `gorm:"default:false"`       // 是否进入了多绑定 Router 分支（组绑定≥2）
	RouterResultStatus int       `gorm:"default:0"`           // 0=未调用, 1=调用成功, 2=调用失败
	RouterResult       string    `gorm:"default:''"`          // Router 返回的模型名
	CacheHitScene      int       `gorm:"default:0"`           // 0=无缓存/未命中, 1=缓存满复用, 2=big模型复用, 3=未满非big调API
	ErrorBody          string    `gorm:"default:''"`          // 上游错误响应体（截取前 1024 字节，仅 status>=400 时写入）
	RequestPath        string    `gorm:"default:''"`          // 客户端请求路径（如 /v1/messages），不含 IP/端口
	CreatedAt          time.Time `gorm:"index"`
}

// BeforeCreate normalises CreatedAt to UTC so that SQLite's string-based
// timestamp comparisons are consistent regardless of the caller's local timezone.
func (u *UsageLog) BeforeCreate(tx *gorm.DB) error {
	if !u.CreatedAt.IsZero() {
		u.CreatedAt = u.CreatedAt.UTC()
	}
	return nil
}

// Peer 集群中已注册的 s-proxy 节点（sp-1 专用）
type Peer struct {
	ID           string `gorm:"primarykey"` // e.g. "sp-2"
	Addr         string `gorm:"uniqueIndex;not null"`
	Weight       int    `gorm:"default:50"`
	IsActive     bool   `gorm:"default:true"`
	RegisteredAt time.Time
	LastSeen     *time.Time
}

// APIKey 系统级 API Key（加密存储）
type APIKey struct {
	ID             string `gorm:"primarykey"`
	Name           string `gorm:"uniqueIndex;not null"` // 标识名称（唯一）
	EncryptedValue string `gorm:"not null"`             // 加密/混淆后的 Key 值；格式由 KeyScheme 决定
	KeyScheme      string `gorm:"default:'obfuscated'"` // "obfuscated"（config-sync 路径）| "aes"（Admin API 路径）
	Provider       string `gorm:"default:'anthropic'"`  // "anthropic" | "openai" | "ollama"
	IsActive       bool   `gorm:"default:true"`
	CreatedAt      time.Time
}

type AuditLog struct {
	ID        uint      `gorm:"primarykey;autoIncrement"`
	Operator  string    `gorm:"not null;index"` // 操作者（固定为 "admin"）
	Action    string    `gorm:"not null;index"` // 操作类型，如 "user.create", "group.set_quota"
	Target    string    // 操作对象（用户名、分组名等）
	Detail    string    // 变更详情（JSON 或可读字符串）
	CreatedAt time.Time `gorm:"index"`
}

// LLMBinding 用户或分组与特定 LLM target 的绑定关系。
// 用于将请求路由到指定 LLM 上游，支持精细化流量分配。
// 优先级：用户级绑定 > 分组级绑定 > 负载均衡。
type LLMBinding struct {
	ID string `gorm:"primarykey"`
	// TargetID: 普通索引 + 参与分组复合唯一索引 idx_llmb_group_target(group_id, target_id)
	TargetID string `gorm:"not null;index;uniqueIndex:idx_llmb_group_target"`
	// UserID: UNIQUE(user_id) 强制用户级 1:1 绑定；NULL 表示分组级绑定
	UserID *string `gorm:"index:idx_llmb_user;uniqueIndex:idx_llmb_user_target"`
	// GroupID: UNIQUE(group_id, target_id) 防止同一分组重复绑定同一 target；NULL 表示用户级绑定
	// 分组支持 1:N（同一分组可绑定多个 target，但 provider 必须一致）
	GroupID *string `gorm:"index:idx_llmb_group;uniqueIndex:idx_llmb_group_target"`
	// TargetURL 冗余存储：便于直接查看数据库及展示时减少 JOIN；由 Set()/AddGroupBinding() 负责写入。
	TargetURL string `gorm:"not null;index"`
	CreatedAt time.Time
}

// LLMTarget LLM 目标端点（支持配置文件和数据库双来源）
type LLMTarget struct {
	ID                  string  `gorm:"primarykey"`
	URL                 string  `gorm:"not null;uniqueIndex"` // LLM 端点 URL（全局唯一）
	APIKeyID            *string `gorm:"index"`                // 外键 → api_keys.id
	Provider            string  `gorm:"default:'anthropic'"`  // "anthropic" | "openai" | "ollama"
	Name                string  // 显示名称
	Weight              int     `gorm:"default:1"` // 负载均衡权重
	HealthCheckPath     string  // 健康检查路径
	ModelMappingJSON    string  `gorm:"column:model_mapping;default:'{}'"`    // JSON 序列化的 model_mapping（Anthropic→Ollama 模型名映射）
	SupportedModelsJSON string  `gorm:"column:supported_models;default:'[]'"` // JSON array: ["claude-sonnet-4-*", "gpt-4o", "*"]，空表示支持所有模型
	AutoModel           string  `gorm:"column:auto_model;default:''"`         // auto 模式下使用的模型名（空表示降级到 supported_models[0] 或透传）
	Source              string  `gorm:"default:'database'"`                   // "config" | "database"
	IsEditable          bool    `gorm:"default:true"`                         // false for config-sourced
	IsActive            bool    `gorm:"default:true"`
	IsSynced            bool    `gorm:"default:true"` // false 表示已修改但尚未被 SyncLLMTargets 加载到内存
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ModelInfo 模型信息表，记录模型名、参数规模及是否支持多模态输入。
// 由管理员手动插入/维护，model_router 路由决策时查询。
type ModelInfo struct {
	ID           string `gorm:"primarykey"`
	ModelName    string `gorm:"uniqueIndex;not null"` // 模型名，全局唯一
	ModelScale   string `gorm:"not null"`             // 参数规模："big" | "middle" | "small"
	IsMultimodal bool   `gorm:"default:false"`        // true = 支持图片/视频输入
	CreatedAt    time.Time
}

// TableName 方法（可选，用于显式指定表名）
func (Group) TableName() string        { return "groups" }
func (User) TableName() string         { return "users" }
func (RefreshToken) TableName() string { return "refresh_tokens" }
func (UsageLog) TableName() string     { return "usage_logs" }
func (Peer) TableName() string         { return "peers" }
func (AuditLog) TableName() string     { return "audit_logs" }
func (APIKey) TableName() string       { return "api_keys" }
func (LLMBinding) TableName() string   { return "llm_bindings" }
func (LLMTarget) TableName() string    { return "llm_targets" }
func (ModelInfo) TableName() string    { return "model_infos" }
