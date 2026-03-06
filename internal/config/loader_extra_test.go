package config

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// DefaultCProxyConfigPath / DefaultSProxyConfigPath
// ---------------------------------------------------------------------------

func TestDefaultCProxyConfigPath_ContainsCproxy(t *testing.T) {
	path := DefaultCProxyConfigPath()
	if path == "" {
		t.Fatal("DefaultCProxyConfigPath() should not be empty")
	}
	base := filepath.Base(path)
	if base != "cproxy.yaml" {
		t.Errorf("DefaultCProxyConfigPath() filename = %q, want 'cproxy.yaml'", base)
	}
}

func TestDefaultSProxyConfigPath_ContainsSproxy(t *testing.T) {
	path := DefaultSProxyConfigPath()
	if path == "" {
		t.Fatal("DefaultSProxyConfigPath() should not be empty")
	}
	base := filepath.Base(path)
	if base != "sproxy.yaml" {
		t.Errorf("DefaultSProxyConfigPath() filename = %q, want 'sproxy.yaml'", base)
	}
}

// ---------------------------------------------------------------------------
// ParseCProxyConfig — 无验证解析（ParseCProxyConfig 不调用 Validate）
// ---------------------------------------------------------------------------

func TestParseCProxyConfig_ValidFile(t *testing.T) {
	yaml := `
listen:
  host: "127.0.0.1"
  port: 8081
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cproxy.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, missing, err := ParseCProxyConfig(path)
	if err != nil {
		t.Fatalf("ParseCProxyConfig: %v", err)
	}
	if len(missing) != 0 {
		t.Errorf("unexpected missing env vars: %v", missing)
	}
	if cfg.Listen.Port != 8081 {
		t.Errorf("Listen.Port = %d, want 8081", cfg.Listen.Port)
	}
}

func TestParseCProxyConfig_FileNotFound_ReturnsError(t *testing.T) {
	_, _, err := ParseCProxyConfig("/nonexistent/path/cproxy.yaml")
	if err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestParseCProxyConfig_InvalidYAML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cproxy.yaml")
	if err := os.WriteFile(path, []byte("{invalid: yaml: content:"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, _, err := ParseCProxyConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParseCProxyConfig_WithMissingEnvVars(t *testing.T) {
	yaml := `
listen:
  port: 8082
sproxy:
  primary: "https://sproxy.example.com"
auth:
  token_dir: "${NONEXISTENT_DIR_PARSE}"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "cproxy.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 确保环境变量不存在
	os.Unsetenv("NONEXISTENT_DIR_PARSE")

	cfg, missing, err := ParseCProxyConfig(path)
	if err != nil {
		t.Fatalf("ParseCProxyConfig: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	// missing 应包含 NONEXISTENT_DIR_PARSE
	found := false
	for _, m := range missing {
		if m == "NONEXISTENT_DIR_PARSE" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected NONEXISTENT_DIR_PARSE in missing vars, got %v", missing)
	}
}

// ---------------------------------------------------------------------------
// DefaultConfigDir — 覆盖 UserConfigDir 失败分支（通过环境变量模拟）
// ---------------------------------------------------------------------------

func TestDefaultConfigDir_NonEmpty(t *testing.T) {
	dir := DefaultConfigDir()
	if dir == "" {
		t.Fatal("DefaultConfigDir() should not return empty string")
	}
	if filepath.Base(dir) != "pairproxy" {
		t.Errorf("DefaultConfigDir() last segment = %q, want 'pairproxy'", filepath.Base(dir))
	}
}

// ---------------------------------------------------------------------------
// expandTilde — 覆盖 ~ 展开场景
// ---------------------------------------------------------------------------

func TestExpandTilde_AbsPath_Unchanged(t *testing.T) {
	result := expandTilde("/absolute/path/config")
	if result != "/absolute/path/config" {
		t.Errorf("expandTilde of absolute path = %q, should be unchanged", result)
	}
}

func TestExpandTilde_TildeSlash_ExpandsToHome(t *testing.T) {
	result := expandTilde("~/pairproxy/config")
	if result == "" {
		t.Fatal("expandTilde should return non-empty result")
	}
	if result == "~/pairproxy/config" {
		t.Error("expandTilde should have expanded ~ prefix")
	}
}

func TestExpandTilde_NoPrefix_Unchanged(t *testing.T) {
	result := expandTilde("relative/path")
	if result != "relative/path" {
		t.Errorf("expandTilde without ~ prefix = %q, should be unchanged", result)
	}
}
