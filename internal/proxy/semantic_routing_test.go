package proxy

import (
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/corpus"
	"github.com/l17728/pairproxy/internal/db"
)

func TestExtractMessagesFromBody(t *testing.T) {
	t.Run("valid messages", func(t *testing.T) {
		body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]}`)
		msgs := extractMessagesFromBody(body)
		if len(msgs) != 2 {
			t.Fatalf("len = %d, want 2", len(msgs))
		}
		if msgs[0].Role != "user" || msgs[0].Content != "hello" {
			t.Errorf("msgs[0] = %+v", msgs[0])
		}
		if msgs[1].Role != "assistant" || msgs[1].Content != "hi" {
			t.Errorf("msgs[1] = %+v", msgs[1])
		}
	})

	t.Run("empty messages array", func(t *testing.T) {
		body := []byte(`{"model":"claude-3","messages":[]}`)
		msgs := extractMessagesFromBody(body)
		if len(msgs) != 0 {
			t.Errorf("len = %d, want 0", len(msgs))
		}
	})

	t.Run("no messages field", func(t *testing.T) {
		body := []byte(`{"model":"claude-3"}`)
		msgs := extractMessagesFromBody(body)
		if msgs != nil && len(msgs) != 0 {
			t.Errorf("expected nil/empty, got %v", msgs)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		body := []byte(`not json at all`)
		msgs := extractMessagesFromBody(body)
		if msgs != nil {
			t.Errorf("expected nil for malformed JSON, got %v", msgs)
		}
	})

	t.Run("nil body", func(t *testing.T) {
		msgs := extractMessagesFromBody(nil)
		if msgs != nil {
			t.Errorf("expected nil for nil body, got %v", msgs)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		msgs := extractMessagesFromBody([]byte{})
		if msgs != nil {
			t.Errorf("expected nil for empty body, got %v", msgs)
		}
	})
}

// TestPickLLMTarget_CandidateFilter 测试 candidateFilter 参数对目标选择的影响
func TestPickLLMTarget_CandidateFilter(t *testing.T) {
	logger := zaptest.NewLogger(t)
	jwtMgr, err := auth.NewManager(logger, "test-secret-key")
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
	writer := db.NewUsageWriter(gormDB, logger, 100, time.Minute)

	targets := []LLMTarget{
		{URL: "http://llm-a:8080", APIKey: "key-a"},
		{URL: "http://llm-b:8080", APIKey: "key-b"},
		{URL: "http://llm-c:8080", APIKey: "key-c"},
	}

	sp, err := NewSProxy(logger, jwtMgr, writer, targets)
	if err != nil {
		t.Fatalf("NewSProxy: %v", err)
	}

	t.Run("nil filter returns any target", func(t *testing.T) {
		info, err := sp.pickLLMTarget("/v1/messages", "", "", nil, nil)
		if err != nil {
			t.Fatalf("pickLLMTarget: %v", err)
		}
		found := false
		for _, tgt := range targets {
			if tgt.URL == info.URL {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected URL %q", info.URL)
		}
	})

	t.Run("filter narrows candidates", func(t *testing.T) {
		filter := []string{"http://llm-b:8080"}
		info, err := sp.pickLLMTarget("/v1/messages", "", "", nil, filter)
		if err != nil {
			t.Fatalf("pickLLMTarget: %v", err)
		}
		if info.URL != "http://llm-b:8080" {
			t.Errorf("URL = %q, want %q", info.URL, "http://llm-b:8080")
		}
	})

	t.Run("filter with no matching candidates returns error", func(t *testing.T) {
		filter := []string{"http://nonexistent:8080"}
		_, err := sp.pickLLMTarget("/v1/messages", "", "", nil, filter)
		if err == nil {
			t.Fatal("expected error for filter with no matching candidates")
		}
	})

	t.Run("filter combined with tried excludes both", func(t *testing.T) {
		filter := []string{"http://llm-a:8080", "http://llm-b:8080"}
		tried := []string{"http://llm-a:8080"}
		info, err := sp.pickLLMTarget("/v1/messages", "", "", tried, filter)
		if err != nil {
			t.Fatalf("pickLLMTarget: %v", err)
		}
		if info.URL != "http://llm-b:8080" {
			t.Errorf("URL = %q, want %q", info.URL, "http://llm-b:8080")
		}
	})
}

// ensure corpus import used
var _ = corpus.Message{}
