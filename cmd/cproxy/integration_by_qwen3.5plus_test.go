package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/config"
	"go.uber.org/zap/zaptest"
)

// TestCProxyConfig tests the config related functionality
func TestCProxyConfig(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("TestRenderProgressBar", func(t *testing.T) {
		t.Run("empty bar", func(t *testing.T) {
			bar := renderProgressBar(0, 10)
			expected := "[░░░░░░░░░░]"
			if bar != expected {
				t.Errorf("Expected '%s', got '%s'", expected, bar)
			}
		})

		t.Run("full bar", func(t *testing.T) {
			bar := renderProgressBar(100, 10)
			expected := "[██████████]"
			if bar != expected {
				t.Errorf("Expected '%s', got '%s'", expected, bar)
			}
		})

		t.Run("half bar", func(t *testing.T) {
			bar := renderProgressBar(50, 10)
			expected := "[█████░░░░░]"
			if bar != expected {
				t.Errorf("Expected '%s', got '%s'", expected, bar)
			}
		})

		t.Run("clamped percentages", func(t *testing.T) {
			above100 := renderProgressBar(150, 5)
			below0 := renderProgressBar(-10, 5)
			expectedAbove := "[█████]" // Should clamp to 100%
			expectedBelow := "[░░░░░]" // Should clamp to 0%

			if above100 != expectedAbove {
				t.Errorf("Expected '%s' for above 100%%, got '%s'", expectedAbove, above100)
			}
			if below0 != expectedBelow {
				t.Errorf("Expected '%s' for below 0%%, got '%s'", expectedBelow, below0)
			}
		})
	})

	t.Run("TokenStoreTest", func(t *testing.T) {
		tokenDir := t.TempDir()
		store := auth.NewTokenStore(logger, 30*time.Minute)

		tf := &auth.TokenFile{
			AccessToken:  "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
			RefreshToken: "refresh-atoken-12345",
			ExpiresAt:    time.Now().Add(24 * time.Hour),
			ServerAddr:   "http://example.com",
			Username:     "testuser",
		}

		// Test saving
		err := store.Save(tokenDir, tf)
		if err != nil {
			t.Fatalf("Failed to save token file: %v", err)
		}

		// Test loading
		loaded, err := store.Load(tokenDir)
		if err != nil {
			t.Fatalf("Failed to load token file: %v", err)
		}

		if loaded.Username != tf.Username {
			t.Errorf("Username mismatch: got %s, want %s", loaded.Username, tf.Username)
		}

		if loaded.ServerAddr != tf.ServerAddr {
			t.Errorf("ServerAddr mismatch: got %s, want %s", loaded.ServerAddr, tf.ServerAddr)
		}
	})

	t.Run("BuildInitialTargets", func(t *testing.T) {
		cacheDir := t.TempDir()

		t.Run("primary config priority", func(t *testing.T) {
			cfg := &config.SProxySect{
				Primary: "http://primary:9000",
				Targets: []string{"http://backup:9000"},
			}

			result, err := buildInitialTargets(cfg, cacheDir, logger)
			if err != nil {
				t.Fatalf("buildInitialTargets should succeed: %v", err)
			}

			// Should include both primary and backup
			if len(result) != 2 {
				t.Fatalf("Expected 2 targets, got %d", len(result))
			}

			// Both should be healthy initially
			for _, target := range result {
				if target.Addr != "http://primary:9000" && target.Addr != "http://backup:9000" {
					t.Errorf("Unexpected target address: %s", target.Addr)
				}
				if !target.Healthy {
					t.Error("Config-provided targets should be healthy by default")
				}
			}
		})

		t.Run("duplicate elimination", func(t *testing.T) {
			cfg := &config.SProxySect{
				Primary: "http://same:9000",
				Targets: []string{"http://same:9000"}, // Duplicate address
			}

			result, err := buildInitialTargets(cfg, cacheDir, logger)
			if err != nil {
				t.Fatalf("buildInitialTargets should succeed: %v", err)
			}

			if len(result) != 1 {
				t.Fatalf("Expected 1 unique target, got %d", len(result))
			}

			if result[0].Addr != "http://same:9000" {
				t.Errorf("Expected target address 'http://same:9000', got %s", result[0].Addr)
			}
		})

		t.Run("empty_config", func(t *testing.T) {
			cfg := &config.SProxySect{
				Primary: "",
				Targets: []string{},
			}

			_, err := buildInitialTargets(cfg, cacheDir, logger)
			if err == nil {
				t.Fatal("Expected error for empty config")
			}

			if !strings.Contains(err.Error(), "no s-proxy targets configured") {
				t.Errorf("Expected specific error message, got: %v", err)
			}
		})
	})
}

// TestCProxyIntegration tests integration between different parts of cproxy
func TestCProxyIntegration(t *testing.T) {
	logger := zaptest.NewLogger(t)

	t.Run("TokenStoreOperations", func(t *testing.T) {
		tokenDir := t.TempDir()
		store := auth.NewTokenStore(logger, 5*time.Minute) // Short refresh threshold for testing

		// Create a token that's near expiration to test refresh logic
		expiredSoonToken := &auth.TokenFile{
			AccessToken:  "some_token",
			RefreshToken: "some_refresh_token",
			ExpiresAt:    time.Now().Add(10 * time.Minute), // Expires in 10 minutes (beyond 5min threshold)
			ServerAddr:   "http://test.server",
			Username:     "testuser",
		}

		err := store.Save(tokenDir, expiredSoonToken)
		if err != nil {
			t.Fatalf("Failed to save token: %v", err)
		}

		loaded, err := store.Load(tokenDir)
		if err != nil {
			t.Fatalf("Failed to load token: %v", err)
		}

		// Verify the token is loaded correctly
		if loaded.Username != "testuser" {
			t.Errorf("Username not loaded correctly, got: %s", loaded.Username)
		}

		// Test validity check
		isValid := store.IsValid(loaded)
		if !isValid {
			t.Error("Freshly loaded token should be valid")
		}

		// Now check a past-expired token should be invalid
		pastToken := &auth.TokenFile{
			AccessToken:  "some_token",
			RefreshToken: "some_refresh_token",
			ExpiresAt:    time.Now().Add(-1 * time.Hour), // Already expired
			ServerAddr:   "http://test.server",
			Username:     "testuser",
		}

		isValid = store.IsValid(pastToken)
		if isValid {
			t.Error("Past-expired token should be invalid")
		}
	})

	t.Run("TestStatusFlow", func(t *testing.T) {
		logger := zaptest.NewLogger(t)

		// Set up a token directory
		tokenDir := t.TempDir()
		store := auth.NewTokenStore(logger, 30*time.Minute)

		// Test case: no token exists
		stdout := captureStdout(func() {
			tf, err := store.Load(tokenDir)
			if err != nil {
				fmt.Printf("load error: %v", err)
				return
			}
			if tf == nil {
				fmt.Println("Status: Not authenticated (run 'cproxy login')")
				return
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
		})

		if !strings.Contains(stdout, "Status: Not authenticated") {
			t.Errorf("Expected 'Status: Not authenticated', got: %s", stdout)
		}

		// Now test with a valid token
		validToken := &auth.TokenFile{
			AccessToken:  "valid-token",
			RefreshToken: "valid-refresh-token",
			ExpiresAt:    time.Now().Add(12 * time.Hour), // Expiring in 12 hours
			ServerAddr:   "http://example.com",
			Username:     "alice",
		}

		store.Save(tokenDir, validToken)

		stdout = captureStdout(func() {
			tf, err := store.Load(tokenDir)
			if err != nil {
				t.Fatalf("Failed to load valid token: %v", err)
			}

			remaining := time.Until(tf.ExpiresAt)
			fmt.Printf("Status:  valid\n")
			fmt.Printf("User:    %s\n", tf.Username)
			fmt.Printf("Server:  %s\n", tf.ServerAddr)
			fmt.Printf("Expires: %s\n", tf.ExpiresAt.Format(time.RFC3339))

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
		})

		if !strings.Contains(stdout, "Status:  valid") || !strings.Contains(stdout, "User:    alice") {
			t.Errorf("Expected status display for valid token, got: %s", stdout)
		}
	})
}

// Helper functions for testing
func captureStdout(f func()) string {
	r, w, _ := os.Pipe()
	origStdout := os.Stdout
	os.Stdout = w

	f()

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	return buf.String()
}

// Entry point for go test to handle cobra flag resetting
