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

// TableName 方法（可选，用于显式指定表名）
func (Group) TableName() string           { return "groups" }
func (User) TableName() string            { return "users" }
func (RefreshToken) TableName() string    { return "refresh_tokens" }
func (UsageLog) TableName() string        { return "usage_logs" }
func (Peer) TableName() string            { return "peers" }
func (AuditLog) TableName() string        { return "audit_logs" }
func (APIKey) TableName() string          { return "api_keys" }
func (APIKeyAssignment) TableName() string { return "api_key_assignments" }
