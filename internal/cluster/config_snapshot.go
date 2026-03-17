package cluster

import (
	"time"

	"github.com/l17728/pairproxy/internal/db"
)

// ConfigSnapshot 配置快照，由 Primary 节点生成，Worker 节点定期拉取并同步到本地 DB。
//
// 包含的数据类型：
//   - Users：所有用户（含密码 hash 和 IsActive 状态，用于 P0-1 禁用传播）
//   - Groups：所有分组（含配额限制）
//   - LLMTargets：所有 LLM 目标端点（元数据，不含 API Key 明文）
//   - LLMBindings：所有 LLM 绑定关系（用户/分组 → target URL）
//
// 安全说明：快照通过 cluster.shared_secret Bearer token 认证的内部 API 传输。
// API Key 明文不包含在快照中（LLMTarget.APIKeyID 为 FK，不含加密值）。
type ConfigSnapshot struct {
	Version     time.Time       `json:"version"`      // 快照生成时间（Primary 服务器时间）
	Users       []db.User       `json:"users"`        // 所有用户记录（含 IsActive 状态）
	Groups      []db.Group      `json:"groups"`       // 所有分组记录（含配额）
	LLMTargets  []*db.LLMTarget `json:"llm_targets"`  // 所有 LLM 目标端点（元数据）
	LLMBindings []db.LLMBinding `json:"llm_bindings"` // 所有 LLM 绑定关系
}
