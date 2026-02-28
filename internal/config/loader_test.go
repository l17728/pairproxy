package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestLoadCProxyConfig(t *testing.T) {
	yaml := `
listen:
  host: "127.0.0.1"
  port: 8080
sproxy:
  primary: "http://sp-1:9000"
  health_check_interval: 30s
  request_timeout: 300s
auth:
  token_dir: "/tmp/pairproxy"
  refresh_threshold: 30m
log:
  level: "debug"
`
	path := writeTempFile(t, yaml)
	cfg, missing, err := LoadCProxyConfig(path)
	if err != nil {
		t.Fatalf("LoadCProxyConfig: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("unexpected missing env vars: %v", missing)
	}
	if cfg.Listen.Port != 8080 {
		t.Errorf("Listen.Port = %d, want 8080", cfg.Listen.Port)
	}
	if cfg.SProxy.Primary != "http://sp-1:9000" {
		t.Errorf("SProxy.Primary = %q, want http://sp-1:9000", cfg.SProxy.Primary)
	}
	if cfg.SProxy.RequestTimeout != 300*time.Second {
		t.Errorf("RequestTimeout = %v, want 300s", cfg.SProxy.RequestTimeout)
	}
	if cfg.Log.Level != "debug" {
		t.Errorf("Log.Level = %q, want debug", cfg.Log.Level)
	}
}

func TestLoadSProxyConfig(t *testing.T) {
	yaml := `
listen:
  host: "0.0.0.0"
  port: 9000
llm:
  lb_strategy: round_robin
  targets:
    - url: "https://api.anthropic.com"
      api_key: "sk-ant-test"
      weight: 1
database:
  path: "./test.db"
auth:
  jwt_secret: "my-secret"
  access_token_ttl: 24h
cluster:
  role: primary
  alert_threshold: 80
log:
  level: info
`
	path := writeTempFile(t, yaml)
	cfg, missing, err := LoadSProxyConfig(path)
	if err != nil {
		t.Fatalf("LoadSProxyConfig: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("unexpected missing env vars: %v", missing)
	}
	if cfg.Listen.Port != 9000 {
		t.Errorf("Listen.Port = %d, want 9000", cfg.Listen.Port)
	}
	if len(cfg.LLM.Targets) != 1 {
		t.Fatalf("LLM.Targets len = %d, want 1", len(cfg.LLM.Targets))
	}
	if cfg.LLM.Targets[0].APIKey != "sk-ant-test" {
		t.Errorf("APIKey = %q, want sk-ant-test", cfg.LLM.Targets[0].APIKey)
	}
	if cfg.Cluster.Role != "primary" {
		t.Errorf("Cluster.Role = %q, want primary", cfg.Cluster.Role)
	}
}

func TestEnvVarSubstitution(t *testing.T) {
	t.Setenv("TEST_API_KEY", "sk-ant-from-env")

	yaml := `
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${TEST_API_KEY}"
      weight: 1
database:
  path: "./test.db"
auth:
  jwt_secret: "secret"
cluster:
  role: primary
`
	path := writeTempFile(t, yaml)
	cfg, missing, err := LoadSProxyConfig(path)
	if err != nil {
		t.Fatalf("LoadSProxyConfig: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("unexpected missing env vars: %v", missing)
	}
	if cfg.LLM.Targets[0].APIKey != "sk-ant-from-env" {
		t.Errorf("APIKey = %q, want sk-ant-from-env", cfg.LLM.Targets[0].APIKey)
	}
}

func TestEnvVarMissingReported(t *testing.T) {
	// 确保变量不存在
	os.Unsetenv("NONEXISTENT_VAR_XYZ")

	yaml := `
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "${NONEXISTENT_VAR_XYZ}"
database:
  path: "./test.db"
auth:
  jwt_secret: "secret"
cluster:
  role: primary
`
	path := writeTempFile(t, yaml)
	_, missing, err := LoadSProxyConfig(path)
	if err != nil {
		t.Fatalf("LoadSProxyConfig: %v", err)
	}
	found := false
	for _, v := range missing {
		if v == "NONEXISTENT_VAR_XYZ" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected NONEXISTENT_VAR_XYZ in missing list, got: %v", missing)
	}
}

func TestDefaultConfigDir(t *testing.T) {
	dir := DefaultConfigDir()
	if dir == "" {
		t.Error("DefaultConfigDir() returned empty string")
	}
	// 路径应包含 "pairproxy"
	base := filepath.Base(dir)
	if base != "pairproxy" {
		t.Errorf("DefaultConfigDir() base = %q, want pairproxy", base)
	}
}

func TestApplyDefaultsCProxy(t *testing.T) {
	// 空配置应填充所有默认值
	yaml := `{}`
	path := writeTempFile(t, yaml)
	cfg, _, err := LoadCProxyConfig(path)
	if err != nil {
		t.Fatalf("LoadCProxyConfig: %v", err)
	}
	if cfg.Listen.Host != "127.0.0.1" {
		t.Errorf("default Host = %q, want 127.0.0.1", cfg.Listen.Host)
	}
	if cfg.Listen.Port != 8080 {
		t.Errorf("default Port = %d, want 8080", cfg.Listen.Port)
	}
	if cfg.SProxy.RequestTimeout != 300*time.Second {
		t.Errorf("default RequestTimeout = %v, want 300s", cfg.SProxy.RequestTimeout)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("default Log.Level = %q, want info", cfg.Log.Level)
	}
}

// ---------------------------------------------------------------------------
// Negative / edge-case tests
// ---------------------------------------------------------------------------

// TestLoadCProxyConfig_FileNotFound verifies that a missing config file returns an error.
func TestLoadCProxyConfig_FileNotFound(t *testing.T) {
	_, _, err := LoadCProxyConfig("/nonexistent/path/that/does/not/exist/cproxy.yaml")
	if err == nil {
		t.Error("LoadCProxyConfig should return error for non-existent file")
	}
}

// TestLoadSProxyConfig_FileNotFound verifies that a missing config file returns an error.
func TestLoadSProxyConfig_FileNotFound(t *testing.T) {
	_, _, err := LoadSProxyConfig("/nonexistent/path/that/does/not/exist/sproxy.yaml")
	if err == nil {
		t.Error("LoadSProxyConfig should return error for non-existent file")
	}
}

// TestLoadCProxyConfig_InvalidYAML verifies that malformed YAML returns an error.
func TestLoadCProxyConfig_InvalidYAML(t *testing.T) {
	path := writeTempFile(t, "listen:\n  :\nbad: [[[unclosed bracket")
	_, _, err := LoadCProxyConfig(path)
	if err == nil {
		t.Error("LoadCProxyConfig should return error for invalid YAML")
	}
}

// TestLoadSProxyConfig_InvalidYAML verifies that malformed YAML returns an error.
func TestLoadSProxyConfig_InvalidYAML(t *testing.T) {
	path := writeTempFile(t, "database:\n  :\nbad: [[[unclosed bracket")
	_, _, err := LoadSProxyConfig(path)
	if err == nil {
		t.Error("LoadSProxyConfig should return error for invalid YAML")
	}
}

// TestApplyDefaultsSProxy verifies that an empty s-proxy config gets sensible defaults.
func TestApplyDefaultsSProxy(t *testing.T) {
	path := writeTempFile(t, "{}")
	cfg, _, err := LoadSProxyConfig(path)
	if err != nil {
		t.Fatalf("LoadSProxyConfig: %v", err)
	}

	if cfg.Listen.Host != "0.0.0.0" {
		t.Errorf("default Host = %q, want 0.0.0.0", cfg.Listen.Host)
	}
	if cfg.Listen.Port != 9000 {
		t.Errorf("default Port = %d, want 9000", cfg.Listen.Port)
	}
	if cfg.LLM.LBStrategy != "round_robin" {
		t.Errorf("default LBStrategy = %q, want round_robin", cfg.LLM.LBStrategy)
	}
	if cfg.LLM.RequestTimeout != 300*time.Second {
		t.Errorf("default RequestTimeout = %v, want 300s", cfg.LLM.RequestTimeout)
	}
	if cfg.Auth.AccessTokenTTL != 24*time.Hour {
		t.Errorf("default AccessTokenTTL = %v, want 24h", cfg.Auth.AccessTokenTTL)
	}
	if cfg.Auth.RefreshTokenTTL != 168*time.Hour {
		t.Errorf("default RefreshTokenTTL = %v, want 168h", cfg.Auth.RefreshTokenTTL)
	}
	if cfg.Cluster.Role != "primary" {
		t.Errorf("default Cluster.Role = %q, want primary", cfg.Cluster.Role)
	}
	if cfg.Database.WriteBufferSize != 200 {
		t.Errorf("default WriteBufferSize = %d, want 200", cfg.Database.WriteBufferSize)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("default Log.Level = %q, want info", cfg.Log.Level)
	}
}

// TestEnvVarMultipleMissing verifies that all missing env var names are reported.
func TestEnvVarMultipleMissing(t *testing.T) {
	t.Setenv("PP_MISSING_A", "") // ensure not set; Unsetenv more correct
	os.Unsetenv("PP_MISSING_A")
	os.Unsetenv("PP_MISSING_B")

	yaml := `
llm:
  targets:
    - url: "${PP_MISSING_A}"
      api_key: "${PP_MISSING_B}"
database:
  path: "./test.db"
auth:
  jwt_secret: "secret"
cluster:
  role: primary
`
	path := writeTempFile(t, yaml)
	_, missing, err := LoadSProxyConfig(path)
	if err != nil {
		t.Fatalf("LoadSProxyConfig: %v", err)
	}

	wantMissing := map[string]bool{"PP_MISSING_A": true, "PP_MISSING_B": true}
	for _, v := range missing {
		delete(wantMissing, v)
	}
	if len(wantMissing) > 0 {
		t.Errorf("missing var(s) not reported: %v (got: %v)", wantMissing, missing)
	}
}

// TestExpandTilde verifies that tilde paths are expanded to the home directory.
func TestExpandTilde(t *testing.T) {
	result := expandTilde("~/.pairproxy/token.json")
	if result == "~/.pairproxy/token.json" {
		t.Error("expandTilde should expand ~ to the home directory")
	}
	if !filepath.IsAbs(result) {
		t.Errorf("expandTilde result %q should be absolute path", result)
	}
}

// TestExpandTildeNoPrefix verifies that paths without ~ are returned unchanged.
func TestExpandTildeNoPrefix(t *testing.T) {
	input := "/absolute/path/to/file"
	if result := expandTilde(input); result != input {
		t.Errorf("expandTilde(%q) = %q, want unchanged", input, result)
	}
}

func TestPricingComputeCost(t *testing.T) {
	p := PricingConfig{
		Models: map[string]ModelPrice{
			"claude-3-5-sonnet": {InputPer1K: 3.0, OutputPer1K: 15.0},
		},
		DefaultInputPer1K:  1.0,
		DefaultOutputPer1K: 5.0,
	}

	// 已知模型：1000 input + 500 output
	cost := p.ComputeCost("claude-3-5-sonnet", 1000, 500)
	want := 1000.0/1000*3.0 + 500.0/1000*15.0 // 3.0 + 7.5 = 10.5
	if cost != want {
		t.Errorf("ComputeCost(known model) = %f, want %f", cost, want)
	}

	// 未知模型 → 使用默认定价
	cost2 := p.ComputeCost("unknown-model", 2000, 1000)
	want2 := 2000.0/1000*1.0 + 1000.0/1000*5.0 // 2.0 + 5.0 = 7.0
	if cost2 != want2 {
		t.Errorf("ComputeCost(unknown model) = %f, want %f", cost2, want2)
	}

	// 零 token → 零费用
	if cost3 := p.ComputeCost("claude-3-5-sonnet", 0, 0); cost3 != 0 {
		t.Errorf("ComputeCost(0 tokens) = %f, want 0", cost3)
	}
}
