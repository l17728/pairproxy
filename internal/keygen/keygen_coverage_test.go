package keygen_test

// keygen_coverage_test.go — 补充覆盖以下函数的缺失分支：
//   - NewKeyCache: size=0/负数 → 错误（lru.New 拒绝）
//   - GenerateKey: 空用户名 → 错误
//   - GenerateKey: secret 太短 → 错误
//   - GenerateKey: 超长用户名 → 正常（HMAC 无长度限制）
//   - ValidateAndGetUser: 无活跃用户 → nil, nil
//   - ValidateAndGetUser: 不活跃用户被跳过
//   - ValidateAndGetUser: 格式无效 → nil, nil（前缀不对/长度不对）
//   - ValidateAndGetUser: 正确 secret 匹配 / 错误 secret 不匹配
//   - IsValidFormat: 各种格式检查
//   - KeyCache: TTL 过期、重新写入

import (
	"strings"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/keygen"
)

// ---------------------------------------------------------------------------
// NewKeyCache — size=0 → 错误
// ---------------------------------------------------------------------------

func TestCoverage_NewKeyCache_ZeroSize(t *testing.T) {
	_, err := keygen.NewKeyCache(0, time.Minute)
	if err == nil {
		t.Error("expected error for size=0, got nil")
	}
}

// ---------------------------------------------------------------------------
// NewKeyCache — 正常创建，ttl=0 表示永不过期
// ---------------------------------------------------------------------------

func TestCoverage_NewKeyCache_ZeroTTL(t *testing.T) {
	cache, err := keygen.NewKeyCache(10, 0) // ttl=0 → 永不过期
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cache.Set("key1", &keygen.CachedUser{UserID: "u1", Username: "user1"})

	// 即使等待一段时间，也不应过期（ttl=0）
	time.Sleep(10 * time.Millisecond)
	if got := cache.Get("key1"); got == nil {
		t.Error("with ttl=0, entry should never expire")
	}
}

// ---------------------------------------------------------------------------
// GenerateKey — 空用户名 → 错误
// ---------------------------------------------------------------------------

func TestCoverage_GenerateKey_EmptyUsername(t *testing.T) {
	_, err := keygen.GenerateKey("", testSecret)
	if err == nil {
		t.Error("expected error for empty username")
	}
}

// ---------------------------------------------------------------------------
// GenerateKey — secret 太短 → 错误
// ---------------------------------------------------------------------------

func TestCoverage_GenerateKey_ShortSecret(t *testing.T) {
	_, err := keygen.GenerateKey("alice", []byte("short"))
	if err == nil {
		t.Error("expected error for short secret")
	}
}

// ---------------------------------------------------------------------------
// GenerateKey — 超长用户名 → 正常（HMAC 无长度限制）
// ---------------------------------------------------------------------------

func TestCoverage_GenerateKey_LongUsername(t *testing.T) {
	longName := strings.Repeat("a", keygen.KeyBodyLen+10)
	key, err := keygen.GenerateKey(longName, testSecret)
	if err != nil {
		t.Fatalf("unexpected error for long username: %v", err)
	}
	if len(key) != keygen.KeyTotalLen {
		t.Errorf("key length = %d, want %d", len(key), keygen.KeyTotalLen)
	}
}

// ---------------------------------------------------------------------------
// GenerateKey — 确定性验证（多次生成相同 key）
// ---------------------------------------------------------------------------

func TestCoverage_GenerateKey_DeterministicVerification(t *testing.T) {
	key1, err := keygen.GenerateKey("testuser123", testSecret)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < 50; i++ {
		key, err := keygen.GenerateKey("testuser123", testSecret)
		if err != nil {
			t.Fatalf("iter %d: unexpected error: %v", i, err)
		}
		if key != key1 {
			t.Errorf("iter %d: key mismatch (HMAC should be deterministic)", i)
		}
	}
}

// ---------------------------------------------------------------------------
// GenerateKey — 特殊字符用户名（HMAC 接受任意字符串）
// ---------------------------------------------------------------------------

func TestCoverage_GenerateKey_SpecialCharsUsername(t *testing.T) {
	// 旧算法对纯特殊字符用户名报错，HMAC 算法接受任意非空字符串
	key, err := keygen.GenerateKey("---!!!", testSecret)
	if err != nil {
		t.Fatalf("HMAC should accept any non-empty username, got error: %v", err)
	}
	if len(key) != keygen.KeyTotalLen {
		t.Errorf("key length = %d, want %d", len(key), keygen.KeyTotalLen)
	}
}

// ---------------------------------------------------------------------------
// ValidateAndGetUser — 格式无效（前缀不符）→ nil, nil
// ---------------------------------------------------------------------------

func TestCoverage_ValidateAndGetUser_InvalidPrefix(t *testing.T) {
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: string(testSecret), IsActive: true},
	}
	got, err := keygen.ValidateAndGetUser("sk-openai-abc123", users)
	if err != nil {
		t.Errorf("expected nil error for invalid format, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil user for invalid format, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ValidateAndGetUser — 格式有效但长度不符
// ---------------------------------------------------------------------------

func TestCoverage_ValidateAndGetUser_WrongLength(t *testing.T) {
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: string(testSecret), IsActive: true},
	}
	shortKey := keygen.KeyPrefix + "short"
	got, err := keygen.ValidateAndGetUser(shortKey, users)
	if err != nil {
		t.Errorf("expected nil error for wrong length, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil user for wrong length, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ValidateAndGetUser — 空用户列表 → nil, nil
// ---------------------------------------------------------------------------

func TestCoverage_ValidateAndGetUser_EmptyUsers(t *testing.T) {
	key, err := keygen.GenerateKey("alice", testSecret)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	got, err := keygen.ValidateAndGetUser(key, nil)
	if err != nil {
		t.Errorf("expected nil error for empty users, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil user for empty list, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ValidateAndGetUser — 不活跃用户被跳过
// ---------------------------------------------------------------------------

func TestCoverage_ValidateAndGetUser_InactiveUserSkipped(t *testing.T) {
	key, err := keygen.GenerateKey("alice", testSecret)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: string(testSecret), IsActive: false}, // 不活跃
	}
	got, validErr := keygen.ValidateAndGetUser(key, users)
	if validErr != nil {
		t.Errorf("expected nil error, got %v", validErr)
	}
	if got != nil {
		t.Errorf("expected nil (inactive user skipped), got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ValidateAndGetUser — 旧密码派生的 key 不匹配持有新密码 hash 的用户
// ---------------------------------------------------------------------------

func TestCoverage_ValidateAndGetUser_WrongPassword(t *testing.T) {
	// key 由旧密码 hash 派生，用户现已持有新密码 hash → 不匹配
	key, err := keygen.GenerateKey("alice", testSecret)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	newPasswordHash := "new-password-hash-must-be-at-least-32-bytes-long!!"
	users := []keygen.UserEntry{
		{ID: "u1", Username: "alice", PasswordHash: newPasswordHash, IsActive: true},
	}
	got, err := keygen.ValidateAndGetUser(key, users)
	if err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil user with new password hash, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ValidateAndGetUser — HMAC 无碰撞（旧算法碰撞场景）
// ---------------------------------------------------------------------------

func TestCoverage_ValidateAndGetUser_NoCollision(t *testing.T) {
	// 旧算法碰撞场景：abcd 和 dcba 有相同字符集
	// HMAC 算法下两者 key 不同，各自只匹配自己
	ph := string(testSecret)
	users := []keygen.UserEntry{
		{ID: "u1", Username: "abcd", PasswordHash: ph, IsActive: true},
		{ID: "u2", Username: "dcba", PasswordHash: ph, IsActive: true},
	}

	key1, err := keygen.GenerateKey("abcd", testSecret)
	if err != nil {
		t.Fatalf("GenerateKey abcd: %v", err)
	}
	key2, err := keygen.GenerateKey("dcba", testSecret)
	if err != nil {
		t.Fatalf("GenerateKey dcba: %v", err)
	}
	if key1 == key2 {
		t.Fatal("HMAC keys for different usernames must differ")
	}

	got1, err := keygen.ValidateAndGetUser(key1, users)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if got1 == nil || got1.Username != "abcd" {
		t.Errorf("key1 should match abcd, got %v", got1)
	}

	got2, err := keygen.ValidateAndGetUser(key2, users)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if got2 == nil || got2.Username != "dcba" {
		t.Errorf("key2 should match dcba, got %v", got2)
	}
}

// ---------------------------------------------------------------------------
// KeyCache.Get — TTL 过期后，缓存内条目仍存在但被重新写入（二次校验）
// ---------------------------------------------------------------------------

func TestCoverage_KeyCache_TTL_DoubleCheck(t *testing.T) {
	cache, err := keygen.NewKeyCache(100, 30*time.Millisecond)
	if err != nil {
		t.Fatalf("NewKeyCache: %v", err)
	}

	cache.Set("k", &keygen.CachedUser{UserID: "u1", Username: "alice"})

	// 在 TTL 内访问 → 命中
	if got := cache.Get("k"); got == nil {
		t.Error("should hit before TTL expires")
	}

	// 等待 TTL 过期
	time.Sleep(60 * time.Millisecond)

	// 过期后访问 → miss
	if got := cache.Get("k"); got != nil {
		t.Error("should miss after TTL expires")
	}

	// 重新写入后应命中
	cache.Set("k", &keygen.CachedUser{UserID: "u1", Username: "alice"})
	if got := cache.Get("k"); got == nil {
		t.Error("should hit after re-adding the expired entry")
	}
}

// ---------------------------------------------------------------------------
// IsValidFormat — 各种格式检查
// ---------------------------------------------------------------------------

func TestCoverage_IsValidFormat(t *testing.T) {
	cases := []struct {
		key   string
		valid bool
		desc  string
	}{
		{keygen.KeyPrefix + strings.Repeat("a", keygen.KeyBodyLen), true, "valid key"},
		{"", false, "empty string"},
		{"sk-openai-" + strings.Repeat("a", 44), false, "wrong prefix"},
		{keygen.KeyPrefix + strings.Repeat("a", keygen.KeyBodyLen-1), false, "too short body"},
		{keygen.KeyPrefix + strings.Repeat("a", keygen.KeyBodyLen+1), false, "too long body"},
		{keygen.KeyPrefix + strings.Repeat("a", keygen.KeyBodyLen-1) + "!", false, "invalid char"},
	}
	for _, tc := range cases {
		got := keygen.IsValidFormat(tc.key)
		if got != tc.valid {
			t.Errorf("%s: IsValidFormat(%q) = %v, want %v", tc.desc, tc.key, got, tc.valid)
		}
	}
}
