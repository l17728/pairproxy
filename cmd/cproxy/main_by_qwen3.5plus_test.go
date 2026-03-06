package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// TestMainCommands runs various CLI command tests
func TestMainCommands(t *testing.T) {
	t.Run("version", func(t *testing.T) {
		output := captureStdout(func() {
			originalArgs := os.Args
			os.Args = []string{"cproxy", "version"}
			// Call main function to execute version command
			main()
			os.Args = originalArgs
		})
		
		if !strings.Contains(output, "cproxy") {
			t.Errorf("Expected version output to contain 'cproxy', got: %s", output)
		}
	})

	t.Run("config-validate-success", func(t *testing.T) {
		// Create a minimal temp config
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cproxy.yaml")
		configData := `listen:
  host: "localhost"
  port: 8080
sproxy:
  primary: "http://localhost:9000"
  lb_strategy: "weighted_random"
  health_check_interval: 30s
auth:
  refresh_threshold: 30m
log:
  level: "info"
`
		if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
			t.Fatalf("Failed to write config file: %v", err)
		}

		// Test validation command by loading and validating the config directly
		cfg, warnings, err := config.LoadCProxyConfig(configPath)

		if err != nil {
			t.Fatalf("Expected config to load successfully, got: %v", err)
		}
		
		if len(warnings) > 0 {
			t.Logf("Warnings: %v", warnings)
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Expected validation to pass, got: %v", err)
		}
	})
}

// Test buildInitialTargets functionality in isolation
func TestBuildInitialTargets(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("empty_config", func(t *testing.T) {
		cfg := &config.SProxySect{
			Primary: "",
			Targets: []string{},
		}
		_, err := buildInitialTargets(cfg, "", logger)
		
		if err == nil {
			t.Fatal("Expected error for empty config")
		}
		if !strings.Contains(err.Error(), "no s-proxy targets configured") {
			t.Errorf("Expected specific error message, got: %v", err)
		}
	})

	t.Run("primary_only", func(t *testing.T) {
		cfg := &config.SProxySect{
			Primary: "http://primary:9000",
			Targets: []string{},
		}
		result, err := buildInitialTargets(cfg, "", logger)
		
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("Expected 1 target, got: %d", len(result))
		}
		if result[0].ID != "http://primary:9000" {
			t.Errorf("Expected ID 'http://primary:9000', got: %s", result[0].ID)
		}
		if result[0].Addr != "http://primary:9000" {
			t.Errorf("Expected Addr 'http://primary:9000', got: %s", result[0].Addr)
		}
		if !result[0].Healthy {
			t.Error("Expected Healthy to be true")
		}
	})

	t.Run("multiple_targets", func(t *testing.T) {
		cfg := &config.SProxySect{
			Primary: "http://primary:9000",
			Targets: []string{"http://worker1:9000", "http://worker2:9000"},
		}
		result, err := buildInitialTargets(cfg, "", logger)
		
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("Expected 3 targets, got: %d", len(result))
		}
		
		addresses := make([]string, len(result))
		for i, target := range result {
			addresses[i] = target.Addr
		}
		
		if !contains(addresses, "http://primary:9000") ||
		   !contains(addresses, "http://worker1:9000") ||
		   !contains(addresses, "http://worker2:9000") {
			t.Errorf("Expected all targets to be present, got: %v", addresses)
		}
	})
}

func TestStatusCommand(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("token_not_exists", func(t *testing.T) {
		// Create token store pointing to non-existent tokens
		output := captureStdout(func() {
			tokenDir := t.TempDir() + "/nonexistent"

			if err := runStatusTokenCheck(tokenDir, logger); err != nil {
				fmt.Printf("Error: %v\n", err)
			}
		})
		
		if !strings.Contains(output, "Status: Not authenticated") {
			t.Errorf("Expected 'Status: Not authenticated', got: %s", output)
		}
	})

	t.Run("invalid_token", func(t *testing.T) {
		tokenDir := t.TempDir()
		logger := zaptest.NewLogger(t)
		store := auth.NewTokenStore(logger, 30*time.Minute)
		
		invalidToken := &auth.TokenFile{
			AccessToken:  "invalid.token.here",
			RefreshToken: "invalid-refresh-token",
			ExpiresAt:    time.Now().Add(-1 * time.Hour), // Expired
			ServerAddr:   "http://localhost:9000",
			Username:     "testuser",
		}
		
		if err := store.Save(tokenDir, invalidToken); err != nil {
			t.Fatalf("Failed to save token: %v", err)
		}
		
		output := captureStdout(func() {
			if err := runStatusTokenCheck(tokenDir, logger); err != nil {
				fmt.Printf("Error: %v\n", err)
			}
		})

		if !strings.Contains(output, "Status:") || !strings.Contains(output, "expired") {
			t.Errorf("Expected output to contain 'Status:' and 'expired', got: %s", output)
		}
	})
}

// Mock function to simulate runStatus with only token checking logic
func runStatusTokenCheck(tokenDir string, logger *zap.Logger) error {
	store := auth.NewTokenStore(logger, 30*time.Minute)

	tf, err := store.Load(tokenDir)
	if err != nil {
		return err
	}
	if tf == nil {
		fmt.Println("Status: Not authenticated (run 'cproxy login')")
		return nil
	}

	remaining := time.Until(tf.ExpiresAt)
	status := "valid"
	if !store.IsValid(tf) {
		if remaining < 0 {
			status = "expired"
		} else {
			status = "near expiry (needs refresh)"
		}
	}

	fmt.Printf("Status:  %s\n", status)
	if tf.Username != "" {
		fmt.Printf("User:    %s\n", tf.Username)
	}
	fmt.Printf("Server:  %s\n", tf.ServerAddr)
	fmt.Printf("Expires: %s\n", tf.ExpiresAt.Format(time.RFC3339))

	// Token TTL progress bar (assumes 24h standard access token lifetime)
	const totalTTL = 24 * time.Hour
	pct := 0
	if remaining > 0 {
		pct = int(remaining * 100 / totalTTL)
		if pct > 100 {
			pct = 100
		}
	}
	bar := renderProgressBar(pct, 20)
	fmt.Printf("TTL:     %s %d%% (%s remaining)\n", bar, pct, remaining.Truncate(time.Second))

	return nil
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}


