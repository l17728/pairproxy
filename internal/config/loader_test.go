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
