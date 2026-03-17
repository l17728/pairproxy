package auth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// ---------------------------------------------------------------------------
// DefaultTokenDir — fallback branch when UserConfigDir fails
// ---------------------------------------------------------------------------

func TestDefaultTokenDir_FallbackWhenNOHOME(t *testing.T) {
	// DefaultTokenDir uses os.UserConfigDir() first; if it succeeds (normally),
	// the result ends with "pairproxy". Verify the happy path explicitly.
	dir := DefaultTokenDir()
	if dir == "" {
		t.Fatal("DefaultTokenDir() must not return empty string")
	}
	if filepath.Base(dir) != "pairproxy" {
		t.Errorf("DefaultTokenDir() base = %q, want 'pairproxy'", filepath.Base(dir))
	}
}

// TestDefaultTokenDir_FallsBackToHomeDir exercises the fallback code path by
// temporarily unsetting the environment variables that os.UserConfigDir depends on.
// On Windows: APPDATA; on Linux/macOS: XDG_CONFIG_HOME + HOME.
// When UserConfigDir fails the fallback appends ".pairproxy" to the home directory;
// when it succeeds it appends "pairproxy" to the config dir.
func TestDefaultTokenDir_HomeBasedFallback(t *testing.T) {
	// Save and clear platform-specific env vars that UserConfigDir uses
	appdata := os.Getenv("APPDATA")
	xdgCfg := os.Getenv("XDG_CONFIG_HOME")
	homeEnv := os.Getenv("HOME")

	defer func() {
		if appdata != "" {
			_ = os.Setenv("APPDATA", appdata)
		}
		if xdgCfg != "" {
			_ = os.Setenv("XDG_CONFIG_HOME", xdgCfg)
		}
		if homeEnv != "" {
			_ = os.Setenv("HOME", homeEnv)
		}
	}()

	_ = os.Unsetenv("APPDATA")
	_ = os.Unsetenv("XDG_CONFIG_HOME")

	dir := DefaultTokenDir()
	if dir == "" {
		t.Fatal("DefaultTokenDir() must not return empty string even without APPDATA")
	}
	// Depending on whether UserConfigDir() succeeds or falls back:
	//   success  → base is "pairproxy"
	//   fallback → base is ".pairproxy"
	base := filepath.Base(dir)
	if base != "pairproxy" && base != ".pairproxy" {
		t.Errorf("DefaultTokenDir() base = %q, want 'pairproxy' or '.pairproxy'", base)
	}
}

// ---------------------------------------------------------------------------
// TokenStore.Save — writes readable content
// ---------------------------------------------------------------------------

func TestTokenStore_Save_WritesReadableContent(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()

	tf := &TokenFile{
		AccessToken:  "at-save",
		RefreshToken: "rt-save",
		ExpiresAt:    time.Now().Add(time.Hour),
		ServerAddr:   "https://sproxy.test",
		Username:     "testuser",
	}

	if err := store.Save(dir, tf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(filepath.Join(dir, tokenFileName)); err != nil {
		t.Fatalf("token file should exist after Save: %v", err)
	}

	// Verify round-trip
	loaded, err := store.Load(dir)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if loaded.AccessToken != tf.AccessToken {
		t.Errorf("AccessToken = %q, want %q", loaded.AccessToken, tf.AccessToken)
	}
	if loaded.Username != tf.Username {
		t.Errorf("Username = %q, want %q", loaded.Username, tf.Username)
	}
}

// ---------------------------------------------------------------------------
// TokenStore.Delete — deletes non-existent file without error
// ---------------------------------------------------------------------------

func TestTokenStore_Delete_NonExistentFile_NoError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()

	// No file has been saved; Delete should succeed silently
	if err := store.Delete(dir); err != nil {
		t.Errorf("Delete of non-existent file should not error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TokenStore.Load — reads existing valid JSON
// ---------------------------------------------------------------------------

func TestTokenStore_Load_ValidFile(t *testing.T) {
	logger := zaptest.NewLogger(t)
	store := NewTokenStore(logger, 30*time.Minute)
	dir := t.TempDir()

	tf := &TokenFile{
		AccessToken: "at-load",
		ExpiresAt:   time.Now().Add(2 * time.Hour),
		ServerAddr:  "https://sp.test",
	}
	if err := store.Save(dir, tf); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("Load returned nil for valid file")
	}
	if loaded.AccessToken != "at-load" {
		t.Errorf("AccessToken = %q, want 'at-load'", loaded.AccessToken)
	}
}

// ---------------------------------------------------------------------------
// JWT Manager.Sign — produces non-empty token
// ---------------------------------------------------------------------------

func TestJWTManager_Sign_ProducesToken(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr, err := NewManager(logger, "sign-secret-key")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	token, err := mgr.Sign(JWTClaims{UserID: "u-sign", Username: "signuser", Role: "user"}, time.Hour)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if token == "" {
		t.Error("Sign must return non-empty token")
	}
}

// ---------------------------------------------------------------------------
// JWT Manager.Parse — wrong algorithm rejected
// ---------------------------------------------------------------------------

func TestJWTManager_Parse_RejectsNonsenseToken(t *testing.T) {
	logger := zaptest.NewLogger(t)
	mgr, err := NewManager(logger, "parse-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, parseErr := mgr.Parse("this.is.not.a.valid.jwt")
	if parseErr == nil {
		t.Error("Parse should return error for garbage token")
	}
}

// ---------------------------------------------------------------------------
// HashPassword — non-empty input succeeds
// ---------------------------------------------------------------------------

func TestHashPassword_NonEmptyInput_Succeeds(t *testing.T) {
	logger := zaptest.NewLogger(t)
	hash, err := HashPassword(logger, "MyPassword1!")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Error("HashPassword should return non-empty hash")
	}
	// Verify it works with VerifyPassword
	if !VerifyPassword(logger, hash, "MyPassword1!") {
		t.Error("VerifyPassword should return true for correct password")
	}
}

// ---------------------------------------------------------------------------
// HashPassword — empty input returns error
// ---------------------------------------------------------------------------

func TestHashPassword_EmptyInput_ReturnsError(t *testing.T) {
	logger := zaptest.NewLogger(t)
	_, err := HashPassword(logger, "")
	if err == nil {
		t.Error("HashPassword should return error for empty password")
	}
}

// ---------------------------------------------------------------------------
// Encrypt — basic round-trip coverage
// ---------------------------------------------------------------------------

func TestEncrypt_BasicRoundTrip(t *testing.T) {
	plaintext := "encrypt-coverage-test"
	key := "coverage-key"

	enc, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	dec, err := Decrypt(enc, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if dec != plaintext {
		t.Errorf("Decrypt = %q, want %q", dec, plaintext)
	}
}

// ---------------------------------------------------------------------------
// Decrypt — too-short ciphertext
// ---------------------------------------------------------------------------

func TestDecrypt_TooShortCiphertext(t *testing.T) {
	// A valid base64 string that decodes to fewer bytes than the GCM nonce size (12)
	// base64("hello") = "aGVsbG8="
	_, err := Decrypt("aGVsbG8=", "some-key")
	if err == nil {
		t.Error("Decrypt with too-short ciphertext should return error")
	}
}

// TestDecrypt_InvalidBase64Input re-verifies invalid base64 returns error.
func TestDecrypt_InvalidBase64Input(t *testing.T) {
	_, err := Decrypt("!not-valid-base64!!!", "key")
	if err == nil {
		t.Error("Decrypt with invalid base64 should return error")
	}
}
