package proxy

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/router"
)

// SProxyClassifierTarget 实现 router.ClassifierTarget，
// 复用 SProxy 的现有 LB 逻辑选取分类器 LLM 端点。
// 依赖方向：proxy → router（单向，无循环）。
type SProxyClassifierTarget struct {
	sp     *SProxy
	logger *zap.Logger
}

// NewSProxyClassifierTarget 创建 SProxyClassifierTarget。
func NewSProxyClassifierTarget(sp *SProxy, logger *zap.Logger) router.ClassifierTarget {
	return &SProxyClassifierTarget{
		sp:     sp,
		logger: logger.Named("classifier_target"),
	}
}

// Pick 从现有 LB 池中选取一个健康 target，用于分类器调用。
// 使用空 userID/groupID（无绑定），直接走 LB 负载均衡路径。
func (a *SProxyClassifierTarget) Pick(ctx context.Context) (targetURL, apiKey string, err error) {
	info, err := a.sp.pickLLMTarget("/v1/messages", "", "", "", nil, nil)
	if err != nil {
		a.logger.Warn("classifier_target: failed to pick LLM target",
			zap.Error(err),
		)
		return "", "", fmt.Errorf("classifier_target: no healthy target: %w", err)
	}
	a.logger.Debug("classifier_target: picked LLM target",
		zap.String("url", info.URL),
	)
	return info.URL, info.APIKey, nil
}
