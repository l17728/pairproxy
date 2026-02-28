package tap

import (
	"testing"
)

// ---------------------------------------------------------------------------
// NewResponseParser factory
// ---------------------------------------------------------------------------

func TestNewResponseParser_Anthropic(t *testing.T) {
	p := NewResponseParser("anthropic", nil)
	if _, ok := p.(*AnthropicSSEParser); !ok {
		t.Errorf("expected *AnthropicSSEParser for provider='anthropic', got %T", p)
	}
}

func TestNewResponseParser_EmptyDefault(t *testing.T) {
	p := NewResponseParser("", nil)
	if _, ok := p.(*AnthropicSSEParser); !ok {
		t.Errorf("expected *AnthropicSSEParser for provider='', got %T", p)
	}
}

func TestNewResponseParser_OpenAI(t *testing.T) {
	p := NewResponseParser("openai", nil)
	if _, ok := p.(*OpenAISSEParser); !ok {
		t.Errorf("expected *OpenAISSEParser for provider='openai', got %T", p)
	}
}

func TestNewResponseParser_Ollama(t *testing.T) {
	p := NewResponseParser("ollama", nil)
	if _, ok := p.(*OpenAISSEParser); !ok {
		t.Errorf("expected *OpenAISSEParser for provider='ollama', got %T", p)
	}
}

func TestNewResponseParser_UnknownFallback(t *testing.T) {
	p := NewResponseParser("bedrock", nil)
	if _, ok := p.(*AnthropicSSEParser); !ok {
		t.Errorf("expected *AnthropicSSEParser fallback for unknown provider, got %T", p)
	}
}

// ---------------------------------------------------------------------------
// ResponseParser interface compliance
// ---------------------------------------------------------------------------

// TestResponseParserInterface verifies that both concrete parsers satisfy the interface.
func TestResponseParserInterface(t *testing.T) {
	var _ ResponseParser = &AnthropicSSEParser{}
	var _ ResponseParser = &OpenAISSEParser{}
}

// ---------------------------------------------------------------------------
// AnthropicSSEParser.ParseNonStreaming（通过接口调用）
// ---------------------------------------------------------------------------

func TestAnthropicParser_ParseNonStreamingViaInterface(t *testing.T) {
	p := NewResponseParser("anthropic", nil)
	body := []byte(`{"id":"msg_1","type":"message","usage":{"input_tokens":100,"output_tokens":50}}`)
	in, out := p.ParseNonStreaming(body)
	if in != 100 {
		t.Errorf("input_tokens = %d, want 100", in)
	}
	if out != 50 {
		t.Errorf("output_tokens = %d, want 50", out)
	}
}

func TestAnthropicParser_ParseNonStreamingViaInterface_Invalid(t *testing.T) {
	p := NewResponseParser("anthropic", nil)
	in, out := p.ParseNonStreaming([]byte("not json"))
	if in != 0 || out != 0 {
		t.Errorf("expected 0,0 for invalid JSON, got %d,%d", in, out)
	}
}
