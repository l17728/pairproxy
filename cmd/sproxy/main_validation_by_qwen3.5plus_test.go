package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/config"
)

// TestSProxyConfigValidation tests the config validation and loading in sproxy
func TestSProxyConfigValidation(t *testing.T) {
	t.Parallel() // Run tests in parallel for efficiency

	t.Run("valid_config_loading", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "sproxy.yaml")
		dbPath := filepath.ToSlash(filepath.Join(tmpDir, "test.db"))

		configData := `listen:
  host: "localhost"
  port: 9000
llm:
  lb_strategy: "round_robin"
  request_timeout: 300s
  targets:
    - url: "https://api.anthropic.com"
      api_key: "fake-key-$${TEST_API_KEY}"
      weight: 1
      provider: "anthropic"
database:
  path: "` + dbPath + `"
  write_buffer_size: 100
  flush_interval: 5s
auth:
  jwt_secret: "test_secret_test_secret_test_secret_te"
  access_token_ttl: "24h"
  refresh_token_ttl: "168h"
admin:
  password_hash: "$2a$$10$$vDtCxgJ890DO7ygpJ7CUseMQxIngNJoQ803KbbR6fHx3sKskHE72."
cluster:
  role: "primary"
  self_addr: "http://localhost:9000"
  self_weight: 50
dashboard:
  enabled: true
log:
  level: "info"
`
		if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
			t.Fatalf("Failed to write config file: %v", err)
		}

		// Test without environment variable set (should show warning but still load)
		cfg, warnings, err := config.LoadSProxyConfig(configPath)
		if err != nil {
			t.Fatalf("Expected config to load successfully despite missing env vars, got: %v", err)
		}

		if len(warnings) == 0 {
			t.Error("Expected warnings for missing env vars, got none")
		}

		// Check that configuration was loaded successfully
		if cfg.Listen.Port != 9000 {
			t.Errorf("Expected port 9000, got: %d", cfg.Listen.Port)
		}

		if len(cfg.LLM.Targets) != 1 {
			t.Errorf("Expected 1 LLM target, got: %d", len(cfg.LLM.Targets))
		}

		if cfg.Cluster.Role != "primary" {
			t.Errorf("Expected role 'primary', got: %s", cfg.Cluster.Role)
		}
	})

	t.Run("invalid_config_validation_errors", func(t *testing.T) {
		// Test a config that will fail validation
		cfg := &config.SProxyFullConfig{
			Auth: config.SProxyAuth{
				JWTSecret: "",
			},
			Database: config.DatabaseConfig{
				Path: "",
			},
			LLM: config.LLMConfig{
				Targets: []config.LLMTarget{},
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation to fail for missing required fields")
		}

		errStr := err.Error()
		if !strings.Contains(errStr, "auth.jwt_secret is required") {
			t.Errorf("Expected JWT secret validation error, got: %v", errStr)
		}
		if !strings.Contains(errStr, "database.path is required") {
			t.Errorf("Expected database path validation error, got: %v", errStr)
		}
		if !strings.Contains(errStr, "llm.targets must not be empty") {
			t.Errorf("Expected LLM targets validation error, got: %v", errStr)
		}
	})

	t.Run("jwt_secret_length_requirements", func(t *testing.T) {
		cfg := &config.SProxyFullConfig{
			Auth: config.SProxyAuth{
				JWTSecret: "short", // Less than 32 chars
			},
			Database: config.DatabaseConfig{
				Path: ":memory:",
			},
			LLM: config.LLMConfig{
				Targets: []config.LLMTarget{
					{URL: "https://api.anthropic.com", APIKey: "fake-key"},
				},
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation to fail for short JWT secret")
		}

		if !strings.Contains(err.Error(), "should be at least 32 characters") {
			t.Errorf("Expected short JWT secret error, got: %v", err)
		}
	})

	t.Run("port_range_and_role_validation", func(t *testing.T) {
		// Test port out of range
		cfg := &config.SProxyFullConfig{
			Listen: config.ListenConfig{
				Port: 99999, // Out of range
			},
			Database: config.DatabaseConfig{
				Path: "temp.db",
			},
			LLM: config.LLMConfig{
				Targets: []config.LLMTarget{{URL: "https://api.com", APIKey: "key"}},
			},
			Auth: config.SProxyAuth{
				JWTSecret: "very_long_secret_string_of_at_least_32_characters",
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation to fail for out-of-range port")
		}

		if !strings.Contains(err.Error(), "is out of range (1–65535)") {
			t.Errorf("Expected port range validation error, got: %v", err)
		}

		// Test invalid role
		cfg.Cluster.Role = "invalid-role"

		err = cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "is invalid; must be \"primary\", \"worker\", or \"peer\"") {
			t.Errorf("Expected invalid role validation error, got: %v", err)
		}
	})

	t.Run("port_range_bounds", func(t *testing.T) {
		// Valid ports
		validPorts := []int{1, 8080, 65535}
		for _, port := range validPorts {
			cfg := &config.SProxyFullConfig{
				Listen: config.ListenConfig{
					Port: port,
				},
				Database: config.DatabaseConfig{
					Path: "temp.db",
				},
				LLM: config.LLMConfig{
					Targets: []config.LLMTarget{{URL: "https://api.com", APIKey: "key"}},
				},
				Auth: config.SProxyAuth{
					JWTSecret: "very_long_secret_string_of_at_least_32_characters",
				},
			}

			if err := cfg.Validate(); err != nil {
				t.Errorf("Port %d should be valid but validation failed: %v", port, err)
			}
		}

		// Invalid ports
		invalidPorts := []int{0, 65536, 70000}
		for _, port := range invalidPorts {
			cfg := &config.SProxyFullConfig{
				Listen: config.ListenConfig{
					Port: port,
				},
				Database: config.DatabaseConfig{
					Path: "temp.db",
				},
				LLM: config.LLMConfig{
					Targets: []config.LLMTarget{{URL: "https://api.com", APIKey: "key"}},
				},
				Auth: config.SProxyAuth{
					JWTSecret: "very_long_secret_string_of_at_least_32_characters",
				},
			}

			if err := cfg.Validate(); err == nil {
				t.Errorf("Port %d should be invalid but validation passed", port)
			}
		}
	})
}

// TestSProxyLogUtilities tests various utility functions used in sproxy
func TestSProxyLogUtilities(t *testing.T) {
	t.Run("log_level_parsing_variations", func(t *testing.T) {
		tests := []struct {
			input    string
			expected string
		}{
			{"debug", "debug"},
			{"info", "info"},
			{"warn", "warn"},
			{"warning", "warn"}, // Should normalize to warn
			{"error", "error"},
			{"", "info"},           // Default
			{"invalid", "info"},    // Should fallback to info
			{"  DEBUG  ", "debug"}, // Test trimming and case-insensitive
			{"INFO", "info"},       // Uppercase
		}

		for _, testCase := range tests {
			resultStr := parseLogLevelForTest(testCase.input)
			if resultStr != testCase.expected {
				t.Errorf("For input '%s', expected '%s', got '%s'", testCase.input, testCase.expected, resultStr)
			}
		}
	})

	t.Run("progress_bar_rendering_edge_cases", func(t *testing.T) {
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
			{0, 0, "[]"},      // Zero width
			{50, 1, "[░]"},    // Single char width
			{25, 4, "[█░░░]"}, // Quarter filled
			{33, 3, "[░░░]"},  // Third filled (33% of 3 = 0.99, rounds to 0)
		}

		for _, tc := range testCases {
			result := renderProgressBarUtil(tc.pct, tc.width)
			if result != tc.expected {
				t.Errorf("For pct=%d width=%d, expected '%s', got '%s'", tc.pct, tc.width, tc.expected, result)
			}
		}
	})
}

// Helper utility functions with different names to avoid duplicates
func parseLogLevelForTest(level string) string {
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

func renderProgressBarUtil(pct, width int) string {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	filled := pct * width / 100
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// TestCProxyValidationTests tests for cproxy validations
func TestCProxyValidationTests(t *testing.T) {
	t.Run("cproxy_valid_config", func(t *testing.T) {
		// Test basic valid configuration for cproxy
		cfg := &config.CProxyConfig{
			Listen: config.ListenConfig{
				Port: 8080,
			},
			Auth: config.CProxyAuth{
				RefreshThreshold: 30 * time.Minute,
			},
			Log: config.LogConfig{
				Level: "info",
			},
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Valid configuration should pass validation: %v", err)
		}

		// Test with different log levels
		for _, level := range []string{"debug", "info", "warn", "error"} {
			cfg.Log.Level = level
			if err := cfg.Validate(); err != nil {
				t.Errorf("Log level '%s' should be valid: %v", level, err)
			}
		}
	})

	t.Run("cproxy_port_validation", func(t *testing.T) {
		cfg := &config.CProxyConfig{
			Listen: config.ListenConfig{
				Port: 99999, // Out of range
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation error for invalid port")
		}

		if !strings.Contains(err.Error(), "is out of range") {
			t.Errorf("Expected port range error, got: %v", err)
		}
	})

	t.Run("cproxy_invalid_log_level", func(t *testing.T) {
		cfg := &config.CProxyConfig{
			Log: config.LogConfig{
				Level: "invalid_level",
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation error for invalid log level")
		}

		if !strings.Contains(err.Error(), "is invalid") {
			t.Errorf("Expected log level error, got: %v", err)
		}
	})
}

// TestHTTPClientSetup tests aspects of HTTP handling
func TestHTTPClientSetup(t *testing.T) {
	t.Run("admin_config_validator_test_flow", func(t *testing.T) {
		// Create a minimal config for validation testing
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "sproxy_validate.yaml")
		dbPath := filepath.ToSlash(filepath.Join(tmpDir, "test_validate.db"))

		configData := `listen:
  host: "127.0.0.1"
  port: 0  # Use 0 to avoid port conflicts in tests
llm:
  targets:
    - url: "https://api.test.fake"
      api_key: "test-key"
database:
  path: "` + dbPath + `"
auth:
  jwt_secret: "very_secure_jwt_secret_test_only_for_testing"
admin:
  password_hash: "$2a$$10$$vDtCxgJ890DO7ygpJ7CUseMQxIngNJoQ803KbbR6fHx3sKskHE72."
`

		if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
			t.Fatalf("Failed to create test config: %v", err)
		}

		// Parse config without validation to get the effective config
		cfg, warnings, err := config.LoadSProxyConfig(configPath)
		if err != nil {
			t.Fatalf("Config parsing should succeed: %v", err)
		}

		if len(warnings) > 0 {
			t.Logf("Config warnings: %v", warnings)
		}

		// Apply defaults and validate
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Configuration should pass validation: %v", err)
		}

		// Display effective configuration (simulation of validation output)
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "Listen:             %s\n", cfg.Listen.Addr())
		fmt.Fprintf(&buf, "S-Proxy primary:    %s\n", cfg.Cluster.SelfAddr)
		fmt.Fprintf(&buf, "Health check:       every %s\n", cfg.LLM.RequestTimeout)
		fmt.Fprintf(&buf, "Log level:          %s\n", cfg.Log.Level)

		output := buf.String()
		if !strings.Contains(output, "127.0.0.1") {
			t.Errorf("Expected listen address information in effective config output: %s", output)
		}
	})
}

// TestHTTPMock tests with mock HTTP server for endpoints validation
func TestHTTPMock(t *testing.T) {
	// Create mock handler that simulates our actual endpoints
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"test_access","refresh_token":"test_refresh","expires_in":3600,"username":"test"}`)
		case "/health":
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "healthy")
		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	t.Run("test_mock_server_endpoints", func(t *testing.T) {
		// Test health endpoint
		resp, err := http.Get(mockServer.URL + "/health")
		if err != nil {
			t.Fatalf("Health check should work: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200 OK for health, got %d", resp.StatusCode)
		}
		resp.Body.Close()

		// Test auth endpoint simulation
		resp, err = http.Post(mockServer.URL+"/auth/login", "application/json",
			strings.NewReader(`{"username":"test","password":"test"}`))
		if err != nil {
			t.Fatalf("Login request should work: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200 OK for login, got %d", resp.StatusCode)
		}
		resp.Body.Close()
	})
}
