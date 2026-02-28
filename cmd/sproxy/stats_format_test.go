package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/l17728/pairproxy/internal/db"
)

var (
	testFrom = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	testTo   = time.Date(2024, 1, 7, 23, 59, 59, 0, time.UTC)
)

// ---------------------------------------------------------------------------
// printUserStats
// ---------------------------------------------------------------------------

func TestPrintUserStats_Text(t *testing.T) {
	var buf bytes.Buffer
	err := printUserStats(&buf, "text", "alice", "uid-123", 1000, 500, 7, testFrom, testTo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"alice", "uid-123", "1000", "500", "1500", "7 day"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q, got:\n%s", want, out)
		}
	}
}

func TestPrintUserStats_JSON(t *testing.T) {
	var buf bytes.Buffer
	err := printUserStats(&buf, "json", "alice", "uid-123", 1000, 500, 7, testFrom, testTo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if result["user"] != "alice" {
		t.Errorf("json user = %v, want alice", result["user"])
	}
	if result["user_id"] != "uid-123" {
		t.Errorf("json user_id = %v, want uid-123", result["user_id"])
	}
	// JSON numbers are float64 when unmarshaled into interface{}
	if v, ok := result["input_tokens"].(float64); !ok || int(v) != 1000 {
		t.Errorf("json input_tokens = %v, want 1000", result["input_tokens"])
	}
	if v, ok := result["total_tokens"].(float64); !ok || int(v) != 1500 {
		t.Errorf("json total_tokens = %v, want 1500", result["total_tokens"])
	}
}

func TestPrintUserStats_CSV(t *testing.T) {
	var buf bytes.Buffer
	err := printUserStats(&buf, "csv", "alice", "uid-123", 1000, 500, 7, testFrom, testTo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (header + data), got %d:\n%s", len(lines), buf.String())
	}
	// Header must contain expected columns
	if !strings.Contains(lines[0], "username") || !strings.Contains(lines[0], "total_tokens") {
		t.Errorf("CSV header missing expected columns: %s", lines[0])
	}
	// Data row must contain username and token values
	if !strings.Contains(lines[1], "alice") || !strings.Contains(lines[1], "1500") {
		t.Errorf("CSV data row missing expected values: %s", lines[1])
	}
}

func TestPrintUserStats_InvalidFormat(t *testing.T) {
	// Invalid format falls through to the default text case — no error.
	var buf bytes.Buffer
	err := printUserStats(&buf, "xml", "bob", "uid-456", 100, 50, 1, testFrom, testTo)
	if err != nil {
		t.Fatalf("unexpected error for unsupported format: %v", err)
	}
}

// ---------------------------------------------------------------------------
// printGlobalStats
// ---------------------------------------------------------------------------

func sampleGlobalStats() db.GlobalStats {
	return db.GlobalStats{
		TotalInput:   5000,
		TotalOutput:  2000,
		TotalTokens:  7000,
		RequestCount: 100,
		ErrorCount:   3,
	}
}

func sampleUserRows() []db.UserStatRow {
	return []db.UserStatRow{
		{UserID: "user-a", TotalInput: 3000, TotalOutput: 1200, RequestCount: 60},
		{UserID: "user-b", TotalInput: 2000, TotalOutput: 800, RequestCount: 40},
	}
}

func TestPrintGlobalStats_Text(t *testing.T) {
	var buf bytes.Buffer
	err := printGlobalStats(&buf, "text", sampleGlobalStats(), sampleUserRows(), 7, testFrom, testTo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"5000", "2000", "7000", "100", "3", "user-a", "user-b", "Top users"} {
		if !strings.Contains(out, want) {
			t.Errorf("output should contain %q, got:\n%s", want, out)
		}
	}
}

func TestPrintGlobalStats_JSON(t *testing.T) {
	var buf bytes.Buffer
	err := printGlobalStats(&buf, "json", sampleGlobalStats(), sampleUserRows(), 7, testFrom, testTo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
	if v, ok := result["total_tokens"].(float64); !ok || int(v) != 7000 {
		t.Errorf("json total_tokens = %v, want 7000", result["total_tokens"])
	}
	if v, ok := result["requests"].(float64); !ok || int(v) != 100 {
		t.Errorf("json requests = %v, want 100", result["requests"])
	}
	topUsers, ok := result["top_users"].([]interface{})
	if !ok || len(topUsers) != 2 {
		t.Errorf("json top_users should have 2 entries, got: %v", result["top_users"])
	}
}

func TestPrintGlobalStats_CSV(t *testing.T) {
	var buf bytes.Buffer
	err := printGlobalStats(&buf, "csv", sampleGlobalStats(), sampleUserRows(), 7, testFrom, testTo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out := buf.String()
	// Summary header + data row
	if !strings.Contains(out, "period_days,from,to") {
		t.Errorf("CSV should contain summary header, got:\n%s", out)
	}
	if !strings.Contains(out, "7000") {
		t.Errorf("CSV should contain total_tokens 7000, got:\n%s", out)
	}
	// Top users header + rows
	if !strings.Contains(out, "user_id,input_tokens") {
		t.Errorf("CSV should contain top-users header, got:\n%s", out)
	}
	if !strings.Contains(out, "user-a") {
		t.Errorf("CSV should contain user-a, got:\n%s", out)
	}
}

func TestPrintGlobalStats_CSVNoTopUsers(t *testing.T) {
	var buf bytes.Buffer
	err := printGlobalStats(&buf, "csv", sampleGlobalStats(), nil, 7, testFrom, testTo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should not print top-users section when rows is nil
	if strings.Contains(buf.String(), "user_id") {
		t.Errorf("CSV should not contain user_id header when rows is empty, got:\n%s", buf.String())
	}
}
