package config

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestPricingConfig tests pricing-related configuration
func TestPricingConfig(t *testing.T) {
	t.Run("compute_cost_standard_models", func(t *testing.T) {
		cfg := &PricingConfig{
			Models: map[string]ModelPrice{
				"claude-3-5-sonnet-20240620": {InputPer1K: 0.003, OutputPer1K: 0.015},
				"gpt-4o":                     {InputPer1K: 0.005, OutputPer1K: 0.015},
			},
			DefaultInputPer1K:  0.001,
			DefaultOutputPer1K: 0.003,
		}

		// Test matched model
		cost1 := cfg.ComputeCost("claude-3-5-sonnet-20240620", 1000, 2000)
		expected1 := (1000.0/1000)*0.003 + (2000.0/1000)*0.015 // 0.003 + 0.030 = 0.033
		if cost1 != expected1 {
			t.Errorf("Expected %f, got %f for sonnet model", expected1, cost1)
		}

		// Test unmatched model (defaults)
		cost2 := cfg.ComputeCost("unknown-model", 500, 1000)
		expected2 := (500.0/1000)*0.001 + (1000.0/1000)*0.003 // 0.0005 + 0.003 = 0.0035
		if cost2 != expected2 {
			t.Errorf("Expected %f, got %f for unknown model", expected2, cost2)
		}

		// Edge cases
		zeroCost := cfg.ComputeCost("claude-3-5-sonnet-20240620", 0, 0)
		if zeroCost != 0 {
			t.Errorf("Expected 0 for zero tokens, got %f", zeroCost)
		}
	})

	t.Run("compute_cost_edge_cases", func(t *testing.T) {
		cfg := &PricingConfig{
			Models: map[string]ModelPrice{
				"gpt-4o": {InputPer1K: 0.005, OutputPer1K: 0.015},
			},
			DefaultInputPer1K:  0, // Zero defaults
			DefaultOutputPer1K: 0,
		}

		// Zero costs
		cost := cfg.ComputeCost("nonexistent", 1000, 1000)
		if cost != 0 {
			t.Errorf("Expected 0 for zero-rate defaults, got %f", cost)
		}

		// Large token counts
		bigCost := cfg.ComputeCost("gpt-4o", 500000, 300000)      // 500k input, 300k output
		expected := (500000.0/1000)*0.005 + (300000.0/1000)*0.015 // 2.5 + 4.5 = 7.0
		if bigCost != expected {
			t.Errorf("Expected %f for large token counts, got %f", expected, bigCost)
		}
	})
}

// TestLLMTarget tests the LLM target configuration
func TestLLMTarget(t *testing.T) {
	t.Run("llm_target_defaults", func(t *testing.T) {
		// Full spec
		target := LLMTarget{
			URL:             "https://api.anthropic.com",
			APIKey:          "${ANTHROPIC_KEY}",
			Weight:          5,
			Provider:        "anthropic",
			Name:            "Claude Pro",
			HealthCheckPath: "/health",
		}

		expectedFields := map[string]interface{}{
			"url":               "https://api.anthropic.com",
			"weight":            5,
			"provider":          "anthropic",
			"name":              "Claude Pro",
			"health_check_path": "/health",
		}

		if target.URL != expectedFields["url"].(string) {
			t.Errorf("Expected URL %s, got %s", expectedFields["url"], target.URL)
		}
		if target.Weight != expectedFields["weight"].(int) {
			t.Errorf("Expected Weight %v, got %v", expectedFields["weight"], target.Weight)
		}
		if target.Provider != expectedFields["provider"].(string) {
			t.Errorf("Expected Provider %s, got %s", expectedFields["provider"], target.Provider)
		}
		if target.Name != expectedFields["name"].(string) {
			t.Errorf("Expected Name %s, got %s", expectedFields["name"], target.Name)
		}
		if target.HealthCheckPath != expectedFields["health_check_path"].(string) {
			t.Errorf("Expected HealthCheckPath %s, got %s", expectedFields["health_check_path"], target.HealthCheckPath)
		}
	})

	t.Run("provider_specific_defaults", func(t *testing.T) {
		providers := []string{"anthropic", "openai", "ollama"}
		for _, provider := range providers {
			target := LLMTarget{
				URL:      "https://api." + provider + ".com",
				Provider: provider,
			}
			if target.Provider != provider {
				t.Errorf("Provider not preserved, expected %s, got %s", provider, target.Provider)
			}
		}
	})
}

// TestLDAPConfig tests LDAP configuration
func TestLDAPConfig(t *testing.T) {
	t.Run("ldap_config_validation", func(t *testing.T) {
		cfg := LDAPConfig{
			ServerAddr:   "ldap.company.com:389",
			BaseDN:       "dc=company,dc=com",
			BindDN:       "cn=admin,dc=company,dc=com",
			BindPassword: "${LDAP_PASSWORD}",
			UserFilter:   "(uid=%s)",
			UseTLS:       true,
		}

		if cfg.ServerAddr != "ldap.company.com:389" {
			t.Errorf("ServerAddr not stored correctly")
		}
		if cfg.UseTLS != true {
			t.Errorf("UseTLS not stored correctly")
		}
	})
}

// TestClusterConfig tests cluster configuration
func TestClusterConfig(t *testing.T) {
	t.Run("webhook_config_structure", func(t *testing.T) {
		webhookTargets := []WebhookTarget{
			{
				URL:      "https://hooks.slack.com/services/xxx",
				Events:   []string{"node_failure", "quota_exceeded"},
				Template: `{"text": "{{.Event}} failed on {{.Node}}", "status": "{{.Details}}"}`,
			},
			{
				URL:    "https://open.feishu.cn/webhook/xxx",
				Events: []string{"node_failure"},
			},
		}

		if len(webhookTargets) != 2 {
			t.Errorf("Expected 2 webhook targets, got %d", len(webhookTargets))
		}

		if webhookTargets[0].URL != "https://hooks.slack.com/services/xxx" {
			t.Errorf("URL mismatch")
		}

		if len(webhookTargets[0].Events) != 2 || webhookTargets[0].Events[0] != "node_failure" {
			t.Errorf("Events mismatch")
		}
	})

	t.Run("cluster_role_validation", func(t *testing.T) {
		validRoles := []string{"primary", "worker", ""}
		invalidRoles := []string{"master", "slave", "invalid"}

		for _, role := range validRoles {
			// Just test that they don't cause internal failure
			cfg := ClusterConfig{Role: role}
			if !(cfg.Role == role) {
				t.Errorf("Role not preserved: %s", role)
			}
		}

		for _, role := range invalidRoles {
			cfg := ClusterConfig{Role: role}
			if cfg.Role != role {
				t.Errorf("Invalid role not preserved: %s", role)
			}
		}
	})
}

// TestListenConfig tests the ListenConfig's Addr method
func TestListenConfig(t *testing.T) {
	t.Run("addr_generation", func(t *testing.T) {
		testCases := []struct {
			config   ListenConfig
			expected string
		}{
			{ListenConfig{Host: "localhost", Port: 8080}, "localhost:8080"},
			{ListenConfig{Host: "0.0.0.0", Port: 9000}, "0.0.0.0:9000"},
			{ListenConfig{Host: "", Port: 8080}, "0.0.0.0:8080"},             // Default host
			{ListenConfig{Host: "example.com", Port: 0}, "example.com:8080"}, // Default port for cproxy
		}

		for _, tc := range testCases {
			result := tc.config.Addr()
			if result != tc.expected {
				t.Errorf("For config %+v, expected addr %s, got %s", tc.config, tc.expected, result)
			}
		}
	})
}

// TestSProxySect tests the sproxy section configuration
func TestSProxySect(t *testing.T) {
	t.Run("sproxy_sect_defaults", func(t *testing.T) {
		cfg := SProxySect{
			Primary:             "http://primary.sproxy.local:9000",
			Targets:             []string{"http://worker1.sproxy.local:9000", "http://worker2.sproxy.local:9000"},
			LBStrategy:          "weighted_random",
			HealthCheckInterval: 30 * time.Second,
			RequestTimeout:      5 * time.Minute,
		}

		if cfg.Primary != "http://primary.sproxy.local:9000" {
			t.Error("Primary not stored correctly")
		}
		if len(cfg.Targets) != 2 {
			t.Error("Targets not stored correctly")
		}
		if cfg.HealthCheckInterval != 30*time.Second {
			t.Error("HealthCheckInterval not stored correctly")
		}
		if cfg.RequestTimeout != 5*time.Minute {
			t.Error("RequestTimeout not stored correctly")
		}
	})
}

// TestCProxyAuth tests CProxy Auth configuration
func TestCProxyAuth(t *testing.T) {
	t.Run("auth_config_validation", func(t *testing.T) {
		cfg := CProxyAuth{
			TokenDir:         "/home/user/.config/pairproxy",
			AutoRefresh:      true,
			RefreshThreshold: 30 * time.Minute,
		}

		if cfg.TokenDir != "/home/user/.config/pairproxy" {
			t.Error("TokenDir not stored correctly")
		}
		if !cfg.AutoRefresh {
			t.Error("AutoRefresh not stored correctly")
		}
		if cfg.RefreshThreshold != 30*time.Minute {
			t.Error("RefreshThreshold not stored correctly")
		}
	})
}

// TestLLMConfig tests LLM configuration
func TestLLMConfig(t *testing.T) {
	t.Run("llm_config_validation", func(t *testing.T) {
		cfg := LLMConfig{
			LBStrategy:     "round_robin",
			RequestTimeout: 300 * time.Second,
			MaxRetries:     2,
			RecoveryDelay:  60 * time.Second,
			Targets: []LLMTarget{
				{
					URL:      "https://api.anthropic.com",
					APIKey:   "sk-ant-xxx",
					Weight:   1,
					Provider: "anthropic",
				},
			},
		}

		if cfg.LBStrategy != "round_robin" {
			t.Error("LBStrategy not stored correctly")
		}
		if cfg.MaxRetries != 2 {
			t.Error("MaxRetries not stored correctly")
		}
		if cfg.RecoveryDelay != 60*time.Second {
			t.Error("RecoveryDelay not stored correctly")
		}
		if len(cfg.Targets) != 1 {
			t.Error("Targets not stored correctly")
		}
	})
}

// TestDatabaseConfig tests the database configuration fields
func TestDatabaseConfig(t *testing.T) {
	t.Run("database_config_fields", func(t *testing.T) {
		cfg := DatabaseConfig{
			Path:            "/var/data/sproxy.db",
			WriteBufferSize: 200,
			FlushInterval:   5 * time.Second,
			MaxOpenConns:    25,
			MaxIdleConns:    10,
			ConnMaxLifetime: 1 * time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		}

		if cfg.Path != "/var/data/sproxy.db" {
			t.Error("Path not stored correctly")
		}
		if cfg.WriteBufferSize != 200 {
			t.Error("WriteBufferSize not stored correctly")
		}
		if cfg.FlushInterval != 5*time.Second {
			t.Error("FlushInterval not stored correctly")
		}
		if cfg.MaxOpenConns != 25 {
			t.Error("MaxOpenConns not stored correctly")
		}
	})
}

// TestSProxyAuth tests the auth configuration for sproxy
func TestSProxyAuth(t *testing.T) {
	t.Run("auth_config_validation", func(t *testing.T) {
		cfg := SProxyAuth{
			JWTSecret:       "very_long_secret_at_least_32_chars_xx",
			AccessTokenTTL:  24 * time.Hour,
			RefreshTokenTTL: 7 * 24 * time.Hour,
			Provider:        "local",
			TrustedProxies:  []string{"10.0.0.0/8", "192.168.0.0/16"},
			DefaultGroup:    "default",
		}

		if cfg.JWTSecret != "very_long_secret_at_least_32_chars_xx" {
			t.Error("JWTSecret not stored correctly")
		}
		if cfg.AccessTokenTTL != 24*time.Hour {
			t.Error("AccessTokenTTL not stored correctly")
		}
		if cfg.RefreshTokenTTL != 7*24*time.Hour {
			t.Error("RefreshTokenTTL not stored correctly")
		}
		if cfg.Provider != "local" {
			t.Error("Provider not stored correctly")
		}
		if cfg.DefaultGroup != "default" {
			t.Error("DefaultGroup not stored correctly")
		}
		if len(cfg.TrustedProxies) != 2 {
			t.Errorf("TrustedProxies count not correct, got %d", len(cfg.TrustedProxies))
		}
	})
}

// TestModelJsonMarshaling tests JSON marshaling functionality
func TestModelJsonMarshaling(t *testing.T) {
	t.Run("marshal_unmarshal_consistency", func(t *testing.T) {
		original := PricingConfig{
			Models: map[string]ModelPrice{
				"test-model": {
					InputPer1K:  0.003,
					OutputPer1K: 0.015,
				},
			},
			DefaultInputPer1K:  0.001,
			DefaultOutputPer1K: 0.003,
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("Can't marshal PricingConfig: %v", err)
		}

		var unmarshaled PricingConfig
		err = json.Unmarshal(data, &unmarshaled)
		if err != nil {
			t.Fatalf("Can't unmarshal PricingConfig: %v", err)
		}

		if original.DefaultInputPer1K != unmarshaled.DefaultInputPer1K {
			t.Errorf("DefaultInputPer1K mismatch: expect %f, got %f",
				original.DefaultInputPer1K, unmarshaled.DefaultInputPer1K)
		}
	})
}

// TestEnvVarsExpansion tests environment variable expansion (conceptually)
func TestEnvVarsExpansion(t *testing.T) {
	t.Run("config_values_with_env_placeholders", func(t *testing.T) {
		// Test that configurations preserve template strings for later expansion
		llmTarget := LLMTarget{
			URL:      "https://api.anthropic.com",
			APIKey:   "${ANTHROPIC_API_KEY_1}",
			Provider: "anthropic",
		}

		// Template string should be preserved as is
		if llmTarget.APIKey != "${ANTHROPIC_API_KEY_1}" {
			t.Errorf("Templated API key not preserved: got %s", llmTarget.APIKey)
		}

		ldapTarget := LDAPConfig{
			ServerAddr:   "${LDAP_SERVER}:389",
			BaseDN:       "${LDAP_BASE_DN}",
			BindPassword: "${LDAP_BIND_PWD}",
		}

		if !strings.HasPrefix(ldapTarget.ServerAddr, "${") ||
			!strings.HasSuffix(ldapTarget.ServerAddr, "}:389") {
			t.Errorf("LDAP ServerAddr templating pattern not preserved, got: %s", ldapTarget.ServerAddr)
		}
	})
}

// TestListenConfigDefaults tests default value functionality
func TestListenConfigDefaults(t *testing.T) {
	t.Run("address_generation_with_default_host", func(t *testing.T) {
		cfg := ListenConfig{
			Host: "",
			Port: 9000,
		}
		addr := cfg.Addr()
		if addr != "0.0.0.0:9000" {
			t.Errorf("Expected default host '0.0.0.0:9000', got %s", addr)
		}
	})

	t.Run("address_generation_with_default_port", func(t *testing.T) {
		cfg := ListenConfig{
			Host: "example.com",
			Port: 0, // Will fall back to hardcoded defaults based on context (but we'll simulate with the function behavior)
		}
		addr := cfg.Addr()
		if addr != "example.com:8080" { // Assuming default port is 8080 for this implementation
			t.Errorf("Expected addr with default port 'example.com:8080', got %s", addr)
		}
	})

	t.Run("typical_configurations", func(t *testing.T) {
		testCases := []struct {
			desc     string
			config   ListenConfig
			expected string
		}{
			{"Standard setup", ListenConfig{Host: "localhost", Port: 8080}, "localhost:8080"},
			{"Public listening", ListenConfig{Host: "0.0.0.0", Port: 9000}, "0.0.0.0:9000"},
			{"Default all", ListenConfig{Host: "", Port: 0}, "0.0.0.0:8080"},
			{"Local dev", ListenConfig{Host: "127.0.0.1", Port: 4567}, "127.0.0.1:4567"},
		}

		for _, tc := range testCases {
			addr := tc.config.Addr()
			if addr != tc.expected {
				t.Errorf("%s: Expected '%s', got '%s'", tc.desc, tc.expected, addr)
			}
		}
	})
}

// TestCProxyConfigValidation tests the validation functionality directly
func TestCProxyConfigValidation(t *testing.T) {
	t.Run("valid_cproxy_config", func(t *testing.T) {
		cfg := &CProxyConfig{
			Listen: ListenConfig{
				Port: 8080,
			},
			Auth: CProxyAuth{
				RefreshThreshold: 30 * time.Minute,
			},
			Log: LogConfig{
				Level: "info",
			},
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Valid configuration should pass validation: %v", err)
		}

		// Test with different valid log levels
		for _, level := range []string{"debug", "info", "warn", "error"} {
			cfg.Log.Level = level
			if err := cfg.Validate(); err != nil {
				t.Errorf("Log level '%s' should be valid: %v", level, err)
			}
		}
	})

	t.Run("invalid_port_cproxy", func(t *testing.T) {
		cfg := &CProxyConfig{
			Listen: ListenConfig{
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

	t.Run("invalid_log_level_cproxy", func(t *testing.T) {
		cfg := &CProxyConfig{
			Log: LogConfig{
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

// TestSProxyFullConfigValidation tests the validation functionality
func TestSProxyFullConfigValidation(t *testing.T) {
	t.Run("minimal_valid_sproxy_config", func(t *testing.T) {
		cfg := &SProxyFullConfig{
			Auth: SProxyAuth{
				JWTSecret: "very_very_secure_jwt_secret_that_is_at_least_32_chars_test",
			},
			Database: DatabaseConfig{
				Path: "test.db", // This is sufficient for testing validation, file need not exist
			},
			LLM: LLMConfig{
				Targets: []LLMTarget{{URL: "https://api.com", APIKey: "key"}},
			},
			Listen: ListenConfig{
				Host: "localhost",
				Port: 8080,
			},
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Valid config should pass validation: %v", err)
		}
	})

	t.Run("invalid_sproxy_missing_jwt_secret", func(t *testing.T) {
		cfg := &SProxyFullConfig{
			Auth: SProxyAuth{
				JWTSecret: "", // Missing JWT secret
			},
			Database: DatabaseConfig{
				Path: "test.db",
			},
			LLM: LLMConfig{
				Targets: []LLMTarget{{URL: "https://api.com", APIKey: "key"}},
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation error for missing JWT secret")
		}

		if !strings.Contains(err.Error(), "auth.jwt_secret is required") {
			t.Errorf("Expected JWT secret error, got: %v", err)
		}
	})

	t.Run("invalid_sproxy_short_jwt_secret", func(t *testing.T) {
		cfg := &SProxyFullConfig{
			Auth: SProxyAuth{
				JWTSecret: "short", // Too short JWT secret (< 32 chars)
			},
			Database: DatabaseConfig{
				Path: "test.db",
			},
			LLM: LLMConfig{
				Targets: []LLMTarget{{URL: "https://api.com", APIKey: "key"}},
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation error for short JWT secret")
		}

		if !strings.Contains(err.Error(), "at least 32 characters") {
			t.Errorf("Expected short JWT secret error, got: %v", err)
		}
	})

	t.Run("invalid_sproxy_empty_database_path", func(t *testing.T) {
		cfg := &SProxyFullConfig{
			Auth: SProxyAuth{
				JWTSecret: "very_very_secure_jwt_secret_that_is_at_least_32_chars_test",
			},
			Database: DatabaseConfig{
				Path: "", // Missing database path
			},
			LLM: LLMConfig{
				Targets: []LLMTarget{{URL: "https://api.com", APIKey: "key"}},
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation error for empty database path")
		}

		if !strings.Contains(err.Error(), "database.path is required") {
			t.Errorf("Expected database path error, got: %v", err)
		}
	})

	t.Run("invalid_sproxy_no_llm_targets", func(t *testing.T) {
		cfg := &SProxyFullConfig{
			Auth: SProxyAuth{
				JWTSecret: "very_very_secure_jwt_secret_that_is_at_least_32_chars_test",
			},
			Database: DatabaseConfig{
				Path: "test.db",
			},
			LLM: LLMConfig{
				Targets: []LLMTarget{}, // No LLM targets
			},
		}

		err := cfg.Validate()
		if err == nil {
			t.Fatal("Expected validation error for no LLM targets")
		}

		if !strings.Contains(err.Error(), "llm.targets must not be empty") {
			t.Errorf("Expected LLM targets error, got: %v", err)
		}
	})
}
