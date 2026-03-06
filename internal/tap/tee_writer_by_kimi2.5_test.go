package tap

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/db"
)

// setupTestDB creates an in-memory test database
func setupTestDB(t *testing.T) (*db.UsageWriter, func()) {
	t.Helper()
	logger := zaptest.NewLogger(t)
	database, err := db.Open(logger, ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := db.Migrate(logger, database); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	writer := db.NewUsageWriter(database, logger, 100, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	writer.Start(ctx)

	cleanup := func() {
		cancel()
		writer.Wait()
		sqlDB, _ := database.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}

	return writer, cleanup
}

// TestTeeResponseWriter_New tests creating a new TeeResponseWriter
func TestTeeResponseWriter_New_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
		Model:     "claude-3",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", time.Now(), nil)
	if tw == nil {
		t.Fatal("expected TeeResponseWriter, got nil")
	}

	if tw.statusCode != http.StatusOK {
		t.Errorf("expected default status %d, got %d", http.StatusOK, tw.statusCode)
	}
}

// TestTeeResponseWriter_WriteHeader tests WriteHeader method
func TestTeeResponseWriter_WriteHeader_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", time.Now(), nil)

	tw.WriteHeader(http.StatusCreated)
	if tw.StatusCode() != http.StatusCreated {
		t.Errorf("expected status %d, got %d", http.StatusCreated, tw.StatusCode())
	}

	if rec.Code != http.StatusCreated {
		t.Errorf("expected recorder status %d, got %d", http.StatusCreated, rec.Code)
	}
}

// TestTeeResponseWriter_Write tests basic Write method
func TestTeeResponseWriter_Write_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", time.Now(), nil)

	data := []byte("data: test\n\n")
	n, err := tw.Write(data)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected %d bytes written, got %d", len(data), n)
	}

	if !bytes.Equal(rec.Body.Bytes(), data) {
		t.Errorf("expected body %q, got %q", data, rec.Body.Bytes())
	}
}

// TestTeeResponseWriter_NonStreaming tests non-streaming response handling
func TestTeeResponseWriter_NonStreaming_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", time.Now(), nil)

	data := []byte(`{"message": "hello"}`)
	tw.Write(data)

	if tw.isStreaming {
		t.Error("expected non-streaming mode")
	}
}

// TestTeeResponseWriter_StreamingDetection tests streaming detection
func TestTeeResponseWriter_StreamingDetection_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", time.Now(), nil)

	tw.Write([]byte("data: hello\n\n"))

	if !tw.isStreaming {
		t.Error("expected streaming mode to be detected")
	}
}

// TestTeeResponseWriter_Flush tests Flush method
func TestTeeResponseWriter_Flush_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", time.Now(), nil)

	tw.Flush()
}

// TestTeeResponseWriter_TTFBMs tests TTFB calculation
func TestTeeResponseWriter_TTFBMs_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	startTime := time.Now()
	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", startTime, nil)

	if tw.TTFBMs() != 0 {
		t.Errorf("expected TTFB 0 before write, got %d", tw.TTFBMs())
	}

	time.Sleep(10 * time.Millisecond)
	tw.Write([]byte("test"))

	ttfb := tw.TTFBMs()
	if ttfb < 5 {
		t.Errorf("expected TTFB >= 5ms, got %d", ttfb)
	}
}

// TestTeeResponseWriter_UpdateModel tests UpdateModel method
func TestTeeResponseWriter_UpdateModel_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
		Model:     "initial-model",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", time.Now(), nil)

	tw.UpdateModel("updated-model")

	if tw.record.Model != "updated-model" {
		t.Errorf("expected model 'updated-model', got %q", tw.record.Model)
	}
}

// TestTeeResponseWriter_StatusCode tests StatusCode method
func TestTeeResponseWriter_StatusCode_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "anthropic", time.Now(), nil)

	if tw.StatusCode() != http.StatusOK {
		t.Errorf("expected default status %d, got %d", http.StatusOK, tw.StatusCode())
	}

	tw.WriteHeader(http.StatusNotFound)
	if tw.StatusCode() != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, tw.StatusCode())
	}
}

// TestTeeResponseWriter_OpenAIProvider tests with OpenAI provider
func TestTeeResponseWriter_OpenAIProvider_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "openai", time.Now(), nil)

	if tw.parser == nil {
		t.Error("expected parser to be set")
	}
}

// TestTeeResponseWriter_OllamaProvider tests with Ollama provider
func TestTeeResponseWriter_OllamaProvider_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "text/event-stream")
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "ollama", time.Now(), nil)

	if tw.parser == nil {
		t.Error("expected parser to be set")
	}
}

// TestTeeResponseWriter_EmptyProvider tests with empty provider
func TestTeeResponseWriter_EmptyProvider_by_kimi2_5(t *testing.T) {
	logger := zaptest.NewLogger(t)
	writer, cleanup := setupTestDB(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	record := db.UsageRecord{
		RequestID: "test-123",
		UserID:    "user-1",
	}

	tw := NewTeeResponseWriter(rec, logger, writer, record, "", time.Now(), nil)

	if tw.parser == nil {
		t.Error("expected parser to be set even with empty provider")
	}
}

// TestTeeResponseWriter_OnChunkCallback tests t
