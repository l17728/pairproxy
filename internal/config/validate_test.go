package config

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// SProxyFullConfig.Validate() 测试
// ---------------------------------------------------------------------------

func TestValidateSProxy_Valid(t *testing.T) {
	cfg := validSProxyCfg()
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config should pass validation, got: %v", err)
	}
}

func TestValidateSProxy_MissingJWTSecret(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.Auth.JWTSecret = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing jwt_secret, got nil")
	}
	if !strings.Contains(err.Error(), "jwt_secret") {
		t.Errorf("error should mention jwt_secret, got: %v", err)
	}
}

func TestValidateSProxy_MissingDatabasePath(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.Database.Path = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing database.path, got nil")
	}
	if !strings.Contains(err.Error(), "database.path") {
		t.Errorf("error should mention database.path, got: %v", err)
	}
}

func TestValidateSProxy_EmptyLLMTargets(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.LLM.Targets = nil
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty llm.targets, got nil")
	}
	if !strings.Contains(err.Error(), "llm.targets") {
		t.Errorf("error should mention llm.targets, got: %v", err)
	}
}

func TestValidateSProxy_InvalidPort(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.Listen.Port = 99999
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for out-of-range port, got nil")
	}
	if !strings.Contains(err.Error(), "listen.port") {
		t.Errorf("error should mention listen.port, got: %v", err)
	}
}

func TestValidateSProxy_WorkerMissingPrimary(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.Cluster.Role = "worker"
	cfg.Cluster.Primary = "" // worker 模式下必须设置 primary
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for worker without primary, got nil")
	}
	if !strings.Contains(err.Error(), "cluster.primary") {
		t.Errorf("error should mention cluster.primary, got: %v", err)
	}
}

func TestValidateSProxy_WorkerWithPrimary(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.Cluster.Role = "worker"
	cfg.Cluster.Primary = "http://sp-1:9000"
	if err := cfg.Validate(); err != nil {
		t.Errorf("worker with primary should pass validation, got: %v", err)
	}
}

func TestValidateSProxy_InvalidRole(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.Cluster.Role = "unknown-role"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid cluster role, got nil")
	}
	if !strings.Contains(err.Error(), "cluster.role") {
		t.Errorf("error should mention cluster.role, got: %v", err)
	}
}

// TestValidateSProxy_MultipleErrors 验证多个校验错误同时返回，而非只返回第一个。
func TestValidateSProxy_MultipleErrors(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.Auth.JWTSecret = ""
	cfg.Database.Path = ""
	cfg.LLM.Targets = nil
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected multiple validation errors, got nil")
	}
	// 错误信息应包含全部缺失字段
	for _, keyword := range []string{"jwt_secret", "database.path", "llm.targets"} {
		if !strings.Contains(err.Error(), keyword) {
			t.Errorf("error should mention %q, got: %v", keyword, err)
		}
	}
}

// ---------------------------------------------------------------------------
// CProxyConfig.Validate() 测试
// ---------------------------------------------------------------------------

func TestValidateCProxy_Valid(t *testing.T) {
	cfg := &CProxyConfig{}
	cfg.Listen.Port = 8080
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid cproxy config should pass, got: %v", err)
	}
}

func TestValidateCProxy_InvalidPort(t *testing.T) {
	cfg := &CProxyConfig{}
	cfg.Listen.Port = 0 // 无效端口（applyDefaults 会将其设为 8080，但若绕过 defaults 直接调用 Validate 则会触发）
	// 0 会被 applyDefaults 修正，所以用明确超界值测试
	cfg.Listen.Port = 70000
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for port 70000, got nil")
	}
	if !strings.Contains(err.Error(), "listen.port") {
		t.Errorf("error should mention listen.port, got: %v", err)
	}
}

func TestValidateCProxy_ValidAfterDefaults(t *testing.T) {
	// 模拟 LoadCProxyConfig 流程：先 applyDefaults 再 Validate
	cfg := &CProxyConfig{}
	applyDefaults(cfg) // port 被设为 8080
	if err := cfg.Validate(); err != nil {
		t.Errorf("config after applyDefaults should pass validation, got: %v", err)
	}
}

// TestValidateSProxy_EmptyAPIKey 验证 llm.targets[i].api_key 为空时返回错误。
func TestValidateSProxy_EmptyAPIKey(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.LLM.Targets[0].APIKey = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty api_key, got nil")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("error should mention api_key, got: %v", err)
	}
}

// TestValidateSProxy_EmptyTargetURL 验证 llm.targets[i].url 为空时返回错误。
func TestValidateSProxy_EmptyTargetURL(t *testing.T) {
	cfg := validSProxyCfg()
	cfg.LLM.Targets[0].URL = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for empty target url, got nil")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error should mention url, got: %v", err)
	}
}

func TestValidateCProxy_InvalidLogLevel(t *testing.T) {
	cfg := &CProxyConfig{}
	applyDefaults(cfg)
	cfg.Log.Level = "verbose" // invalid
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid log level, got nil")
	}
	if !strings.Contains(err.Error(), "log.level") {
		t.Errorf("error should mention log.level, got: %v", err)
	}
}

func TestValidateCProxy_NegativeRefreshThreshold(t *testing.T) {
	cfg := &CProxyConfig{}
	applyDefaults(cfg)
	cfg.Auth.RefreshThreshold = -1 * time.Minute
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for negative refresh_threshold, got nil")
	}
	if !strings.Contains(err.Error(), "refresh_threshold") {
		t.Errorf("error should mention refresh_threshold, got: %v", err)
	}
}

func TestValidateCProxy_MultipleErrors(t *testing.T) {
	cfg := &CProxyConfig{}
	cfg.Listen.Port = 70000
	cfg.Log.Level = "verbose"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected multiple errors, got nil")
	}
	for _, keyword := range []string{"listen.port", "log.level"} {
		if !strings.Contains(err.Error(), keyword) {
			t.Errorf("error should mention %q, got: %v", keyword, err)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func validSProxyCfg() *SProxyFullConfig {
	return &SProxyFullConfig{
		Listen: ListenConfig{Host: "0.0.0.0", Port: 9000},
		Auth:   SProxyAuth{JWTSecret: "test-secret"},
		Database: DatabaseConfig{
			Path:            "/tmp/test.db",
			WriteBufferSize: 200,
		},
		LLM: LLMConfig{
			Targets: []LLMTarget{{URL: "https://api.anthropic.com", APIKey: "sk-ant-test", Weight: 1}},
		},
		Cluster: ClusterConfig{Role: "primary"},
	}
}
