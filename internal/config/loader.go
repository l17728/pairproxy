package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// envVarPattern 匹配 ${VAR_NAME} 格式的环境变量占位符
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnv 将字符串中的 ${VAR} 替换为对应的环境变量值
// 若变量不存在则保留原占位符，并在日志中记录警告（调用方处理）
func expandEnv(s string) (string, []string) {
	var missing []string
	result := envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		// 提取变量名（去掉 ${ 和 }）
		varName := match[2 : len(match)-1]
		val, ok := os.LookupEnv(varName)
		if !ok {
			missing = append(missing, varName)
			return match // 保留原占位符
		}
		return val
	})
	return result, missing
}

// expandEnvInBytes 对整个 YAML 字节内容进行环境变量替换
func expandEnvInBytes(data []byte) ([]byte, []string) {
	expanded, missing := expandEnv(string(data))
	return []byte(expanded), missing
}

// DefaultCProxyConfigPath 返回跨平台默认的 c-proxy 配置文件路径
func DefaultCProxyConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "cproxy.yaml")
}

// DefaultSProxyConfigPath 返回跨平台默认的 s-proxy 配置文件路径
func DefaultSProxyConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "sproxy.yaml")
}

// DefaultConfigDir 返回跨平台配置目录
// Windows: %APPDATA%\pairproxy
// Linux:   ~/.config/pairproxy
// macOS:   ~/Library/Application Support/pairproxy
func DefaultConfigDir() string {
	base, err := os.UserConfigDir()
	if err != nil {
		// 降级到 home 目录
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".pairproxy")
	}
	return filepath.Join(base, "pairproxy")
}

// LoadCProxyConfig 从文件加载 c-proxy 配置，并展开环境变量
// 返回配置结构体和所有缺失的环境变量名列表（调用方决定是否 fatal）
func LoadCProxyConfig(path string) (*CProxyConfig, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read cproxy config %q: %w", path, err)
	}

	expanded, missing := expandEnvInBytes(data)

	var cfg CProxyConfig
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, missing, fmt.Errorf("parse cproxy config %q: %w", path, err)
	}

	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, missing, err
	}
	return &cfg, missing, nil
}

// ParseCProxyConfig loads, parses, and applies defaults for a c-proxy config
// WITHOUT running validation. Used by 'cproxy config validate' to inspect the
// effective configuration before reporting validation issues.
// Returns the config and any missing environment variable names.
func ParseCProxyConfig(path string) (*CProxyConfig, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read cproxy config %q: %w", path, err)
	}
	expanded, missing := expandEnvInBytes(data)
	var cfg CProxyConfig
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, missing, fmt.Errorf("parse cproxy config %q: %w", path, err)
	}
	applyDefaults(&cfg)
	return &cfg, missing, nil
}

// LoadSProxyConfig 从文件加载 s-proxy 配置，并展开环境变量
func LoadSProxyConfig(path string) (*SProxyFullConfig, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read sproxy config %q: %w", path, err)
	}

	expanded, missing := expandEnvInBytes(data)

	var cfg SProxyFullConfig
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, missing, fmt.Errorf("parse sproxy config %q: %w", path, err)
	}

	applySProxyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, missing, err
	}
	return &cfg, missing, nil
}

// applyDefaults 为 c-proxy 配置填充默认值
func applyDefaults(cfg *CProxyConfig) {
	if cfg.Listen.Host == "" {
		cfg.Listen.Host = "127.0.0.1"
	}
	if cfg.Listen.Port == 0 {
		cfg.Listen.Port = 8080
	}
	if cfg.SProxy.HealthCheckInterval == 0 {
		cfg.SProxy.HealthCheckInterval = 30 * time.Second
	}
	if cfg.SProxy.RequestTimeout == 0 {
		cfg.SProxy.RequestTimeout = 300 * time.Second
	}
	// 改进项3：健康检查增强默认值
	if cfg.SProxy.HealthCheckTimeout == 0 {
		cfg.SProxy.HealthCheckTimeout = 3 * time.Second
	}
	if cfg.SProxy.HealthCheckFailureThreshold == 0 {
		cfg.SProxy.HealthCheckFailureThreshold = 3
	}
	if cfg.SProxy.HealthCheckRecoveryDelay == 0 {
		cfg.SProxy.HealthCheckRecoveryDelay = 60 * time.Second
	}
	if cfg.SProxy.PassiveFailureThreshold == 0 {
		cfg.SProxy.PassiveFailureThreshold = 3
	}
	// 改进项4：路由表主动发现默认值
	if cfg.SProxy.RoutingPollInterval == 0 {
		cfg.SProxy.RoutingPollInterval = 60 * time.Second
	}
	// 改进项5：请求级重试默认值
	if !cfg.SProxy.Retry.Enabled {
		cfg.SProxy.Retry.Enabled = true
	}
	if cfg.SProxy.Retry.MaxRetries == 0 {
		cfg.SProxy.Retry.MaxRetries = 2
	}
	if len(cfg.SProxy.Retry.RetryOnStatus) == 0 {
		cfg.SProxy.Retry.RetryOnStatus = []int{502, 503, 504}
	}
	if cfg.Auth.TokenDir == "" {
		cfg.Auth.TokenDir = DefaultConfigDir()
	} else {
		// 展开 ~ 开头的路径
		cfg.Auth.TokenDir = expandTilde(cfg.Auth.TokenDir)
	}
	if cfg.Auth.RefreshThreshold == 0 {
		cfg.Auth.RefreshThreshold = 30 * time.Minute
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	cfg.Auth.AutoRefresh = true // 始终启用自动刷新
}

// applySProxyDefaults 为 s-proxy 配置填充默认值
func applySProxyDefaults(cfg *SProxyFullConfig) {
	if cfg.Listen.Host == "" {
		cfg.Listen.Host = "0.0.0.0"
	}
	if cfg.Listen.Port == 0 {
		cfg.Listen.Port = 9000
	}
	if cfg.LLM.RequestTimeout == 0 {
		cfg.LLM.RequestTimeout = 300 * time.Second
	}
	if cfg.LLM.MaxRetries == 0 {
		cfg.LLM.MaxRetries = 2
	}
	if cfg.LLM.RecoveryDelay == 0 {
		cfg.LLM.RecoveryDelay = 60 * time.Second
	}
	if cfg.LLM.LBStrategy == "" {
		cfg.LLM.LBStrategy = "round_robin"
	}
	// 数据库驱动默认值
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = "sqlite" // 向后兼容，默认 SQLite
	}
	if cfg.Database.Driver == "postgres" {
		if cfg.Database.Port == 0 {
			cfg.Database.Port = 5432
		}
		if cfg.Database.SSLMode == "" {
			cfg.Database.SSLMode = "disable"
		}
		if cfg.Database.MaxOpenConns == 0 {
			cfg.Database.MaxOpenConns = 50 // PG MVCC 支持高并发
		}
	}
	if cfg.Database.WriteBufferSize == 0 {
		cfg.Database.WriteBufferSize = 200
	}
	if cfg.Database.FlushInterval == 0 {
		cfg.Database.FlushInterval = 5 * time.Second
	}
	if cfg.Auth.AccessTokenTTL == 0 {
		cfg.Auth.AccessTokenTTL = 24 * time.Hour
	}
	if cfg.Auth.RefreshTokenTTL == 0 {
		cfg.Auth.RefreshTokenTTL = 168 * time.Hour
	}
	if cfg.Cluster.Role == "" {
		// PG 模式下若未指定 role，默认 peer（真正对等，无主从区分）
		// SQLite 模式下默认 primary（单机或经典主从部署）
		if cfg.Database.Driver == "postgres" {
			cfg.Cluster.Role = "peer"
		} else {
			cfg.Cluster.Role = "primary"
		}
	}
	if cfg.Cluster.SelfWeight == 0 {
		cfg.Cluster.SelfWeight = 50
	}
	if cfg.Cluster.AlertThreshold == 0 {
		cfg.Cluster.AlertThreshold = 80
	}
	if cfg.Cluster.ReportInterval == 0 {
		cfg.Cluster.ReportInterval = 30 * time.Second
	}
	if cfg.Cluster.PeerMonitorInterval == 0 {
		cfg.Cluster.PeerMonitorInterval = 30 * time.Second
	}
	// 改进项2：用量缓冲默认值
	if !cfg.Cluster.UsageBuffer.Enabled {
		cfg.Cluster.UsageBuffer.Enabled = true
	}
	if cfg.Cluster.UsageBuffer.MaxRecordsPerBatch == 0 {
		cfg.Cluster.UsageBuffer.MaxRecordsPerBatch = 1000
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	// Track 对话跟踪默认值：优先从 database.path 推导，peer/postgres 模式下退化为 ./track
	if cfg.Track.Dir == "" {
		if cfg.Database.Path != "" {
			cfg.Track.Dir = filepath.Join(filepath.Dir(cfg.Database.Path), "track")
		} else {
			cfg.Track.Dir = "./track"
		}
	}
	// Corpus 语料采集默认值
	if cfg.Corpus.Path == "" {
		cfg.Corpus.Path = "./corpus/"
	}
	if cfg.Corpus.MaxFileSize == "" {
		cfg.Corpus.MaxFileSize = "200MB"
	}
	if cfg.Corpus.BufferSize == 0 {
		cfg.Corpus.BufferSize = 1000
	}
	if cfg.Corpus.FlushInterval == 0 {
		cfg.Corpus.FlushInterval = 5 * time.Second
	}
	if cfg.Corpus.MinOutputTokens == 0 {
		cfg.Corpus.MinOutputTokens = 50
	}
	// SemanticRouter 语义路由默认值
	if cfg.SemanticRouter.ClassifierTimeout == 0 {
		cfg.SemanticRouter.ClassifierTimeout = 3 * time.Second
	}
	if cfg.SemanticRouter.ClassifierModel == "" {
		cfg.SemanticRouter.ClassifierModel = "claude-haiku-3-5"
	}
	// 设置默认 LLM target 权重
	for i := range cfg.LLM.Targets {
		if cfg.LLM.Targets[i].Weight == 0 {
			cfg.LLM.Targets[i].Weight = 1
		}
	}
}

// expandTilde 将路径中的 ~ 展开为用户 home 目录（跨平台）
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
