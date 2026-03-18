package config

import (
	"fmt"
	"strings"
	"time"
)

// CProxyConfig c-proxy 完整配置
type CProxyConfig struct {
	Listen ListenConfig `yaml:"listen"`
	SProxy SProxySect   `yaml:"sproxy"`
	Auth   CProxyAuth   `yaml:"auth"`
	Log    LogConfig    `yaml:"log"`
}

// TelemetryConfig OpenTelemetry 分布式追踪配置
type TelemetryConfig struct {
	Enabled      bool    `yaml:"enabled"`        // 默认 false
	OTLPEndpoint string  `yaml:"otlp_endpoint"`  // 如 "http://jaeger:4318"
	OTLPProtocol string  `yaml:"otlp_protocol"`  // "grpc"（默认）| "http" | "stdout"
	ServiceName  string  `yaml:"service_name"`   // 显示在追踪后端中的服务名
	SamplingRate float64 `yaml:"sampling_rate"`  // 0.0~1.0，默认 1.0（全量采样）
}

// SProxyFullConfig s-proxy 完整配置
type SProxyFullConfig struct {
	Listen    ListenConfig    `yaml:"listen"`
	LLM       LLMConfig       `yaml:"llm"`
	Database  DatabaseConfig  `yaml:"database"`
	Auth      SProxyAuth      `yaml:"auth"`
	Admin     AdminConfig     `yaml:"admin"`
	Cluster   ClusterConfig   `yaml:"cluster"`
	Dashboard DashboardConfig `yaml:"dashboard"`
	Pricing   PricingConfig   `yaml:"pricing"`
	Telemetry TelemetryConfig `yaml:"telemetry"`
	Log       LogConfig       `yaml:"log"`
}

// PricingConfig 模型定价配置（用于估算费用）
type PricingConfig struct {
	// Models 按模型名称自定义定价（key = 完整模型名）
	Models map[string]ModelPrice `yaml:"models"`
	// 未匹配模型的默认定价（USD per 1000 tokens）
	DefaultInputPer1K  float64 `yaml:"default_input_per_1k"`
	DefaultOutputPer1K float64 `yaml:"default_output_per_1k"`
}

// ModelPrice 单个模型的定价
type ModelPrice struct {
	InputPer1K  float64 `yaml:"input_per_1k"`  // USD per 1K input tokens
	OutputPer1K float64 `yaml:"output_per_1k"` // USD per 1K output tokens
}

// ComputeCost 根据模型和 token 数估算费用（USD）
func (p *PricingConfig) ComputeCost(model string, inputTokens, outputTokens int) float64 {
	mp, ok := p.Models[model]
	if !ok {
		mp = ModelPrice{
			InputPer1K:  p.DefaultInputPer1K,
			OutputPer1K: p.DefaultOutputPer1K,
		}
	}
	return float64(inputTokens)/1000*mp.InputPer1K + float64(outputTokens)/1000*mp.OutputPer1K
}

// ListenConfig 监听地址配置
type ListenConfig struct {
	Host string `yaml:"host"` // 默认 "127.0.0.1"（c-proxy）/ "0.0.0.0"（s-proxy）
	Port int    `yaml:"port"` // c-proxy 默认 8080，s-proxy 默认 9000
}

// RetryConfig cproxy 请求级重试配置
type RetryConfig struct {
	Enabled       bool  `yaml:"enabled"`         // 默认 true
	MaxRetries    int   `yaml:"max_retries"`     // 最大重试次数（不含首次），默认 2
	RetryOnStatus []int `yaml:"retry_on_status"` // 触发重试的 HTTP 状态码，默认 [502, 503, 504]
}

// SProxySect c-proxy 中关于 s-proxy 的配置节
type SProxySect struct {
	Primary             string        `yaml:"primary"`               // 初始 sp-1 地址（种子节点）
	Targets             []string      `yaml:"targets"`               // 已知 s-proxy worker 地址（主节点故障兜底）
	LBStrategy          string        `yaml:"lb_strategy"`           // 当前固定 "weighted_random"
	HealthCheckInterval time.Duration `yaml:"health_check_interval"` // 默认 30s
	RequestTimeout      time.Duration `yaml:"request_timeout"`       // 默认 300s

	// 健康检查增强（改进项3）
	HealthCheckTimeout         time.Duration `yaml:"health_check_timeout"`          // 单次检查超时，默认 3s
	HealthCheckFailureThreshold int          `yaml:"health_check_failure_threshold"` // 连续失败阈值，默认 3
	HealthCheckRecoveryDelay   time.Duration `yaml:"health_check_recovery_delay"`   // 熔断后自动恢复延迟，默认 60s
	PassiveFailureThreshold    int           `yaml:"passive_failure_threshold"`     // 被动熔断阈值，默认 3

	// 路由表主动发现（改进项4）
	SharedSecret        string        `yaml:"shared_secret"`         // 集群内部 API 密钥（路由轮询用）
	RoutingPollInterval time.Duration `yaml:"routing_poll_interval"` // 路由表主动轮询间隔，默认 60s；0=禁用

	// 请求级重试（改进项5）
	Retry RetryConfig `yaml:"retry"`
}

// CProxyAuth c-proxy 认证配置
type CProxyAuth struct {
	TokenDir         string        `yaml:"token_dir"`         // 默认 DefaultTokenDir()
	AutoRefresh      bool          `yaml:"auto_refresh"`      // 默认 true
	RefreshThreshold time.Duration `yaml:"refresh_threshold"` // 默认 30m
}

// LLMConfig s-proxy 上游 LLM 配置
type LLMConfig struct {
	LBStrategy     string        `yaml:"lb_strategy"`     // "round_robin"
	RequestTimeout time.Duration `yaml:"request_timeout"` // 默认 300s
	MaxRetries     int           `yaml:"max_retries"`     // 上游失败时最大重试次数（不含首次），默认 2；0=不重试
	RecoveryDelay  time.Duration `yaml:"recovery_delay"`  // 熔断后自动恢复延迟，默认 60s；0=禁用自动恢复
	Targets        []LLMTarget   `yaml:"targets"`
}

// LLMTarget 单个 LLM 上游节点
type LLMTarget struct {
	URL             string            `yaml:"url"`               // e.g. "https://api.anthropic.com"
	APIKey          string            `yaml:"api_key"`           // 支持 ${ENV_VAR} 替换
	Weight          int               `yaml:"weight"`            // 默认 1
	Provider        string            `yaml:"provider"`          // "anthropic"（默认）| "openai" | "ollama"
	Name            string            `yaml:"name"`              // 可选显示名称（空则使用 URL）
	HealthCheckPath string            `yaml:"health_check_path"` // 主动健康检查路径，空=仅被动检查
	ModelMapping    map[string]string `yaml:"model_mapping,omitempty"` // Anthropic 模型名 → Ollama/OpenAI 模型名映射；"*" 匹配所有未命中的模型
}

// DatabaseConfig 数据库配置（支持 SQLite 和 PostgreSQL）
type DatabaseConfig struct {
	// ── 驱动选择 ─────────────────────────────────────────────────────────────
	// "sqlite"（默认）| "postgres"；省略时等同于 "sqlite"（向后兼容）
	Driver string `yaml:"driver"`

	// ── SQLite 配置（Driver=sqlite 时使用）────────────────────────────────────
	Path string `yaml:"path"` // SQLite 文件路径

	// ── PostgreSQL 配置（Driver=postgres 时使用）──────────────────────────────
	// 方案一：完整 DSN（优先级高于独立字段）
	DSN string `yaml:"dsn"`
	// 方案二：独立字段（若 DSN 为空则从这些字段拼接）
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"` // "disable"|"require"|"verify-full"

	// ── 通用连接池（SQLite & PostgreSQL 共用）──────────────────────────────────
	WriteBufferSize int           `yaml:"write_buffer_size"` // 批量写入 buffer 大小，默认 200
	FlushInterval   time.Duration `yaml:"flush_interval"`    // 强制 flush 间隔，默认 5s

	// 0 = 使用内置默认值：SQLite 非内存库 MaxOpenConns=25，内存库=1；PostgreSQL=50
	MaxOpenConns    int           `yaml:"max_open_conns"`     // 最大打开连接数
	MaxIdleConns    int           `yaml:"max_idle_conns"`     // 最大空闲连接数，默认 10
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`  // 连接最大存活时间，默认 1h
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"` // 连接最大空闲时间，默认 10m
}

// LDAPConfig LDAP 连接配置
type LDAPConfig struct {
	ServerAddr   string `yaml:"server_addr"`   // host:port，如 "ldap.example.com:389"
	BaseDN       string `yaml:"base_dn"`       // 如 "dc=example,dc=com"
	BindDN       string `yaml:"bind_dn"`       // 服务账户 DN（空=匿名绑定）
	BindPassword string `yaml:"bind_password"` // 支持 ${ENV_VAR}
	UserFilter   string `yaml:"user_filter"`   // 搜索过滤器，如 "(uid=%s)"
	UseTLS       bool   `yaml:"use_tls"`       // 是否使用 LDAPS
}

// SProxyAuth s-proxy JWT 配置
type SProxyAuth struct {
	JWTSecret       string        `yaml:"jwt_secret"`        // 支持 ${ENV_VAR}
	KeygenSecret    string        `yaml:"keygen_secret"`     // API Key HMAC 签名密钥，支持 ${ENV_VAR}，至少 32 字符
	AccessTokenTTL  time.Duration `yaml:"access_token_ttl"`  // 默认 24h
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl"` // 默认 168h (7d)
	Provider        string        `yaml:"provider"`          // "local"（默认）| "ldap"
	LDAP            LDAPConfig    `yaml:"ldap"`              // LDAP 配置（provider="ldap" 时生效）
	TrustedProxies  []string      `yaml:"trusted_proxies"`   // CIDR 列表，仅来自这些代理的请求才信任 XFF；空=不信任任何代理
	DefaultGroup    string        `yaml:"default_group"`     // JIT 用户首次登录时自动分配的分组名（空=不分配）
}

// AdminConfig s-proxy 管理员配置
type AdminConfig struct {
	PasswordHash      string `yaml:"password_hash"`       // bcrypt hash，支持 ${ENV_VAR}
	KeyEncryptionKey  string `yaml:"key_encryption_key"`  // AES-256-GCM 密钥（用于加密 API Key），支持 ${ENV_VAR}
}

// WebhookTarget 单个 Webhook 告警目标
type WebhookTarget struct {
	URL      string   `yaml:"url"`      // Webhook URL
	Events   []string `yaml:"events"`   // 空 = 所有事件；填写则只推送指定事件类型
	Template string   `yaml:"template"` // 空 = 默认 JSON；Go text/template 渲染请求 body
}

// UsageBufferConfig worker 用量缓冲配置（改进项2）
type UsageBufferConfig struct {
	Enabled            bool `yaml:"enabled"`              // 默认 true
	MaxRecordsPerBatch int  `yaml:"max_records_per_batch"` // 每批最多上报条数，默认 1000
}

// ClusterConfig 集群配置（s-proxy）
type ClusterConfig struct {
	Role                string            `yaml:"role"`                  // "primary" | "worker"
	Primary             string            `yaml:"primary"`               // worker 用：sp-1 的地址
	SelfAddr            string            `yaml:"self_addr"`             // 本节点对外地址
	SelfWeight          int               `yaml:"self_weight"`           // 建议权重，默认 50
	AlertThreshold      int               `yaml:"alert_threshold"`       // active_req 超过此值触发告警，默认 80
	AlertWebhook        string            `yaml:"alert_webhook"`         // 旧字段，向后兼容（单 URL）
	AlertWebhooks       []WebhookTarget   `yaml:"alert_webhooks"`        // 新字段：多 webhook + 事件过滤 + 自定义模板
	ReportInterval      time.Duration     `yaml:"report_interval"`       // worker 用量上报间隔，默认 30s
	PeerMonitorInterval time.Duration     `yaml:"peer_monitor_interval"` // primary 监控 peer 间隔，默认 30s
	SharedSecret        string            `yaml:"shared_secret"`         // 集群内部 API 共享密钥（HMAC Bearer token）
	UsageBuffer         UsageBufferConfig `yaml:"usage_buffer"`          // worker 用量缓冲配置（改进项2）
}

// DashboardConfig Dashboard 配置
type DashboardConfig struct {
	Enabled bool `yaml:"enabled"` // 默认 true（primary 节点）
}

// LogConfig 日志配置
type LogConfig struct {
	Level    string `yaml:"level"`      // "debug" | "info" | "warn" | "error"，默认 "info"
	DebugFile string `yaml:"debug_file"` // debug 日志文件路径，默认 "debug.log"
}

// Addr 返回监听地址字符串，如 "127.0.0.1:8080"
func (l ListenConfig) Addr() string {
	host := l.Host
	if host == "" {
		host = "0.0.0.0"
	}
	port := l.Port
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// ---------------------------------------------------------------------------
// 配置校验
// ---------------------------------------------------------------------------

// Validate 校验 s-proxy 配置的必填字段和合法性。
// 应在 applyDefaults 之后调用，以确保默认值已填充。
func (c *SProxyFullConfig) Validate() error {
	var errs []string

	if c.Auth.JWTSecret == "" {
		errs = append(errs, "auth.jwt_secret is required (set ${JWT_SECRET} or provide the value directly)")
	} else if len(c.Auth.JWTSecret) < 32 {
		errs = append(errs, "auth.jwt_secret should be at least 32 characters for security (current length is too short)")
	}

	if c.Auth.KeygenSecret == "" {
		errs = append(errs, "auth.keygen_secret is required (set ${KEYGEN_SECRET} or provide the value directly)")
	} else if len(c.Auth.KeygenSecret) < 32 {
		errs = append(errs, "auth.keygen_secret should be at least 32 characters for security (current length is too short)")
	}

	switch c.Database.Driver {
	case "postgres":
		if c.Database.DSN == "" && (c.Database.Host == "" || c.Database.User == "" || c.Database.DBName == "") {
			errs = append(errs, "database: for driver=postgres, either dsn or (host + user + dbname) is required")
		}
		if c.Database.DSN == "" {
			// 独立字段模式下校验 SSLMode
			switch c.Database.SSLMode {
			case "disable", "allow", "prefer", "require", "verify-ca", "verify-full", "":
				// 合法值（空字符串由 applySProxyDefaults 填充默认值）
			default:
				errs = append(errs, fmt.Sprintf(
					`database.sslmode %q is invalid; must be one of: disable, allow, prefer, require, verify-ca, verify-full`,
					c.Database.SSLMode,
				))
			}
		}
		if c.Database.DSN == "" && c.Database.Port != 0 && (c.Database.Port < 1 || c.Database.Port > 65535) {
			errs = append(errs, fmt.Sprintf("database.port %d is out of range (1–65535)", c.Database.Port))
		}
	default: // "sqlite" 或空字符串
		if c.Database.Path == "" {
			errs = append(errs, "database.path is required")
		}
	}
	if len(c.LLM.Targets) == 0 {
		errs = append(errs, "llm.targets must not be empty (at least one LLM target is required)")
	}
	// 逐个检查 LLM target 的必填字段（尤其是 api_key 是否在 env var 展开后仍为空）
	for i, t := range c.LLM.Targets {
		if t.URL == "" {
			errs = append(errs, fmt.Sprintf("llm.targets[%d].url is required", i))
		}
		if t.APIKey == "" {
			errs = append(errs, fmt.Sprintf(
				"llm.targets[%d].api_key is empty — ensure the environment variable is set and exported before starting sproxy", i))
		}
	}
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		errs = append(errs, fmt.Sprintf("listen.port %d is out of range (1–65535)", c.Listen.Port))
	}
	if c.Cluster.Role == "worker" && c.Cluster.Primary == "" {
		errs = append(errs, "cluster.primary is required when cluster.role is \"worker\"")
	}
	if c.Cluster.Role == "worker" && c.Cluster.SharedSecret == "" {
		errs = append(errs, "cluster.shared_secret is required when cluster.role is \"worker\"")
	}
	if c.Cluster.Role == "peer" && c.Database.Driver != "postgres" {
		errs = append(errs, "cluster.role \"peer\" requires database.driver \"postgres\"")
	}
	if c.Cluster.Role != "" && c.Cluster.Role != "primary" && c.Cluster.Role != "worker" && c.Cluster.Role != "peer" {
		errs = append(errs, fmt.Sprintf("cluster.role %q is invalid; must be \"primary\", \"worker\", or \"peer\"", c.Cluster.Role))
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// Validate 校验 c-proxy 配置的必填字段和合法性。
// 应在 applyDefaults 之后调用。
func (c *CProxyConfig) Validate() error {
	var errs []string
	if c.Listen.Port < 1 || c.Listen.Port > 65535 {
		errs = append(errs, fmt.Sprintf("listen.port %d is out of range (1–65535)", c.Listen.Port))
	}
	if c.Auth.RefreshThreshold < 0 {
		errs = append(errs, fmt.Sprintf("auth.refresh_threshold %s must not be negative", c.Auth.RefreshThreshold))
	}
	if c.Log.Level != "" {
		switch c.Log.Level {
		case "debug", "info", "warn", "error":
		default:
			errs = append(errs, fmt.Sprintf("log.level %q is invalid; must be debug, info, warn, or error", c.Log.Level))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
