package proxy

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
)

func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// makeTestSProxyWithModel 创建带模型路由解析器的 SProxy（单元测试用）
func makeTestSProxyWithModel(t *testing.T) *SProxy {
	t.Helper()
	logger := zap.NewNop()
	jwtMgr, err := auth.NewManager(logger, "test-secret-32chars-padding-ok")
	require.NoError(t, err)
	writer := &db.UsageWriter{}
	targets := []LLMTarget{
		{URL: "https://api.anthropic.com", APIKey: "key-ant", Provider: "anthropic", Weight: 1},
		{URL: "http://localhost:11434", APIKey: "ollama", Provider: "ollama", Weight: 1},
	}
	sp, err := NewSProxy(logger, jwtMgr, writer, targets)
	require.NoError(t, err)
	return sp
}

// ---------------------------------------------------------------------------
// SetModelResolver / pickLLMTarget 模型路由单元测试
// ---------------------------------------------------------------------------

func TestSProxy_SetModelResolver(t *testing.T) {
	sp := makeTestSProxyWithModel(t)
	assert.Nil(t, sp.modelResolver, "初始 modelResolver 应为 nil")

	sp.SetModelResolver(func(modelID string) (string, string, bool) {
		return "https://api.anthropic.com", modelID, true
	})
	assert.NotNil(t, sp.modelResolver)
}

func TestSProxy_SetAutoModelResolver(t *testing.T) {
	sp := makeTestSProxyWithModel(t)
	assert.Nil(t, sp.autoModelResolver, "初始 autoModelResolver 应为 nil")

	sp.SetAutoModelResolver(func(userID, groupID string) (string, bool) {
		return "claude-sonnet-4-5", true
	})
	assert.NotNil(t, sp.autoModelResolver)
}

func TestPickLLMTarget_ModelRouting_MatchesTarget(t *testing.T) {
	sp := makeTestSProxyWithModel(t)

	// 无 bindingResolver，modelResolver 将生效
	sp.SetModelResolver(func(modelID string) (string, string, bool) {
		if modelID == "llama3.2" {
			return "http://localhost:11434", "llama3.2", true
		}
		return "", "", false
	})

	info, err := sp.pickLLMTarget("/v1/messages", "user-1", "", "llama3.2", nil)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:11434", info.URL)
	assert.Equal(t, "llama3.2", info.OverrideModel)
}

func TestPickLLMTarget_ModelRouting_NoMatchFallsThrough(t *testing.T) {
	sp := makeTestSProxyWithModel(t)

	// modelResolver 找不到时退化到简单轮询
	sp.SetModelResolver(func(modelID string) (string, string, bool) {
		return "", "", false
	})

	info, err := sp.pickLLMTarget("/v1/messages", "user-1", "", "unknown-model", nil)
	require.NoError(t, err)
	// 轮询会返回 targets 中的某一个
	assert.NotEmpty(t, info.URL)
	assert.Empty(t, info.OverrideModel, "无模型路由时 OverrideModel 应为空")
}

func TestPickLLMTarget_ModelRouting_EmptyModelIDSkipsResolver(t *testing.T) {
	sp := makeTestSProxyWithModel(t)

	called := false
	sp.SetModelResolver(func(modelID string) (string, string, bool) {
		called = true
		return "http://localhost:11434", "llama3.2", true
	})

	// model ID 为空时不调用 modelResolver
	_, err := sp.pickLLMTarget("/v1/messages", "user-1", "", "", nil)
	require.NoError(t, err)
	assert.False(t, called, "modelID 为空时 modelResolver 不应被调用")
}

func TestPickLLMTarget_BindingTakesPriorityOverModelRouting(t *testing.T) {
	sp := makeTestSProxyWithModel(t)

	// 设置绑定解析器（强制使用 Anthropic）
	sp.SetBindingResolver(func(userID, groupID string) (string, bool) {
		return "https://api.anthropic.com", true
	})

	// modelResolver 指向 Ollama
	resolverCalled := false
	sp.SetModelResolver(func(modelID string) (string, string, bool) {
		resolverCalled = true
		return "http://localhost:11434", "llama3.2", true
	})

	// bindingResolver 存在时，modelResolver 不应被调用
	info, err := sp.pickLLMTarget("/v1/messages", "user-1", "", "llama3.2", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://api.anthropic.com", info.URL, "绑定优先级高于模型路由")
	assert.False(t, resolverCalled, "bindingResolver 存在时 modelResolver 不应被调用")
}

func TestPickLLMTarget_ModelRouting_AlreadyTriedSkips(t *testing.T) {
	sp := makeTestSProxyWithModel(t)

	sp.SetModelResolver(func(modelID string) (string, string, bool) {
		return "http://localhost:11434", "llama3.2", true
	})

	// 已尝试过 Ollama，应退化到轮询
	info, err := sp.pickLLMTarget("/v1/messages", "user-1", "", "llama3.2", []string{"http://localhost:11434"})
	require.NoError(t, err)
	// 轮询到另一个 target
	assert.NotEqual(t, "http://localhost:11434", info.URL, "已尝试过的 target 不应再选")
}

// ---------------------------------------------------------------------------
// replaceModelInBody 单元测试
// ---------------------------------------------------------------------------

func TestReplaceModelInBody_ReplacesModelField(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-5","max_tokens":100}`)
	result, err := replaceModelInBody(body, "llama3.2")
	require.NoError(t, err)

	// 验证替换结果
	replaced := extractModelFromBody(result)
	assert.Equal(t, "llama3.2", replaced)
}

func TestReplaceModelInBody_InvalidJSON_ReturnsOriginal(t *testing.T) {
	body := []byte(`not-json`)
	result, err := replaceModelInBody(body, "llama3.2")
	assert.Error(t, err)
	assert.Equal(t, body, result, "解析失败时应返回原始 body")
}

func TestReplaceModelInBody_PreservesOtherFields(t *testing.T) {
	body := []byte(`{"model":"old-model","max_tokens":200,"stream":true}`)
	result, err := replaceModelInBody(body, "new-model")
	require.NoError(t, err)

	var parsed map[string]interface{}
	require.NoError(t, jsonUnmarshal(result, &parsed))
	assert.Equal(t, "new-model", parsed["model"])
	assert.Equal(t, float64(200), parsed["max_tokens"])
	assert.Equal(t, true, parsed["stream"])
}

// ---------------------------------------------------------------------------
// LLMTargetInfo.OverrideModel 单元测试
// ---------------------------------------------------------------------------

func TestLLMTargetInfo_OverrideModel_DefaultEmpty(t *testing.T) {
	info := &lb.LLMTargetInfo{
		URL:    "https://api.anthropic.com",
		APIKey: "key",
	}
	assert.Empty(t, info.OverrideModel)
}

func TestLLMTargetInfo_OverrideModel_SetAndGet(t *testing.T) {
	info := &lb.LLMTargetInfo{
		URL:           "http://localhost:11434",
		APIKey:        "ollama",
		OverrideModel: "llama3.2",
	}
	assert.Equal(t, "llama3.2", info.OverrideModel)
}
