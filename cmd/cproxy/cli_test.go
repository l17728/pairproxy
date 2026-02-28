package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// renderProgressBar
// ---------------------------------------------------------------------------

func TestRenderProgressBar_Boundaries(t *testing.T) {
	tests := []struct {
		name  string
		pct   int
		width int
		want  string
	}{
		{"0%", 0, 10, "[░░░░░░░░░░]"},
		{"100%", 100, 10, "[██████████]"},
		{"50%", 50, 10, "[█████░░░░░]"},
		{"80% 20-wide", 80, 20, "[████████████████░░░░]"},
		{"negative clamp", -5, 10, "[░░░░░░░░░░]"},
		{"over-100 clamp", 150, 10, "[██████████]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderProgressBar(tc.pct, tc.width)
			if got != tc.want {
				t.Errorf("renderProgressBar(%d, %d) = %q, want %q", tc.pct, tc.width, got, tc.want)
			}
		})
	}
}

func TestRenderProgressBar_Width(t *testing.T) {
	// Total length should always be width + 2 (for the brackets)
	for _, width := range []int{5, 10, 20, 30} {
		bar := renderProgressBar(50, width)
		wantLen := width + 2
		// Count runes (not bytes) since we use multi-byte Unicode block chars
		runes := []rune(bar)
		if len(runes) != wantLen {
			t.Errorf("renderProgressBar(50, %d) rune length = %d, want %d (bar: %q)",
				width, len(runes), wantLen, bar)
		}
	}
}

// ---------------------------------------------------------------------------
// runConfigValidate (integration-style, uses temp YAML file)
// ---------------------------------------------------------------------------

func TestRunConfigValidate_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cproxy.yaml")
	content := `
listen:
  host: "127.0.0.1"
  port: 8080
sproxy:
  primary: "http://proxy.company.com:9000"
auth:
  refresh_threshold: 30m
log:
  level: "info"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	configValidateConfigFlag = cfgPath
	defer func() { configValidateConfigFlag = "" }()

	// Should not return error for valid config
	if err := runConfigValidate(nil, nil); err != nil {
		t.Errorf("valid config should not fail validation, got: %v", err)
	}
}

func TestRunConfigValidate_InvalidPort(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cproxy.yaml")
	content := `
listen:
  host: "127.0.0.1"
  port: 99999
sproxy:
  primary: "http://proxy.company.com:9000"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	configValidateConfigFlag = cfgPath
	defer func() { configValidateConfigFlag = "" }()

	err := runConfigValidate(nil, nil)
	if err == nil {
		t.Error("expected error for invalid port, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention config is invalid, got: %v", err)
	}
}

func TestRunConfigValidate_InvalidLogLevel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cproxy.yaml")
	content := `
listen:
  host: "127.0.0.1"
  port: 8080
sproxy:
  primary: "http://proxy.company.com:9000"
log:
  level: "verbose"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	configValidateConfigFlag = cfgPath
	defer func() { configValidateConfigFlag = "" }()

	err := runConfigValidate(nil, nil)
	if err == nil {
		t.Error("expected error for invalid log level, got nil")
	}
}
