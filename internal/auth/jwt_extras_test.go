package auth

import (
	"fmt"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Blacklist concurrency safety
// ---------------------------------------------------------------------------

// TestBlacklistConcurrent verifies that concurrent Add and IsBlocked calls
// do not race (run with -race to detect data races).
func TestBlacklistConcurrent(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)

	const goroutines = 50
	done := make(chan struct{}, goroutines*2)

	for i := 0; i < goroutines; i++ {
		go func(i int) {
			jti := fmt.Sprintf("jti-%d", i)
			bl.Add(jti, time.Now().Add(time.Hour))
			done <- struct{}{}
		}(i)
		go func(i int) {
			jti := fmt.Sprintf("jti-%d", i)
			bl.IsBlocked(jti)
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < goroutines*2; i++ {
		<-done
	}
}

// ---------------------------------------------------------------------------
// Blacklist cleanup
// ---------------------------------------------------------------------------

// TestBlacklistCleanupExpired verifies that cleanup() physically removes expired
// entries from the internal map while leaving valid entries intact.
func TestBlacklistCleanupExpired(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)

	bl.Add("expired-jti", time.Now().Add(-time.Millisecond)) // already expired
	bl.Add("valid-jti", time.Now().Add(time.Hour))           // still valid

	bl.cleanup()

	bl.mu.RLock()
	_, hasExpired := bl.entries["expired-jti"]
	_, hasValid := bl.entries["valid-jti"]
	bl.mu.RUnlock()

	if hasExpired {
		t.Error("cleanup() should have removed expired-jti from entries map")
	}
	if !hasValid {
		t.Error("cleanup() should NOT have removed valid-jti from entries map")
	}
}

// TestBlacklistCleanupEmpty verifies that cleanup() on an empty blacklist does not panic.
func TestBlacklistCleanupEmpty(t *testing.T) {
	logger := testLogger(t)
	bl := NewBlacklist(logger)
	bl.cleanup() // should not panic
}

// ---------------------------------------------------------------------------
// JWT claims round-trip
// ---------------------------------------------------------------------------

// TestJWTClaimsRoundTrip verifies that all JWTClaims fields (including GroupID
// and Role) survive a Sign → Parse round-trip unchanged.
func TestJWTClaimsRoundTrip(t *testing.T) {
	logger := testLogger(t)
	m, _ := NewManager(logger, "roundtrip-secret")

	original := JWTClaims{
		UserID:   "user-uuid-abc",
		Username: "charlie",
		GroupID:  "group-xyz",
		Role:     "admin",
	}

	token, err := m.Sign(original, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	parsed, err := m.Parse(token)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if parsed.UserID != original.UserID {
		t.Errorf("UserID = %q, want %q", parsed.UserID, original.UserID)
	}
	if parsed.Username != original.Username {
		t.Errorf("Username = %q, want %q", parsed.Username, original.Username)
	}
	if parsed.GroupID != original.GroupID {
		t.Errorf("GroupID = %q, want %q", parsed.GroupID, original.GroupID)
	}
	if parsed.Role != original.Role {
		t.Errorf("Role = %q, want %q", parsed.Role, original.Role)
	}
	if parsed.JTI == "" {
		t.Error("JTI should not be empty after parse")
	}
}

// TestJWTBlacklistExpiry verifies that a blacklisted token is correctly rejected
// before its blacklist entry expires, but accepted after (lazy expiry).
func TestJWTBlacklistExpiry(t *testing.T) {
	logger := testLogger(t)
	m, _ := NewManager(logger, "expiry-secret")

	claims := JWTClaims{UserID: "u-expiry"}
	token, _ := m.Sign(claims, time.Hour)

	parsed, _ := m.Parse(token)
	jti := parsed.JTI

	// Add to blacklist with very short TTL
	m.Blacklist(jti, time.Now().Add(20*time.Millisecond))

	// Token should be rejected immediately
	if _, err := m.Parse(token); err == nil {
		t.Fatal("Parse should fail while token is blacklisted")
	}

	// Wait for blacklist entry to expire
	time.Sleep(50 * time.Millisecond)

	// Token should be accepted again (blacklist TTL expired)
	if _, err := m.Parse(token); err != nil {
		t.Errorf("Parse should succeed after blacklist expiry, got: %v", err)
	}
}

// TestManagerIsBlacklisted verifies that IsBlacklisted reflects the blacklist state.
func TestManagerIsBlacklisted(t *testing.T) {
	logger := testLogger(t)
	m, _ := NewManager(logger, "isblacklisted-secret")

	m.Blacklist("some-jti", time.Now().Add(time.Hour))

	if !m.IsBlacklisted("some-jti") {
		t.Error("IsBlacklisted should return true for a blacklisted JTI")
	}
	if m.IsBlacklisted("unknown-jti") {
		t.Error("IsBlacklisted should return false for an unknown JTI")
	}
}
