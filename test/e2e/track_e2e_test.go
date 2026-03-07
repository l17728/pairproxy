package e2e_test

// track_e2e_test.go — E2E tests for per-user conversation content tracking.
//
// Validates the full proxy chain:
//   client → sproxy (with Tracker) → mock LLM → sproxy ModifyResponse → file on disk
//
// Covers:
//   1. Non-streaming: tracked user gets a JSON file with messages + response
//   2. Streaming (Anthropic SSE): tracked user gets accumulated text
//   3. Untracked user: no file written
//   4. Tracking enable/disable live: file written only while enabled
//   5. OpenAI format: tracked via /v1/chat/completions

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/proxy"
	"github.com/l17728/pairproxy/internal/quota"
	"github.com/l17728/pairproxy/internal/tap"
	"github.com/l17728/pairproxy/internal/track"
)

// ---------------------------------------------------------------------------
// 测试环境辅助
// ---------------------------------------------------------------------------

// trackEnv 封装对话跟踪 E2E 测试所需的完整环境。
type trackEnv struct {
	Server   *httptest.Server
	Client   *http.Client
	JWTMgr  *auth.Manager
	Tracker  *track.Tracker
	TrackDir string
	cancel   context.CancelFunc
	t        *testing.T
}

// setupTrackEnv 创建含 ConvTracker 的 sproxy 测试环境。
func setupTrackEnv(t *testing.T, mockLLMURL string) *trackEnv {
	t.Helper()
	logger := zaptest.NewLogger(t)

	jwtMgr, err := auth.NewManager(logger, "track-e2e-secret")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	gormDB, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := db.Migrate(logger, gormDB); err != nil {
		t.Fatalf("db.Migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := db.NewUsageWriter(gormDB, logger, 200, 30*time.Second)
	writer.Start(ctx)

	sp, err := proxy.NewSProxy(logger, jwtMgr, writer, []proxy.LLMTarget{
		{URL: mockLLMURL, APIKey: "track-test-key"},
	})
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	// 注册配额检查（保持与生产一致）
	userRepo := db.NewUserRepo(gormDB, logger)
	usageRepo := db.NewUsageRepo(gormDB, logger)
	quotaCache := quota.NewQuotaCache(5 * time.Second)
	checker := quota.NewChecker(logger, userRepo, usageRepo, quotaCache)
	sp.SetQuotaChecker(checker)

	// 创建并注入 Tracker
	trackDir := filepath.Join(t.TempDir(), "track")
	tr, err := track.New(trackDir)
	if err != nil {
		t.Fatalf("track.New: %v", err)
	}
	sp.SetConvTracker(tr)

	mux := http.NewServeMux()
	mux.Handle("/", sp.Handler())
	srv := httptest.NewServer(mux)

	env := &trackEnv{
		Server:   srv,
		Client:   &http.Client{Timeout: 10 * time.Second},
		JWTMgr:  jwtMgr,
		Tracker:  tr,
		TrackDir: trackDir,
		cancel:   cancel,
		t:        t,
	}
	t.Cleanup(func() {
		srv.Close()
		cancel()
		writer.Wait()
	})
	return env
}

// jwtFor 为用户名签发 JWT。
func (e *trackEnv) jwtFor(username string) string {
	e.t.Helper()
	token, err := e.JWTMgr.Sign(
		auth.JWTClaims{UserID: username, Username: username},
		time.Hour,
	)
	if err != nil {
		e.t.Fatalf("Sign JWT for %s: %v", username, err)
	}
	return token
}

// postAnthropicMessages 向 sproxy POST /v1/messages，返回完整响应。
func (e *trackEnv) postAnthropicMessages(token, bodyJSON string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost,
		e.Server.URL+"/v1/messages",
		strings.NewReader(bodyJSON))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Client.Do(req)
	if err != nil {
		e.t.Fatalf("POST /v1/messages: %v", err)
	}
	return resp
}

// postOpenAI 向 sproxy POST /v1/chat/completions，返回完整响应。
func (e *trackEnv) postOpenAI(token, bodyJSON string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost,
		e.Server.URL+"/v1/chat/completions",
		strings.NewReader(bodyJSON))
	req.Header.Set("X-PairProxy-Auth", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Client.Do(req)
	if err != nil {
		e.t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	return resp
}

// convFiles 返回指定用户目录下的所有对话 JSON 文件。
func (e *trackEnv) convFiles(username string) []string {
	e.t.Helper()
	dir := e.Tracker.UserConvDir(username)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		e.t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var files []string
	for _, ent := range entries {
		files = append(files, filepath.Join(dir, ent.Name()))
	}
	return files
}

// readConvRecord 读取并解析第一个（通常是唯一）对话记录文件。
func (e *trackEnv) readConvRecord(username string) track.ConversationRecord {
	e.t.Helper()
	files := e.convFiles(username)
	if len(files) == 0 {
		e.t.Fatalf("no conversation files found for user %q", username)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		e.t.Fatalf("ReadFile %s: %v", files[0], err)
	}
	var rec track.ConversationRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		e.t.Fatalf("unmarshal ConversationRecord: %v\nraw: %s", err, data)
	}
	return rec
}

// ---------------------------------------------------------------------------
// Test 1：非流式响应 — Anthropic 格式
// ---------------------------------------------------------------------------

func TestTrackE2E_NonStreaming_Anthropic(t *testing.T) {
	const respBody = `{"id":"msg1","type":"message","role":"assistant",` +
		`"content":[{"type":"text","text":"I am Claude!"}],` +
		`"usage":{"input_tokens":12,"output_tokens":8}}`

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, respBody)
	}))
	defer mockLLM.Close()

	env := setupTrackEnv(t, mockLLM.URL)

	// 启用跟踪
	if err := env.Tracker.Enable("alice"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	reqBody := `{"model":"claude-3","messages":[{"role":"user","content":"Hello Claude"}]}`
	resp := env.postAnthropicMessages(env.jwtFor("alice"), reqBody)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// 验证文件已写入
	files := env.convFiles("alice")
	if len(files) != 1 {
		t.Fatalf("expected 1 conversation file, got %d", len(files))
	}

	rec := env.readConvRecord("alice")
	if rec.Username != "alice" {
		t.Errorf("username = %q, want alice", rec.Username)
	}
	if len(rec.Messages) != 1 || rec.Messages[0].Content != "Hello Claude" {
		t.Errorf("messages = %+v, want [{user Hello Claude}]", rec.Messages)
	}
	if rec.Response != "I am Claude!" {
		t.Errorf("response = %q, want %q", rec.Response, "I am Claude!")
	}
	if rec.InputTokens != 12 || rec.OutputTokens != 8 {
		t.Errorf("tokens: in=%d out=%d, want in=12 out=8", rec.InputTokens, rec.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Test 2：流式响应 — Anthropic SSE
// ---------------------------------------------------------------------------

func TestTrackE2E_Streaming_Anthropic(t *testing.T) {
	sse := tap.BuildAnthropicSSE(20, 10, []string{"Hello", ", world", "!"})

	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher := w.(http.Flusher)
		// 分块写入（模拟真实流式传输）
		scanner := bufio.NewScanner(strings.NewReader(sse))
		for scanner.Scan() {
			fmt.Fprintln(w, scanner.Text())
			flusher.Flush()
		}
	}))
	defer mockLLM.Close()

	env := setupTrackEnv(t, mockLLM.URL)
	if err := env.Tracker.Enable("bob"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	reqBody := `{"model":"claude-3","stream":true,"messages":[{"role":"user","content":"Stream test"}]}`
	resp := env.postAnthropicMessages(env.jwtFor("bob"), reqBody)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// SSE 结束时 FeedChunk 触发自动 Flush，稍等一下让文件写完
	time.Sleep(50 * time.Millisecond)

	files := env.convFiles("bob")
	if len(files) != 1 {
		t.Fatalf("expected 1 conversation file, got %d", len(files))
	}

	rec := env.readConvRecord("bob")
	if rec.Response != "Hello, world!" {
		t.Errorf("response = %q, want %q", rec.Response, "Hello, world!")
	}
	if rec.Username != "bob" {
		t.Errorf("username = %q, want bob", rec.Username)
	}
}

// ---------------------------------------------------------------------------
// Test 3：未跟踪用户 — 不产生文件
// ---------------------------------------------------------------------------

func TestTrackE2E_UntrackedUser_NoFile(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m","type":"message","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer mockLLM.Close()

	env := setupTrackEnv(t, mockLLM.URL)
	// carol 未调用 Enable，不应有任何记录

	resp := env.postAnthropicMessages(env.jwtFor("carol"),
		`{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	files := env.convFiles("carol")
	if len(files) != 0 {
		t.Errorf("untracked user: expected 0 files, got %d", len(files))
	}
}

// ---------------------------------------------------------------------------
// Test 4：启用 → 请求 → 禁用 → 再请求 — 只有一个文件
// ---------------------------------------------------------------------------

func TestTrackE2E_EnableThenDisable(t *testing.T) {
	reqNum := 0
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqNum++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"m%d","type":"message","content":[{"type":"text","text":"reply%d"}],"usage":{"input_tokens":1,"output_tokens":1}}`, reqNum, reqNum)
	}))
	defer mockLLM.Close()

	env := setupTrackEnv(t, mockLLM.URL)
	token := env.jwtFor("dave")
	body := `{"model":"claude-3","messages":[{"role":"user","content":"ping"}]}`

	// 第 1 次请求：未启用 → 无文件
	resp := env.postAnthropicMessages(token, body)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if files := env.convFiles("dave"); len(files) != 0 {
		t.Errorf("before enable: expected 0 files, got %d", len(files))
	}

	// 启用跟踪
	if err := env.Tracker.Enable("dave"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// 第 2 次请求：已启用 → 产生 1 个文件
	resp = env.postAnthropicMessages(token, body)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if files := env.convFiles("dave"); len(files) != 1 {
		t.Errorf("while enabled: expected 1 file, got %d", len(files))
	}

	// 禁用跟踪
	if err := env.Tracker.Disable("dave"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// 第 3 次请求：已禁用 → 文件数保持 1
	resp = env.postAnthropicMessages(token, body)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if files := env.convFiles("dave"); len(files) != 1 {
		t.Errorf("after disable: expected 1 file (no new), got %d", len(files))
	}
}

// ---------------------------------------------------------------------------
// Test 5：OpenAI 格式 — 非流式
// ---------------------------------------------------------------------------

func TestTrackE2E_NonStreaming_OpenAI(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion",`+
			`"choices":[{"message":{"role":"assistant","content":"OpenAI reply"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer mockLLM.Close()

	env := setupTrackEnv(t, mockLLM.URL)
	if err := env.Tracker.Enable("eve"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	reqBody := `{"model":"gpt-4","messages":[{"role":"user","content":"OpenAI test"}]}`
	resp := env.postOpenAI(env.jwtFor("eve"), reqBody)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	files := env.convFiles("eve")
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	rec := env.readConvRecord("eve")
	if rec.Response != "OpenAI reply" {
		t.Errorf("response = %q, want %q", rec.Response, "OpenAI reply")
	}
	if len(rec.Messages) != 1 || rec.Messages[0].Content != "OpenAI test" {
		t.Errorf("messages = %+v", rec.Messages)
	}
	if rec.InputTokens != 5 || rec.OutputTokens != 3 {
		t.Errorf("tokens: in=%d out=%d, want in=5 out=3", rec.InputTokens, rec.OutputTokens)
	}
}

// ---------------------------------------------------------------------------
// Test 6：多用户隔离 — 只有被跟踪用户有文件
// ---------------------------------------------------------------------------

func TestTrackE2E_MultiUserIsolation(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m","type":"message","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer mockLLM.Close()

	env := setupTrackEnv(t, mockLLM.URL)

	// 只跟踪 frank，不跟踪 grace
	if err := env.Tracker.Enable("frank"); err != nil {
		t.Fatalf("Enable frank: %v", err)
	}

	body := `{"model":"claude-3","messages":[{"role":"user","content":"hi"}]}`

	// frank 和 grace 各发一次请求
	for _, user := range []string{"frank", "grace"} {
		resp := env.postAnthropicMessages(env.jwtFor(user), body)
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
	}

	if files := env.convFiles("frank"); len(files) != 1 {
		t.Errorf("frank (tracked): expected 1 file, got %d", len(files))
	}
	if files := env.convFiles("grace"); len(files) != 0 {
		t.Errorf("grace (untracked): expected 0 files, got %d", len(files))
	}
}

// ---------------------------------------------------------------------------
// Test 7：JSON 记录字段完整性
// ---------------------------------------------------------------------------

func TestTrackE2E_RecordFieldCompleteness(t *testing.T) {
	mockLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"m","type":"message","content":[{"type":"text","text":"complete reply"}],"usage":{"input_tokens":15,"output_tokens":7}}`)
	}))
	defer mockLLM.Close()

	env := setupTrackEnv(t, mockLLM.URL)
	if err := env.Tracker.Enable("henry"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	before := time.Now()
	reqBody := `{"model":"claude-3-opus","messages":[{"role":"user","content":"Full test"},{"role":"assistant","content":"Sure"},{"role":"user","content":"Thanks"}]}`
	resp := env.postAnthropicMessages(env.jwtFor("henry"), reqBody)
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	resp.Body.Close()
	after := time.Now()

	rec := env.readConvRecord("henry")

	// request_id 非空
	if rec.RequestID == "" {
		t.Error("request_id should not be empty")
	}
	// username 正确
	if rec.Username != "henry" {
		t.Errorf("username = %q, want henry", rec.Username)
	}
	// timestamp 在请求时间范围内
	if rec.Timestamp.Before(before.Add(-time.Second)) || rec.Timestamp.After(after.Add(time.Second)) {
		t.Errorf("timestamp %v out of range [%v, %v]", rec.Timestamp, before, after)
	}
	// model 从请求体提取
	if rec.Model != "claude-3-opus" {
		t.Errorf("model = %q, want claude-3-opus", rec.Model)
	}
	// messages 数量正确
	if len(rec.Messages) != 3 {
		t.Errorf("messages count = %d, want 3", len(rec.Messages))
	}
	// response 文本正确
	if rec.Response != "complete reply" {
		t.Errorf("response = %q, want complete reply", rec.Response)
	}
	// token 数量正确
	if rec.InputTokens != 15 || rec.OutputTokens != 7 {
		t.Errorf("tokens: in=%d out=%d, want in=15 out=7", rec.InputTokens, rec.OutputTokens)
	}
}
