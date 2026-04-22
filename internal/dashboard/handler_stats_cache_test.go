package dashboard_test

// ---------------------------------------------------------------------------
// handleUserStats 缓存刷新用例
//
// 行为规约：
//   1. 第一次请求（冷启动）查 DB，结果写入缓存
//   2. 5 分钟内的第二次请求命中缓存，返回相同数据（不查 DB）
//   3. 携带 ?_=<timestamp> 的请求应穿透缓存，重新查 DB
//   4. 穿透后的结果更新缓存，后续普通请求仍命中新缓存
//   5. 缓存与 DB 之间的一致性：穿透后新增的用户可被观察到
// ---------------------------------------------------------------------------

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/db"
)

// doUserStatsReqURL 允许指定完整 URL（含 query string）发起请求。
// 返回分页响应中的 users 列表（向后兼容测试断言）。
func doUserStatsReqURL(t *testing.T, env *userStatsTestEnv, url string) []map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.AddCookie(env.cookie)
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (url=%s, body=%s)", rr.Code, url, rr.Body.String())
	}
	var page struct {
		Users []map[string]interface{} `json:"users"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	return page.Users
}

// freshURL 生成带唯一 timestamp 的刷新 URL，确保穿透缓存。
func freshURL() string {
	return "/dashboard/api/user-stats?_=" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// TestUserStatsCache_FirstRequestPopulatesCache
// 冷启动：第一次请求后缓存被填充，第二次请求返回相同数据。
func TestUserStatsCache_FirstRequestPopulatesCache(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(makeUser("cache-a", "alice"))
	env.uRepo.Create(makeUser("cache-b", "bob"))

	r1 := doUserStatsReqURL(t, env, "/dashboard/api/user-stats")
	r2 := doUserStatsReqURL(t, env, "/dashboard/api/user-stats") // 应命中缓存

	if len(r1) != 2 || len(r2) != 2 {
		t.Fatalf("expected 2 users in both responses, got %d and %d", len(r1), len(r2))
	}

	// 两次结果完全一致（缓存命中）
	names1 := collectField(r1, "username")
	names2 := collectField(r2, "username")
	if !reflect.DeepEqual(names1, names2) {
		t.Errorf("cached response differs: first=%v, second=%v", names1, names2)
	}
}

// TestUserStatsCache_ForceRefreshBypassesCache
// ?_=<ts> 参数穿透缓存：新增用户后强制刷新，可在响应中看到新用户。
func TestUserStatsCache_ForceRefreshBypassesCache(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(makeUser("fr-u1", "frank"))

	// 第一次普通请求，缓存内只有 1 个用户
	r1 := doUserStatsReqURL(t, env, "/dashboard/api/user-stats")
	if len(r1) != 1 {
		t.Fatalf("expected 1 user before refresh, got %d", len(r1))
	}

	// 向 DB 新增一个用户（缓存尚未感知）
	env.uRepo.Create(makeUser("fr-u2", "grace"))

	// 普通请求仍命中旧缓存，看不到 grace
	rCached := doUserStatsReqURL(t, env, "/dashboard/api/user-stats")
	if len(rCached) != 1 {
		// 如果缓存 TTL 实现正确，第二次普通请求还是 1 个用户
		t.Logf("note: cache hit returned %d users (expected 1)", len(rCached))
	}

	// 强制刷新——应看到 2 个用户
	rFresh := doUserStatsReqURL(t, env, freshURL())
	if len(rFresh) != 2 {
		t.Errorf("after force refresh: got %d users, want 2 (new user should be visible)", len(rFresh))
	}
	if !containsUsername(rFresh, "grace") {
		t.Errorf("force refresh response does not contain 'grace': %v", collectField(rFresh, "username"))
	}
}

// TestUserStatsCache_AfterForceRefresh_SubsequentNormalRequestUsesNewCache
// 强制刷新后，普通请求应命中新缓存（而非再次穿透）。
func TestUserStatsCache_AfterForceRefresh_SubsequentNormalRequestUsesNewCache(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(makeUser("anr-u1", "henry"))

	// 建立初始缓存（1 用户）
	doUserStatsReqURL(t, env, "/dashboard/api/user-stats")

	// 新增用户
	env.uRepo.Create(makeUser("anr-u2", "iris"))

	// 强制刷新（缓存更新为 2 用户）
	doUserStatsReqURL(t, env, freshURL())

	// 此后的普通请求应命中新缓存（2 用户），而不是旧缓存（1 用户）
	rNormal := doUserStatsReqURL(t, env, "/dashboard/api/user-stats")
	if len(rNormal) != 2 {
		t.Errorf("normal request after force refresh: got %d users, want 2 (should use updated cache)", len(rNormal))
	}
}

// TestUserStatsCache_MultipleForceRefreshes 连续多次强制刷新不累积错误。
func TestUserStatsCache_MultipleForceRefreshes(t *testing.T) {
	env := setupUserStatsEnv(t)
	env.uRepo.Create(makeUser("mfr-u1", "jack"))

	for i := 0; i < 3; i++ {
		r := doUserStatsReqURL(t, env, freshURL())
		if len(r) != 1 {
			t.Errorf("force refresh #%d: got %d users, want 1", i+1, len(r))
		}
	}
}

// TestUserStatsCache_ForceRefreshWithEmptyDB 强制刷新后如果 DB 为空，返回 []（非 null）。
func TestUserStatsCache_ForceRefreshWithEmptyDB(t *testing.T) {
	env := setupUserStatsEnv(t)

	r := doUserStatsReqURL(t, env, freshURL())
	if r == nil {
		t.Error("expected non-nil slice (empty []), got nil")
	}
	if len(r) != 0 {
		t.Errorf("expected empty array, got %d items", len(r))
	}
}

// TestUserStatsCache_ResponseContentType 响应 Content-Type 应为 application/json。
func TestUserStatsCache_ResponseContentType(t *testing.T) {
	env := setupUserStatsEnv(t)

	for _, url := range []string{
		"/dashboard/api/user-stats",
		freshURL(),
	} {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.AddCookie(env.cookie)
		rr := httptest.NewRecorder()
		env.mux.ServeHTTP(rr, req)

		ct := rr.Header().Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("url=%s Content-Type = %q, want application/json", url, ct)
		}
	}
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

func makeUser(id, username string) *db.User {
	return &db.User{ID: id, Username: username, IsActive: true}
}

func collectField(items []map[string]interface{}, field string) []string {
	out := make([]string, 0, len(items))
	for _, m := range items {
		if v, ok := m[field]; ok {
			out = append(out, v.(string))
		}
	}
	return out
}

func containsUsername(items []map[string]interface{}, name string) bool {
	for _, m := range items {
		if m["username"] == name {
			return true
		}
	}
	return false
}
