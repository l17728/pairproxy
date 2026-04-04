package db

import "time"

// Group 用户分组（配额管理）
type Group struct {
	ID                  string  `gorm:"primarykey"`
	Name                string  `gorm:"uniqueIndex;not null"`
	DailyTokenLimit     *int64  // NULL = 无限制
	MonthlyTokenLimit   *int64  // NULL = 无限制
	RequestsPerMinute   *int    // NULL = 无限制（每分钟请求数 RPM）
	MaxTokensPerRequest *int64  // NULL = 无限制（单次请求 max_tokens 上限）
	ConcurrentRequests  *int    // NULL = 无限制（每用户最大并发请求数）
	CreatedAt           time.Time
}

// User 系统用户
type User struct {
	ID           string     `gorm:"primarykey"`
	Username     string     `gorm:"uniqueIndex;not null"`
	PasswordHash string     `gorm:"not null"`
	GroupID      *string    `gorm:"index"` // NULL 表示未分配分组
	Group        Group      `gorm:"foreignKey:GroupID"`
	IsActive     bool       `gorm:"default:true"`
	AuthProvider string     `gorm:"default:'local'"` // "local" | "ldap"
	ExternalID   string     `gorm:"index"`            // 外部系统唯一 ID（LDAP: uid）
	CreatedAt    time.Time
	LastLoginAt  *time.Time
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
	ID           uint      `gorm:"primarykey;autoIncrement"`
	RequestID    string    `gorm:"uniqueIndex;not null"` // 幂等防重
	UserID       string    `gorm:"not null;index"`
	Model        string
	InputTokens  int       `gorm:"default:0"`
	OutputTokens int       `gorm:"default:0"`
	TotalTokens  int       `gorm:"default:0"`
	IsStreaming  bool      `gorm:"default:false"`
	UpstreamURL  string
	StatusCode   int
	DurationMs   int64
	CostUSD      float64   `gorm:"default:0"` // 估算费用（USD）
	SourceNode   string    `gorm:"default:'local'"` // 数据来源节点 ID
	Synced       bool      `gorm:"default:false;index"` // sp-2+ 用：是否已上报给 sp-1
	CreatedAt    time.Time `gorm:"index"`
}

// Peer 集群中已注册的 s-proxy 节点（sp-1 专用）
type Peer struct {
	ID           string     `gorm:"primarykey"` // e.g. "sp-2"
	Addr         string     `gorm:"uniqueIndex;not null"`
	Weight       int        `gorm:"default:50"`
	IsActive     bool       `gorm:"default:true"`
	RegisteredAt time.Time
	LastSeen     *time.Time
}

// APIKey 系统级 API Key（加密存储）
type APIKey struct {
	ID             string    `gorm:"primarykey"`
	Name           string    `gorm:"uniqueIndex;not null"` // 标识名称（唯一）
	EncryptedValue string    `gorm:"not null"`             // AES-256-GCM + base64
	Provider       string    `gorm:"default:'anthropic'"`  // "anthropic" | "openai" | "ollama"
	IsActive       bool      `gorm:"default:true"`
	CreatedAt      time.Time
}

// APIKeyAssignment API Key 分配记录（用户级优先于分组级）
type APIKeyAssignment struct {
	ID       string  `gorm:"primarykey"`
	APIKeyID string  `gorm:"not null;index"`
	UserID   *string `gorm:"index"` // 用户级（优先）
	GroupID  *string `gorm:"index"` // 分组级（兜底）
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
	ID        string     `gorm:"primarykey"`
	TargetURL string     `gorm:"not null;index"` // LLM target URL（与 config.LLMTarget.URL 匹配）
	UserID    *string    `gorm:"index"`          // 用户级绑定（优先，与 GroupID 互斥使用）
	GroupID   *string    `gorm:"index"`          // 分组级绑定（兜底）
	CreatedAt time.Time
}

// LLMTarget LLM 目标端点（支持配置文件和数据库双来源）
type LLMTarget struct {
	ID              string     `gorm:"primarykey"`
	URL             string     `gorm:"uniqueIndex;not null"` // LLM 端点 URL
	APIKeyID        *string    `gorm:"index"`                // 外键 → api_keys.id（可选）
	Provider        string     `gorm:"default:'anthropic'"`  // "anthropic" | "openai" | "ollama"
	Name            string     // 显示名称
	Weight          int        `gorm:"default:1"`            // 负载均衡权重
	HealthCheckPath string     // 健康检查路径
	ModelMappingJSON string    `gorm:"column:model_mapping;default:'{}'"` // JSON 序列化的 model_mapping（Anthropic→Ollama 模型名映射）
	SupportedModelsJSON string `gorm:"column:supported_models;default:'[]'"` // JSON array: ["claude-sonnet-4-*", "gpt-4o", "*"]，空表示支持所有模型
	AutoModel       string     `gorm:"column:auto_model;default:''"`         // auto 模式下使用的模型名（空表示降级到 supported_models[0] 或透传）
	Source          string     `gorm:"default:'database'"`   // "config" | "database"
	IsEditable      bool       `gorm:"default:true"`         // false for config-sourced
	IsActive        bool       `gorm:"default:true"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TableName 方法（可选，用于显式指定表名）
func (Group) TableName() string           { return "groups" }
func (User) TableName() string            { return "users" }
func (RefreshToken) TableName() string    { return "refresh_tokens" }
func (UsageLog) TableName() string        { return "usage_logs" }
func (Peer) TableName() string            { return "peers" }
func (AuditLog) TableName() string        { return "audit_logs" }
func (APIKey) TableName() string          { return "api_keys" }
func (APIKeyAssignment) TableName() string { return "api_key_assignments" }
func (LLMBinding) TableName() string      { return "llm_bindings" }
func (LLMTarget) TableName() string       { return "llm_targets" }

// SemanticRoute 语义路由规则（自然语言 description → target URL 集合）
// DB 记录优先于同名 YAML 规则。
type SemanticRoute struct {
	ID             string    `gorm:"primarykey"`
	Name           string    `gorm:"uniqueIndex;not null"` // 规则唯一名称
	Description    string    `gorm:"not null"`             // 送给分类器 LLM 的自然语言描述
	TargetURLsJSON string    `gorm:"column:target_urls;not null;default:'[]'"` // JSON array of target URLs
	Priority       int       `gorm:"default:0"`            // 数值越大越优先
	IsActive       bool      `gorm:"default:true"`
	Source         string    `gorm:"default:'database'"` // "config" | "database"
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (SemanticRoute) TableName() string { return "semantic_routes" }

// GroupTargetSet Group 与 Target Set 的绑定关系
// 支持两类群组：
//   1. 普通组: group_id 指向具体的 groups.id
//   2. 默认组: group_id = NULL 且 is_default = 1
type GroupTargetSet struct {
	ID          string    `gorm:"primarykey"`
	GroupID     *string   `gorm:"index"`                    // NULL = 默认组
	Name        string    `gorm:"uniqueIndex;not null"`    // 显示名称
	Strategy    string    `gorm:"default:'weighted_random'"` // "weighted_random" | "round_robin" | "priority"
	RetryPolicy string    `gorm:"default:'try_next'"`      // "try_next" | "fail_fast"
	IsDefault   bool      `gorm:"default:false;index"`     // 是否默认组
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// GroupTargetSetMember Target Set 的成员（多对多关系）
type GroupTargetSetMember struct {
	ID            string    `gorm:"primarykey"`
	TargetSetID   string    `gorm:"not null;index"`
	TargetURL     string    `gorm:"not null;index"`
	Weight        int       `gorm:"default:1"`
	Priority      int       `gorm:"default:0"`
	IsActive      bool      `gorm:"default:true"`
	HealthStatus  string    `gorm:"default:'unknown'"` // "healthy" | "degraded" | "unhealthy" | "unknown"
	LastHealthCheck *time.Time
	ConsecutiveFailures int `gorm:"default:0"`
	CreatedAt     time.Time

	// 唯一约束：同一 target_set 内 URL 唯一
	// UNIQUE(target_set_id, target_url) 由 GORM 的 uniqueIndex 处理
}

// TargetAlert Target 告警事件（持久化存储）
type TargetAlert struct {
	ID              string    `gorm:"primarykey"`
	TargetURL       string    `gorm:"not null;index"`
	AlertType       string    `gorm:"not null"`           // "error" | "degraded" | "recovered"
	Severity        string    `gorm:"not null"`           // "warning" | "error" | "critical"
	StatusCode      *int
	ErrorMessage    string
	AffectedGroups  string    // JSON 数组
	AlertKey        string    `gorm:"index"`              // 用于去重
	OccurrenceCount int       `gorm:"default:1"`          // 聚合计数
	LastOccurrence  *time.Time
	ResolvedAt      *time.Time `gorm:"index"`
	CreatedAt       time.Time `gorm:"index"`
}

// TableName 方法
func (GroupTargetSet) TableName() string { return "group_target_sets" }
func (GroupTargetSetMember) TableName() string { return "group_target_set_members" }
func (TargetAlert) TableName() string { return "target_alerts" }
