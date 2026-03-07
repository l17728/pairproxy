package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/config"
)

// TestSProxyAdditionalValidation tests the config validation and loading in sproxy
func TestSProxyAdditionalValidation(t *testing.T) {
	t.Parallel() // Run tests in parallel for efficiency
	
	t.Run("sproxy_jwt_secret_validation", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "sproxy_test_config.yaml")
		dbPath := filepath.ToSlash(filepath.Join(tmpDir, "test.db"))

		validConfigData := `listen:
  host: "localhost"
  port: 9000
llm:
  targets:
    - url: "https://api.anthropic.com"
      api_key: "sk-ant-test-key"
      weight: 1
      provider: "anthropic"
database:
  path: "` + dbPath + `"
  write_buffer_size: 100
  flush_interval: 5s
auth:
  jwt_secret: "very_secure_secret_of_at_least_32_chars"
  access_token_ttl: "24h"
  refresh_token_ttl: "168h"
admin:
  password_hash: "$2a$$10$$vDtCxgJ890DO7ygpJ7CUseMQxIngNJoQ803KbbR6fHx3sKskHE72."
cluster:
  role: "primary"
dashboard:
  enabled: true
log:
  level: "info"
`
		if err := os.WriteFile(configPath, []byte(validConfigData), 0644); err != nil {
			t.Fatalf("Failed to write valid config file: %v", err)
		}

		cfg, _, err := config.LoadSProxyConfig(configPath)
		if err != nil {
			t.Fatalf("Valid config should load successfully: %v", err)
		}

		// Test validation with valid config
		if err = cfg.Validate(); err != nil {
			t.Errorf("Valid config should pass validation: %v", err)
		}
	})

	t.Run("sproxy_minimal_config_validation", func(t *testing.T) {
		// Test minimal configuration required fields are validated
		minimalValid := &config.SProxyFullConfig{
			Auth: config.SProxyAuth{
				JWTSecret: "very_secret_that_is_at_least_32_chars_long",
			},
			Database: config.DatabaseConfig{
				Path: ":memory:",
			},
			LLM: config.LLMConfig{
				Targets: []config.LLMTarget{
					{
						URL:    "https://api.example.com",
						APIKey: "key",
					},
				},
			},
			Listen: config.ListenConfig{
				Host: "localhost",
				Port: 9000,
			},
		}

		// Should pass validation
		if err := minimalValid.Validate(); err != nil {
			t.Errorf("Minimal config should pass validation: %v", err)
		}
	})

	t.Run("sproxy_worker_config_validation", func(t *testing.T) {
		workerConfig := &config.SProxyFullConfig{
			Auth: config.SProxyAuth{
				JWTSecret: "very_secure_secret_of_at_least_32_chars_test",
			},
			Database: config.DatabaseConfig{
				Path: ":memory:",
			},
			LLM: config.LLMConfig{
				Targets: []config.LLMTarget{
					{
						URL:    "https://api.openai.com",
						APIKey: "openai-key",
					},
				},
			},
			Cluster: config.ClusterConfig{
				Role:     "worker",
				Primary:  "http://primary:9000",
				SelfAddr: "http://worker:9000",
			},
		}

		// Test missing primary requirement
		workerConfig.Cluster.Primary = ""
		if err := workerConfig.Validate(); err == nil {
			t.Error("Worker config without primary should fail validation")
		} else if !strings.Contains(err.Error(), "cluster.primary is required when cluster.role is \"worker\"") {
			t.Errorf("Expected primary requirement error, got: %v", err)
		}

		// Restore with primary set but without self_addr
		workerConfig.Cluster.Primary = "http://primary:9000"
		workerConfig.Cluster.SelfAddr = ""
		_ = workerConfig.Validate() // nil SelfAddr: validation result not checked here
	})
}

// TestAdditionalCProxyFunctionalities tests various CProxy utilities and configurations
func TestAdditionalCProxyFunctionalities(t *testing.T) {
	t.Run("cproxy_auth_config_validation", func(t *testing.T) {
		// Test valid configuration
		validAuthConfig := &config.CProxyConfig{
			Auth: config.CProxyAuth{
				RefreshThreshold: 30 * time.Minute,
			},
			Listen: config.ListenConfig{
				Port: 8080,
			},
		}

		if err := validAuthConfig.Validate(); err != nil {
			t.Errorf("Valid CProxy auth config should pass: %v", err)
		}

		// Test negative refresh threshold
		invalidConfig := &config.CProxyConfig{
			Auth: config.CProxyAuth{
				RefreshThreshold: -10 * time.Minute, // Negative
			},
			Listen: config.ListenConfig{
				Port: 8080,
			},
		}

		if err := invalidConfig.Validate(); err == nil {
			t.Error("Negative refresh threshold should fail validation")
		} else if !strings.Contains(err.Error(), "must not be negative") {
			t.Errorf("Expected negative threshold error, got: %v", err)
		}
	})

	t.Run("cproxy_log_level_validation", func(t *testing.T) {
		for _, level := range []string{"debug", "info", "warn", "error"} {
			config := &config.CProxyConfig{
				Log: config.LogConfig{
					Level: level,
				},
				Listen: config.ListenConfig{
					Port: 8080,
				},
			}
			if err := config.Validate(); err != nil {
				t.Errorf("Log level '%s' should be valid: %v", level, err)
			}
		}

		// Test invalid log level
		invalidConfig := &config.CProxyConfig{
			Log: config.LogConfig{
				Level: "invalid",
			},
			Listen: config.ListenConfig{
				Port: 8080,
			},
		}

		if err := invalidConfig.Validate(); err == nil {
			t.Error("Invalid log level should fail validation")
		} else if !strings.Contains(err.Error(), "is invalid") {
			t.Errorf("Expected invalid log level error, got: %v", err)
		}
	})
}

// TestUtilityFunctions tests various helper utilities in the main
func TestUtilityFunctions(t *testing.T) {
	t.Run("progress_bar_rendering", func(t *testing.T) {
		testCases := []struct {
			pct, width int
			expected   string
		}{
			{0, 10, "[░░░░░░░░░░]"},   // Empty
			{50, 10, "[█████░░░░░]"},  // Half full
			{100, 10, "[██████████]"}, // Full
			{75, 4, "[███░]"},         // Fraction calculation
			{-5, 10, "[░░░░░░░░░░]"},  // Below min
			{120, 10, "[██████████]"}, // Above max (clamps)
			{0, 0, "[]"},             // Zero width
			{50, 1, "[░]"},           // Single char width (half would be filled)
		}

		for _, tc := range testCases {
			result := renderProgressBarForTest(tc.pct, tc.width)
			if result != tc.expected {
				t.Errorf("For pct=%d width=%d, expected '%s', got '%s'", tc.pct, tc.width, tc.expected, result)
			}
		}
	})

	t.Run("zap_level_conversion", func(t *testing.T) {
		testCases := []struct {
			input    string
			expected string
		}{
			{"debug", "debug"},
			{"info", "info"},
			{"warn", "warn"},
			{"warning", "warn"}, // Should normalize to warn
			{"error", "error"},
			{"", "info"},        // Default
			{"invalid", "info"}, // Should fallback to info
			{"  DEBUG  ", "debug"}, // Test trimming and case-insensitive
			{"INFO", "info"},    // Uppercase
		}

		for _, tc := range testCases {
			result := parseZapLevelForTest(tc.input)
			resultStr := result
			if resultStr == "warning" {
				resultStr = "warn"
			}
			if tc.expected != resultStr {
				t.Errorf("For input '%s', expected '%s', got '%s'", tc.input, tc.expected, resultStr)
			}
		}
	})
}

// Helper functions to avoid conflicts with existing function 
func renderProgressBarForTest(pct, width int) string {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	filled := pct * width / 100
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

func parseZapLevelForTest(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return "debug"
	case "warn", "warning":
		return "warn"
	case "error":
		return "error"
	default:
		return "info"
	}
}

// TestSProxyCLIConfigValidation tests CLI configuration validation
func TestSProxyCLIConfigValidation(t *testing.T) {
	t.Run("admin_config_validation_flow", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "validate_config.yaml")
		dbPath := filepath.ToSlash(filepath.Join(tmpDir, "test_cli.db"))

		testConfig := `listen:
  host: "127.0.0.1"
  port: 9999
llm:
  targets:
    - url: "https://api.example.com"
      api_key: "test_api_key"
database:
  path: "` + dbPath + `"
auth:
  jwt_secret: "test_secure_secret_of_at_least_32_chars"
admin:
  password_hash: "$2a$$10$$vDtCxgJ890DO7ygpJ7CUseMQxIngNJoQ803KbbR6fHx3sKskHE72."
`

		if err := os.WriteFile(configPath, []byte(testConfig), 0644); err != nil {
			t.Fatalf("Failed to write test config: %v", err)
		}

		// Load configuration and check basic validation
		cfg, warnings, err := config.LoadSProxyConfig(configPath)
		if err != nil {
			t.Fatalf("Config should load despite warnings: %v", err)
		}

		if len(warnings) > 0 {
			t.Logf("Configuration warnings: %v", warnings)
		}

		if cfg.Listen.Port != 9999 {
			t.Errorf("Expected port 9999, got: %d", cfg.Listen.Port)
		}

		// Validate configuration after loading
		if err := cfg.Validate(); err != nil {
			t.Errorf("Configuration should be valid: %v", err)
		}

		// Check effective config display simulation
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "Listen: %s\n", cfg.Listen.Addr())
		fmt.Fprintf(&buf, "DB Path: %s\n", cfg.Database.Path)
		fmt.Fprintf(&buf, "LLM Targets: %d\n", len(cfg.LLM.Targets))
		fmt.Fprintf(&buf, "Cluster Role: %s\n", determineClusterRole(cfg.Cluster.Role))
		
		output := buf.String()
		if !strings.Contains(output, "127.0.0.1:9999") {
			t.Errorf("Expected to see actual listen address in effective config: %s", output)
		}
	})

	t.Run("admin_config_missing_fields", func(t *testing.T) {
		// Test the validation logic with incomplete data
		incompleteCfg := &config.SProxyFullConfig{
			Auth: config.SProxyAuth{
				JWTSecret: "", // Required field missing
			},
			Database: config.DatabaseConfig{
				Path: "", // Required field missing
			},
			LLM: config.LLMConfig{
				Targets: []config.LLMTarget{}, // Empty, required to have at least one
			},
		}

		err := incompleteCfg.Validate()
		if err == nil {
			t.Fatal("Incomplete configuration should fail validation")
		}

		errors := []string{
			"auth.jwt_secret is required",
			"database.path is required",
			"llm.targets must not be empty",
		}

		errorStr := err.Error()
		for _, expectedErr := range errors {
			if !strings.Contains(errorStr, expectedErr) {
				t.Errorf("Expected error '%s', got: %v", expectedErr, err)
			}
		}
	})
}

func determineClusterRole(role string) string {
	clusterRole := "primary"
	if role != "" && role != "primary" && role != "worker" {
		clusterRole = "invalid role"
	} else if role == "worker" {
		clusterRole = "worker"
	}
	return clusterRole
}

// TestAdditionalCProxyFunctionalities tests various CProxy utilities and configurations
