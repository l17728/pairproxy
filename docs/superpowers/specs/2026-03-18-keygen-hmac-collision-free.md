# Keygen HMAC-based Collision-Free Algorithm Design

**Date**: 2026-03-18
**Version**: v2.15.0
**Status**: Approved
**Author**: Claude Sonnet 4.6

---

## Problem Statement

The current keygen algorithm embeds username alphanumeric characters into random positions within the API key body. This approach has a critical collision vulnerability:

**Collision Scenario**:
- User `alice123` → fingerprint `[a,l,i,c,e,1,2,3]` (8 chars)
- User `321ecila` → fingerprint `[3,2,1,e,c,i,l,a]` (8 chars, same character set)
- Any key containing these 8 characters matches both users
- System detects collision and rejects the key, but the key is already generated and unusable

**Impact**:
- As user base grows, collision probability increases
- Users with similar alphanumeric character sets cannot coexist
- No cryptographic guarantee of uniqueness

**Goal**: Replace with HMAC-SHA256 based algorithm that cryptographically guarantees zero collisions.

---

## Design Decisions

### 1. Migration Strategy: Hard Cutover (Option A)

**Chosen**: All existing keys immediately invalidated upon upgrade.

**Rationale**:
- Clean break, no legacy code maintenance
- v2.14.1 just released, user base likely small
- Simpler implementation and testing

**Rejected Alternatives**:
- Dual-format coexistence: adds complexity, maintains two validation paths
- Auto-migration with error hints: still requires user interruption

### 2. Secret Management: Independent Configuration (Option B)

**Chosen**: New `auth.keygen_secret` config field, separate from `jwt_secret`.

**Rationale**:
- Security isolation: different secrets for different purposes
- Independent key rotation capability
- API key lifecycle typically longer than JWT

**Configuration**:
```yaml
auth:
  jwt_secret: "${JWT_SECRET}"
  keygen_secret: "${KEYGEN_SECRET}"  # New field
```

**Validation**:
- Required field
- Minimum 32 characters (same as jwt_secret)
- Supports environment variable substitution

### 3. Key Format: 54-Character Length Preserved (Option A)

**Chosen**: `sk-pp-<48_base62_chars>` (total 54 chars, same as current).

**Rationale**:
- No UI/database schema changes needed
- Truncated HMAC still provides 286 bits of entropy (collision probability < 2^-143)
- Backward compatible with existing length assumptions

**Rejected Alternatives**:
- 64-char full HMAC: unnecessary security margin, breaks UI
- Mixed format with username hash prefix: leaks username information

### 4. Validation Strategy: Simple Iteration (Option A)

**Chosen**: O(n) iteration through all active users, recompute HMAC for each.

**Rationale**:
- Existing KeyCache (LRU + TTL) handles hot path (O(1) cache hit)
- Typical enterprise user count < 1000
- HMAC-SHA256 computation ~1μs, total validation ~1ms for 1000 users
- Simple, stateless, no additional indexing needed

---

## Architecture

### Algorithm Overview

**Key Generation**:
```
Input: username (string), secret ([]byte)
Steps:
  1. Compute HMAC-SHA256(secret, username) → 32 bytes
  2. Base62 encode → ~43 characters
  3. Pad/truncate to exactly 48 characters
  4. Prepend "sk-pp-"
Output: 54-character API key
```

**Key Validation**:
```
Input: key (string), users ([]UserEntry), secret ([]byte)
Steps:
  1. Format check: "sk-pp-" prefix + 48 Base62 chars
  2. For each active user:
     - Recompute GenerateKey(user.Username, secret)
     - Compare with provided key
  3. Return matched user or nil
Output: *UserEntry or nil
```

**Collision Guarantee**:
- HMAC-SHA256 cryptographic property: different inputs → different outputs (collision probability < 2^-128)
- Even truncated to 48 chars (286 bits), collision probability < 2^-143 (negligible)
- **Entropy calculation**: log₂(62^48) = 48 × log₂(62) ≈ 48 × 5.954 ≈ 285.8 bits

### Base62 Encoding

**Library**: `github.com/jxskiss/base62 v1.1.0`
- Character set: `0-9A-Za-z` (62 characters)
- Pure Go, no CGO dependencies
- Standard library quality, widely used

**Encoding Logic**:
```go
func encodeBase62HMAC(hmac []byte) string {
    encoded := base62.EncodeToString(hmac)
    if len(encoded) < KeyBodyLen {
        // Right-pad with '0' to reach KeyBodyLen
        // (Left-padding would break Base62 semantics, but we don't decode so it's safe)
        // Using right-pad for conceptual correctness
        encoded = encoded + strings.Repeat("0", KeyBodyLen-len(encoded))
    } else if len(encoded) > KeyBodyLen {
        // Truncate to KeyBodyLen (keep leftmost chars for max entropy)
        encoded = encoded[:KeyBodyLen]
    }
    return encoded
}
```

**Note**: We use right-padding instead of left-padding because left-padding with zeros breaks Base62 decoding reversibility (leading zeros are semantically significant). Although we never decode keys (validation recomputes HMAC), right-padding is conceptually cleaner. The choice doesn't affect security since we're truncating a cryptographic hash.

---

## File Changes

### Summary of Files to Modify

| File | Type | Changes |
|------|------|---------|
| `internal/keygen/generator.go` | Core | Rewrite GenerateKey with HMAC-SHA256 |
| `internal/keygen/validator.go` | Core | Rewrite ValidateAndGetUser with HMAC comparison |
| `internal/config/config.go` | Config | Add KeygenSecret field + validation |
| `internal/api/keygen_handler.go` | API | Pass secret to GenerateKey |
| `internal/proxy/keyauth_middleware.go` | Middleware | Pass secret to ValidateAndGetUser + constructor change |
| `cmd/sproxy/main.go` | Main | Update KeyAuthMiddleware + KeygenHandler instantiation |
| `config/sproxy.yaml.example` | Config | Add keygen_secret field |
| `internal/keygen/generator_test.go` | Test | Rewrite tests for HMAC algorithm |
| `internal/keygen/validator_test.go` | Test | Rewrite tests for HMAC validation |
| `internal/keygen/performance_test.go` | Test | Add performance benchmarks |
| `internal/api/keygen_handler_test.go` | Test | Update integration tests |
| `internal/proxy/keyauth_middleware_test.go` | Test | Update middleware tests |

### Core Implementation

**1. `internal/keygen/generator.go`**

**Changes**:
- Delete old `GenerateKey(username string)` implementation
- Add new `GenerateKey(username string, secret []byte) (string, error)`
- Add `encodeBase62HMAC(data []byte) string` helper
- Delete unused functions: `ExtractAlphanumeric`, `randomChar`, `randomPositions`

**New Implementation**:
```go
func GenerateKey(username string, secret []byte) (string, error) {
    if username == "" {
        return "", fmt.Errorf("username cannot be empty")
    }
    if len(secret) < 32 {
        return "", fmt.Errorf("secret must be at least 32 bytes")
    }

    // Compute HMAC-SHA256
    h := hmac.New(sha256.New, secret)
    h.Write([]byte(username))
    signature := h.Sum(nil)

    // Base62 encode and pad/truncate to KeyBodyLen
    body := encodeBase62HMAC(signature)

    key := KeyPrefix + body
    zap.L().Debug("api key generated (hmac)",
        zap.String("username", username),
        zap.Int("key_length", len(key)),
    )
    return key, nil
}
```

**2. `internal/keygen/validator.go`**

**Changes**:
- Modify `ValidateAndGetUser(key string, users []UserEntry, secret []byte)` signature
- Simplify validation: iterate + recompute HMAC + compare
- Delete `ContainsAllCharsWithCount` (no longer needed)
- Keep `IsValidFormat` (format check still required)
- Delete `ValidateUsername` (HMAC has no minimum length requirement)

**New Implementation**:
```go
func ValidateAndGetUser(key string, users []UserEntry, secret []byte) (*UserEntry, error) {
    if !IsValidFormat(key) {
        return nil, nil
    }

    for i := range users {
        u := &users[i]
        if !u.IsActive {
            continue
        }
        expectedKey, err := GenerateKey(u.Username, secret)
        if err != nil {
            zap.L().Warn("failed to generate key for user during validation",
                zap.String("username", u.Username),
                zap.Error(err),
            )
            continue
        }
        if key == expectedKey {
            zap.L().Debug("api key validated (hmac)",
                zap.String("username", u.Username),
            )
            return u, nil
        }
    }

    return nil, nil
}
```

**3. `internal/config/config.go`**

**Changes**:
- Add `KeygenSecret string` field to `SProxyAuth` struct
- Add validation: required, minimum 32 chars
- Support environment variable substitution

```go
type SProxyAuth struct {
    JWTSecret       string `yaml:"jwt_secret"`
    KeygenSecret    string `yaml:"keygen_secret"`  // New
    // ... other fields
}

func (c *SProxyFullConfig) Validate() error {
    // ... existing validations

    if c.Auth.KeygenSecret == "" {
        errs = append(errs, "auth.keygen_secret is required")
    } else if len(c.Auth.KeygenSecret) < 32 {
        errs = append(errs, "auth.keygen_secret should be at least 32 characters")
    }

    // ... rest of validations
}
```

**4. Caller Updates (3 locations)**

**`internal/api/keygen_handler.go`**:
```go
// In login handler
key, err := keygen.GenerateKey(username, []byte(h.config.Auth.KeygenSecret))

// In regenerate handler
key, err := keygen.GenerateKey(username, []byte(h.config.Auth.KeygenSecret))
```

**`internal/proxy/keyauth_middleware.go`**:
```go
// In KeyAuthMiddleware.authenticate()
user, err := keygen.ValidateAndGetUser(apiKey, users, []byte(m.keygenSecret))
```

**Constructor change**:
```go
func NewKeyAuthMiddleware(
    logger *zap.Logger,
    userLister ActiveUserLister,
    keygenSecret string,  // New parameter
    cache *keygen.KeyCache,
) *KeyAuthMiddleware {
    return &KeyAuthMiddleware{
        logger:       logger,
        userLister:   userLister,
        keygenSecret: keygenSecret,  // Store
        cache:        cache,
    }
}
```

**`cmd/sproxy/main.go`**:
```go
// KeyAuthMiddleware instantiation (around line 200+)
keyAuthMW := proxy.NewKeyAuthMiddleware(
    logger,
    dbUserLister,
    cfg.Auth.KeygenSecret,  // Pass keygen secret
    keyCache,
)

// KeygenHandler instantiation (around line 250+)
keygenHandler := api.NewKeygenHandler(
    logger,
    userRepo,
    cfg.Auth.KeygenSecret,  // Pass keygen secret
)
```
key, err := keygen.GenerateKey(username, []byte(h.config.Auth.KeygenSecret))
```

**5. Configuration Files**

**`config/sproxy.yaml.example`**:
```yaml
auth:
  jwt_secret: "${JWT_SECRET}"
  keygen_secret: "${KEYGEN_SECRET}"  # New field
```

### Dependencies

**`go.mod`**:
```
require github.com/jxskiss/base62 v1.1.0
```

---

## Testing Strategy

### Unit Tests

**1. `generator_test.go` Updates**

**New Tests**:
- `TestGenerateKey_Deterministic`: Same username + secret → same key
- `TestGenerateKey_DifferentUsernames`: Different usernames → different keys
- `TestGenerateKey_NoCollisions`: 1000 random usernames → 1000 unique keys
- `TestGenerateKey_FormatCorrect`: Key is 54 chars, Base62 charset
- `TestGenerateKey_EmptyUsername`: Returns error
- `TestGenerateKey_ShortSecret`: Secret < 32 bytes → error
- `TestGenerateKey_LongUsername`: Handles 1000-char username
- `TestGenerateKey_SpecialChars`: Handles unicode, symbols

**Delete**:
- Old fingerprint embedding tests
- `ExtractAlphanumeric` tests (function deleted)

**2. `validator_test.go` Updates**

**New Tests**:
- `TestValidateAndGetUser_CorrectKey`: Valid key → returns user
- `TestValidateAndGetUser_WrongKey`: Invalid key → returns nil
- `TestValidateAndGetUser_WrongSecret`: Correct key, wrong secret → nil
- `TestValidateAndGetUser_InvalidFormat`: Malformed key → fast reject
- `TestValidateAndGetUser_InactiveUser`: Inactive user's key → nil
- `TestValidateAndGetUser_MultipleUsers`: Finds correct user among 100
- `TestValidateAndGetUser_NoCollisions`: 1000 users, all keys unique

**Delete**:
- Old collision detection tests
- `ContainsAllCharsWithCount` tests (function deleted)
- `ValidateUsername` tests (function deleted)

**3. `cache_test.go`**

**No changes needed** (cache logic unchanged).

### Integration Tests

**1. `keygen_handler_test.go`**

**New Tests**:
- `TestKeygenHandler_Login_NewFormat`: Login returns 54-char HMAC key
- `TestKeygenHandler_Regenerate_NewFormat`: Regenerate returns HMAC key (idempotent)

**Note**: HMAC is deterministic, so "regenerate" for the same user always returns the same key. This is expected and acceptable behavior — users can safely regenerate if they lose their key.

**2. `keyauth_middleware_test.go`**

**New Tests**:
- `TestKeyAuthMiddleware_HMACKey_Success`: New format key authenticates
- `TestKeyAuthMiddleware_OldFormatKey_Rejected`: Old format key returns 401
- `TestKeyAuthMiddleware_WrongSecret_Rejected`: Key generated with different secret fails

### Performance Tests (New)

**`keygen_performance_test.go`**:
```go
func BenchmarkValidateAndGetUser_1000Users_WorstCase(b *testing.B) {
    // Setup: 1000 users, valid key for LAST user (worst case O(n))
    // Measure: validation time without cache
    // Target: < 10ms per validation
}

func BenchmarkValidateAndGetUser_1000Users_InvalidKey(b *testing.B) {
    // Setup: 1000 users, invalid key (full iteration, no early exit)
    // Measure: validation time without cache
    // Target: < 10ms per validation
}

func BenchmarkValidateAndGetUser_CacheHit(b *testing.B) {
    // Setup: cache with 100 keys
    // Measure: cache hit latency
    // Target: < 1μs per validation
}
```

### Test Coverage Target

- `generator.go`: 100% (all branches)
- `validator.go`: 100% (all branches)
- Integration tests: All keygen endpoints covered

---

## Migration Plan

### Pre-Upgrade Preparation

**Documentation**:
- Release notes: "⚠️ Breaking Change: All existing API Keys will be invalidated"
- User guide: "Record locations where API Keys are used before upgrading"

**Communication**:
- Email notification to admins
- Dashboard banner (if possible): "Upcoming v2.15.0 requires API Key regeneration"

### Upgrade Steps

**1. Stop Service**:
```bash
systemctl stop sproxy
```

**2. Generate Keygen Secret**:
```bash
# Generate 32-byte random secret
export KEYGEN_SECRET=$(openssl rand -base64 32)

# Add to environment file
echo "KEYGEN_SECRET=${KEYGEN_SECRET}" >> /etc/pairproxy/env
```

**3. Update Configuration**:
```yaml
# /etc/pairproxy/sproxy.yaml
auth:
  jwt_secret: "${JWT_SECRET}"
  keygen_secret: "${KEYGEN_SECRET}"  # Add this line
```

**4. Update Binary**:
```bash
wget https://github.com/l17728/pairproxy/releases/download/v2.15.0/sproxy-linux-amd64
mv sproxy-linux-amd64 /usr/local/bin/sproxy
chmod +x /usr/local/bin/sproxy
```

**5. Start Service**:
```bash
systemctl start sproxy
```

**6. Verify**:
```bash
# Check logs for successful startup
journalctl -u sproxy -f | grep "keygen_secret"

# Should see: "config loaded successfully"
```

### User Actions

**1. Regenerate Keys**:
- Visit `/keygen/` or Dashboard
- Login with username + password
- Copy new API key

**2. Update Client Configuration**:
```bash
# Update environment variable
export ANTHROPIC_API_KEY="sk-pp-<new_48_chars>"

# Or update config file
echo "ANTHROPIC_API_KEY=sk-pp-<new_48_chars>" > ~/.claude/api_key
```

**3. Verify**:
```bash
# Test with Claude CLI or direct API call
curl -H "x-api-key: sk-pp-<new_key>" http://sproxy:9000/v1/messages
```

### Rollback Plan

**If issues occur**:
```bash
# 1. Stop v2.15.0
systemctl stop sproxy

# 2. Restore v2.14.1 binary
mv /usr/local/bin/sproxy.backup /usr/local/bin/sproxy

# 3. Remove keygen_secret from config
# (v2.14.1 will ignore unknown fields)

# 4. Start v2.14.1
systemctl start sproxy

# 5. Old keys work again
```

**Database State**: No schema changes in v2.15.0, rollback is safe at database level.

**Key Invalidation**: Keys generated during v2.15.0 (HMAC-based) will become invalid after rollback to v2.14.1 (fingerprint-based). Users who regenerated keys during v2.15.0 must regenerate again after rollback.

**Note**: Rollback requires users to regenerate keys again when re-upgrading to v2.15.0.

---

## Documentation Updates

### 1. `docs/manual.md`

**Section to Update**: §8 LLM Target 动态管理 → Add keygen subsection

**Content**:
```markdown
### 8.X API Key 生成（Direct Proxy）

**Key 格式**: `sk-pp-<48字符>` (总长度 54)

**生成方式**:
1. 访问 `/keygen/` 或 Dashboard
2. 使用用户名 + 密码登录
3. 系统生成基于 HMAC-SHA256 的唯一 key

**安全特性**:
- 密码学保证无碰撞（collision-free）
- 每个用户名对应唯一 key
- 服务端使用 `keygen_secret` 签名

**配置**:
```yaml
auth:
  keygen_secret: "${KEYGEN_SECRET}"  # 至少 32 字符
```

**注意**: v2.15.0 起，旧格式 key 不再有效，需重新生成。
```

### 2. `README.md`

**Update**: Direct Proxy feature description

**Before**:
```markdown
| **Direct Proxy（v2.9.0）** | `sk-pp-` API Key 直连，无需 cproxy；访问 `/keygen/` 自助生成 Key |
```

**After**:
```markdown
| **Direct Proxy（v2.9.0）** | `sk-pp-` API Key 直连，无需 cproxy；HMAC-SHA256 签名保证无碰撞（v2.15.0 算法升级） |
```

### 3. `docs/UPGRADE.md`

**New Section**: v2.15.0 Upgrade Guide

**Content**:
```markdown
## v2.15.0 升级指南

### ⚠️ 不兼容变更：API Key 算法升级

**影响**: 所有现有 `sk-pp-` API Keys 将失效

**原因**: 旧算法存在碰撞风险，新算法使用 HMAC-SHA256 保证唯一性

**升级步骤**:

1. **配置新增**:
   ```yaml
   auth:
     keygen_secret: "${KEYGEN_SECRET}"  # 新增必填项
   ```

2. **生成密钥**:
   ```bash
   export KEYGEN_SECRET=$(openssl rand -base64 32)
   ```

3. **重启服务**:
   ```bash
   systemctl restart sproxy
   ```

4. **用户操作**:
   - 访问 `/keygen/` 重新生成 API Key
   - 更新客户端配置中的 `ANTHROPIC_API_KEY`

**验证**:
```bash
# 旧 key 返回 401
curl -H "x-api-key: sk-pp-<old_key>" http://sproxy:9000/v1/messages
# 响应: {"error": "unauthorized"}

# 新 key 正常工作
curl -H "x-api-key: sk-pp-<new_key>" http://sproxy:9000/v1/messages
# 响应: 正常 LLM 响应
```
```

### 4. `config/sproxy.yaml.example`

**Add**:
```yaml
auth:
  jwt_secret: "${JWT_SECRET}"
  keygen_secret: "${KEYGEN_SECRET}"  # v2.15.0 新增：API Key HMAC 签名密钥（至少 32 字符）
```

---

## Logging & Observability

### Log Messages

**Key Generation**:
```go
zap.L().Debug("api key generated (hmac)",
    zap.String("username", username),
    zap.Int("key_length", len(key)),
)
```

**Key Validation Success**:
```go
zap.L().Debug("api key validated (hmac)",
    zap.String("username", u.Username),
)
```

**Key Validation Failure**:
- Invalid format or no match: silent rejection (no log, for security)
- Internal errors during validation (e.g., GenerateKey fails): logged as WARN

**Configuration Validation**:
```go
zap.L().Info("keygen secret configured",
    zap.Int("secret_length", len(config.Auth.KeygenSecret)),
)
```

**Startup Verification**:
```go
zap.L().Info("keygen module initialized",
    zap.String("algorithm", "hmac-sha256"),
    zap.String("encoding", "base62"),
)
```

### Metrics (Optional Future Enhancement)

**Prometheus Metrics**:
- `keygen_validations_total{result="success|failure"}` — Validation attempts
- `keygen_validation_duration_seconds` — Validation latency histogram
- `keygen_cache_hit_ratio` — Cache effectiveness

---

## Security Considerations

### Secret Management

**Best Practices**:
- Generate with `openssl rand -base64 32` or equivalent
- Store in environment variables, not in config files
- Rotate periodically (requires all users to regenerate keys)
- Never log the secret value

**Secret Rotation**:
1. Generate new secret
2. Update `KEYGEN_SECRET` environment variable
3. Restart sproxy
4. All users regenerate keys (old keys immediately invalid)

**Impact**: Secret rotation is a **breaking operation** requiring coordination:
- All existing API keys become invalid immediately
- Users must regenerate keys before they can make requests
- No grace period or dual-secret support (hard cutover)
- Plan rotation during maintenance windows with user notification

### Attack Vectors

**Brute Force**:
- 62^48 possible keys (286 bits of entropy)
- Infeasible to brute force

**Collision Attack**:
- HMAC-SHA256 collision resistance: 2^128 operations
- Truncated to 48 chars: still 2^143 collision resistance
- Cryptographically secure

**Secret Leakage**:
- If `keygen_secret` leaks, attacker can generate valid keys for any username
- Mitigation: Rotate secret immediately, invalidate all keys

---

## Success Criteria

### Functional Requirements

✅ Zero collision probability (cryptographic guarantee)
✅ Deterministic key generation (same user → same key)
✅ Backward incompatible (hard cutover, no legacy support)
✅ 54-character key length preserved
✅ Configuration validation enforces 32-char minimum secret

### Performance Requirements

✅ Key generation: < 1ms per key
✅ Key validation (no cache): < 10ms for 1000 users
✅ Key validation (cache hit): < 1μs
✅ Cache hit ratio: > 95% in production

### Testing Requirements

✅ 100% unit test coverage for generator.go and validator.go
✅ Integration tests for all keygen endpoints
✅ Performance benchmarks for 1000-user scenario
✅ No collision in 10,000 random username test

### Documentation Requirements

✅ Manual updated with new keygen section
✅ Upgrade guide with step-by-step instructions
✅ README updated with algorithm change note
✅ Config example includes keygen_secret

---

## Implementation Checklist

- [ ] Add `github.com/jxskiss/base62` dependency
- [ ] Update `internal/config/config.go` (KeygenSecret field + validation)
- [ ] Rewrite `internal/keygen/generator.go` (HMAC-based generation)
- [ ] Rewrite `internal/keygen/validator.go` (HMAC-based validation)
- [ ] Update `internal/api/keygen_handler.go` (pass secret parameter)
- [ ] Update `internal/proxy/keyauth_middleware.go` (pass secret parameter)
- [ ] Update `config/sproxy.yaml.example` (add keygen_secret)
- [ ] Rewrite `generator_test.go` (new test cases)
- [ ] Rewrite `validator_test.go` (new test cases)
- [ ] Add `keygen_performance_test.go` (benchmarks)
- [ ] Update `keygen_handler_test.go` (integration tests)
- [ ] Update `keyauth_middleware_test.go` (integration tests)
- [ ] Update `docs/manual.md` (keygen section)
- [ ] Update `docs/UPGRADE.md` (v2.15.0 guide)
- [ ] Update `README.md` (Direct Proxy description)
- [ ] Run full test suite (2,465+ tests)
- [ ] Run performance benchmarks
- [ ] Manual testing: generate key, validate key, old key rejected
- [ ] Update release notes

---

## Appendix: Algorithm Comparison

### Old Algorithm (v2.9.0 - v2.14.1)

**Generation**:
1. Extract alphanumeric chars from username → fingerprint
2. Fill 48-byte body with random chars
3. Scatter fingerprint chars at random positions
4. Prepend "sk-pp-"

**Validation**:
1. For each user, extract fingerprint
2. Check if key body contains all fingerprint chars (with counts)
3. Return longest matching fingerprint
4. Collision if multiple users have same fingerprint length

**Collision Example**:
- `alice123` → `[a,l,i,c,e,1,2,3]` (8 chars)
- `321ecila` → `[3,2,1,e,c,i,l,a]` (8 chars)
- Both match any key containing these 8 chars → collision

### New Algorithm (v2.15.0+)

**Generation**:
1. Compute HMAC-SHA256(secret, username)
2. Base62 encode → 48 chars
3. Prepend "sk-pp-"

**Validation**:
1. For each user, recompute HMAC-SHA256(secret, username)
2. Compare with provided key
3. Return matched user

**Collision Guarantee**:
- HMAC-SHA256 cryptographic property: different inputs → different outputs
- Collision probability < 2^-143 (negligible)

---

**End of Design Document**
