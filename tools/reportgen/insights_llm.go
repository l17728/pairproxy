package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// llmTarget holds the upstream URL, decrypted API key, provider, and model.
type llmTarget struct {
	URL      string
	APIKey   string
	Provider string // "anthropic" | "openai"
	Model    string // overrides per-provider default when set
}

// QueryLLMTarget reads the first active LLM target from the DB and decrypts its API key.
// KEK (key encryption key) is read from the KEY_ENCRYPTION_KEY environment variable.
// Supports both SQLite and PostgreSQL databases.
func QueryLLMTarget(driver, dsn string) (*llmTarget, error) {
	kek := os.Getenv("KEY_ENCRYPTION_KEY")
	if kek == "" {
		return nil, errors.New("KEY_ENCRYPTION_KEY env var not set; skipping LLM insights")
	}

	if driver == "" {
		driver = "sqlite"
	}

	var driverName string
	switch driver {
	case "postgres":
		driverName = "postgres"
	default:
		driverName = "sqlite"
		driver = "sqlite"
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	// Join llm_targets → api_keys to get url + encrypted key in one query.
	row := db.QueryRow(`
		SELECT t.url, t.provider, COALESCE(k.encrypted_value, '')
		FROM llm_targets t
		LEFT JOIN api_keys k ON k.id = t.api_key_id AND k.is_active = 1
		WHERE t.is_active = 1
		  AND t.provider IN ('anthropic', 'openai')
		ORDER BY t.id
		LIMIT 1
	`)

	var target llmTarget
	var encryptedKey string
	if err := row.Scan(&target.URL, &target.Provider, &encryptedKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("no active LLM target found in database; skipping LLM insights")
		}
		return nil, fmt.Errorf("query llm_targets: %w", err)
	}

	if encryptedKey == "" {
		return nil, errors.New("LLM target has no associated api_key; skipping LLM insights")
	}

	plain, err := aesGCMDecrypt(encryptedKey, kek)
	if err != nil {
		return nil, fmt.Errorf("decrypt api key: %w", err)
	}
	target.APIKey = plain
	return &target, nil
}

// aesGCMDecrypt mirrors internal/auth/encrypt.go Decrypt.
// Format: base64(nonce[12] || ciphertext || tag[16])
// Key derivation: SHA-256 of the KEK string.
func aesGCMDecrypt(ciphertext64, key string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext64)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	h := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(h[:])
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}
	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	plain, err := gcm.Open(nil, data[:nonceSize], data[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("GCM open: %w", err)
	}
	return string(plain), nil
}

// GenerateLLMInsights calls the upstream LLM with the full report data.
// On context-too-long errors it retries with error_requests and slow_requests stripped.
// Returns a single Insight of type "llm_analysis", or nil if LLM is unavailable.
//
// Priority: command-line -llm-url/-llm-key params → database query (with KEY_ENCRYPTION_KEY).
func GenerateLLMInsights(data *ReportData, params QueryParams) *Insight {
	var target *llmTarget

	if params.LLMURL != "" && params.LLMKey != "" {
		// Use directly-specified URL and key (OpenAI-compatible API)
		model := params.LLMModel
		if model == "" {
			model = "gpt-4o-mini"
		}
		target = &llmTarget{
			URL:      params.LLMURL,
			APIKey:   params.LLMKey,
			Provider: "openai",
			Model:    model,
		}
	} else {
		var err error
		target, err = QueryLLMTarget(params.Driver, params.DSN)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  LLM insights skipped: %v\n", err)
			return nil
		}
	}

	// Attempt 1: full report JSON.
	result, err := callLLM(target, data, false)
	if err != nil {
		if isContextTooLong(err) {
			// Attempt 2: strip large detail arrays.
			fmt.Fprintf(os.Stderr, "⚠️  LLM context too long, retrying without error/slow details...\n")
			result, err = callLLM(target, data, true)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠️  LLM insights failed: %v\n", err)
			return nil
		}
	}

	return &Insight{
		Type:   "llm_analysis",
		Title:  "🤖 AI 智能洞察",
		Detail: result,
		Emoji:  "🤖",
	}
}

// callLLM serialises ReportData (optionally stripping large arrays) and sends it to the LLM.
func callLLM(target *llmTarget, data *ReportData, stripDetails bool) (string, error) {
	payload := data
	if stripDetails {
		// Shallow copy and zero out the large detail slices.
		stripped := *data
		stripped.ErrorRequests = nil
		stripped.SlowRequests = nil
		stripped.IOScatterPlot = nil
		stripped.RetentionData = nil
		payload = &stripped
	}

	reportJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}

	systemPrompt := `你是一名专业的 AI 平台运营分析师。
你会收到一份 PairProxy API 网关的 JSON 格式使用报告。
请从以下三个视角给出深度分析，使用中文，每个视角 3~5 条关键洞察：

**使用者视角**：关注请求量、模型偏好、延迟体验、错误频率。
**运维视角**：关注错误率、慢请求、上游健康、流量峰值分布。
**管理者视角**：关注费用趋势、用户采纳率、分组差异、帕累托集中度。

输出格式为纯文本，分三段，每段标题加粗，条目用「•」开头。不要输出 JSON。`

	userMsg := fmt.Sprintf("以下是报告数据（JSON）：\n\n```json\n%s\n```\n\n请开始分析。", string(reportJSON))

	// Use Anthropic native format only when provider is "anthropic" and no explicit model override.
	// All other cases (including -llm-url direct params) use OpenAI-compatible /v1/chat/completions.
	if target.Provider == "anthropic" && target.Model == "" {
		return callAnthropic(target, systemPrompt, userMsg)
	}
	return callOpenAI(target, systemPrompt, userMsg)
}

// callAnthropic sends a request to the Anthropic Messages API.
func callAnthropic(target *llmTarget, system, userMsg string) (string, error) {
	endpoint := strings.TrimRight(target.URL, "/") + "/v1/messages"
	model := target.Model
	if model == "" {
		model = "claude-haiku-4-5-20251001" // fast + cheap for analysis
	}
	body := map[string]interface{}{
		"model":      model,
		"max_tokens": 2048,
		"system":     system,
		"messages": []map[string]string{
			{"role": "user", "content": userMsg},
		},
	}
	return doPost(endpoint, target.APIKey, "x-api-key", body, extractAnthropic)
}

// callOpenAI sends a request to the OpenAI-compatible Chat Completions API.
// Works with OpenAI, sproxy, and any OpenAI-compatible endpoint.
func callOpenAI(target *llmTarget, system, userMsg string) (string, error) {
	endpoint := strings.TrimRight(target.URL, "/") + "/v1/chat/completions"
	model := target.Model
	if model == "" {
		model = "gpt-4o-mini"
	}
	body := map[string]interface{}{
		"model":      model,
		"max_tokens": 2048,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": userMsg},
		},
	}
	return doPost(endpoint, target.APIKey, "Bearer", body, extractOpenAI)
}

func doPost(endpoint, apiKey, authScheme string, body interface{}, extract func([]byte) (string, error)) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if authScheme == "x-api-key" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &llmError{status: resp.StatusCode, body: string(respBody)}
	}

	return extract(respBody)
}

func extractAnthropic(body []byte) (string, error) {
	var resp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse anthropic response: %w", err)
	}
	for _, c := range resp.Content {
		if c.Type == "text" {
			return strings.TrimSpace(c.Text), nil
		}
	}
	return "", errors.New("no text content in anthropic response")
}

func extractOpenAI(body []byte) (string, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("parse openai response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("no choices in openai response")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// llmError carries HTTP status for context-too-long detection.
type llmError struct {
	status int
	body   string
}

func (e *llmError) Error() string {
	return fmt.Sprintf("LLM returned HTTP %d: %s", e.status, e.body)
}

// isContextTooLong detects context-length errors from Anthropic and OpenAI.
func isContextTooLong(err error) bool {
	var le *llmError
	if !errors.As(err, &le) {
		return false
	}
	// Anthropic: 400 with "context window" in body
	// OpenAI: 400 with "context_length_exceeded" or "maximum context length"
	lower := strings.ToLower(le.body)
	return le.status == 400 && (
		strings.Contains(lower, "context window") ||
			strings.Contains(lower, "context_length_exceeded") ||
			strings.Contains(lower, "maximum context length") ||
			strings.Contains(lower, "too many tokens") ||
			strings.Contains(lower, "reduce the length"))
}
