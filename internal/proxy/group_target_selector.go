package proxy

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// GroupTargetSelector 在 Group 内选择 target
type GroupTargetSelector struct {
	repo          *db.GroupTargetSetRepo
	logger        *zap.Logger
	mu            sync.RWMutex
	strategyCache map[string]SelectionStrategy
}

// SelectionStrategy 选择策略接口
type SelectionStrategy interface {
	Select(targets []db.TargetWithWeight, tried map[string]bool) *db.TargetWithWeight
}

// WeightedRandomStrategy 加权随机选择
type WeightedRandomStrategy struct {
	rng *rand.Rand
}

// NewWeightedRandomStrategy 创建加权随机选择策略
func NewWeightedRandomStrategy() *WeightedRandomStrategy {
	return &WeightedRandomStrategy{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Select 执行加权随机选择
func (s *WeightedRandomStrategy) Select(targets []db.TargetWithWeight, tried map[string]bool) *db.TargetWithWeight {
	// 过滤未尝试的健康 targets
	available := make([]db.TargetWithWeight, 0, len(targets))
	for _, t := range targets {
		if !tried[t.URL] && t.Healthy {
			available = append(available, t)
		}
	}

	if len(available) == 0 {
		return nil
	}

	// 计算总权重
	totalWeight := 0
	for _, t := range available {
		totalWeight += t.Weight
	}

	if totalWeight == 0 {
		return nil
	}

	// 加权随机选择
	r := s.rng.Intn(totalWeight)
	for i := range available {
		r -= available[i].Weight
		if r < 0 {
			return &available[i]
		}
	}

	return &available[0]
}

// RoundRobinStrategy 轮询选择
type RoundRobinStrategy struct {
	counter map[string]int
	mu      sync.Mutex
}

// NewRoundRobinStrategy 创建轮询选择策略
func NewRoundRobinStrategy() *RoundRobinStrategy {
	return &RoundRobinStrategy{
		counter: make(map[string]int),
	}
}

// Select 执行轮询选择
func (s *RoundRobinStrategy) Select(targets []db.TargetWithWeight, tried map[string]bool) *db.TargetWithWeight {
	// 过滤未尝试的健康 targets
	available := make([]db.TargetWithWeight, 0, len(targets))
	for _, t := range targets {
		if !tried[t.URL] && t.Healthy {
			available = append(available, t)
		}
	}

	if len(available) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 获取计数器
	key := fmt.Sprintf("%v", available)
	idx := s.counter[key] % len(available)
	s.counter[key]++

	return &available[idx]
}

// PriorityStrategy 优先级选择
type PriorityStrategy struct{}

// NewPriorityStrategy 创建优先级选择策略
func NewPriorityStrategy() *PriorityStrategy {
	return &PriorityStrategy{}
}

// Select 执行优先级选择
func (s *PriorityStrategy) Select(targets []db.TargetWithWeight, tried map[string]bool) *db.TargetWithWeight {
	// 过滤未尝试的健康 targets
	available := make([]db.TargetWithWeight, 0, len(targets))
	for _, t := range targets {
		if !tried[t.URL] && t.Healthy {
			available = append(available, t)
		}
	}

	if len(available) == 0 {
		return nil
	}

	// 按优先级排序（优先级越小越优先）
	minPriority := available[0].Priority
	for _, t := range available {
		if t.Priority < minPriority {
			minPriority = t.Priority
		}
	}

	for i := range available {
		if available[i].Priority == minPriority {
			return &available[i]
		}
	}

	return &available[0]
}

// SelectedTarget 选中的 target 信息
type SelectedTarget struct {
	URL      string
	Weight   int
	Priority int
}

// NewGroupTargetSelector 创建选择器
func NewGroupTargetSelector(
	repo *db.GroupTargetSetRepo,
	logger *zap.Logger,
) *GroupTargetSelector {
	return &GroupTargetSelector{
		repo:          repo,
		logger:        logger.Named("group_target_selector"),
		strategyCache: make(map[string]SelectionStrategy),
	}
}

// getStrategy 获取或创建选择策略
func (s *GroupTargetSelector) getStrategy(strategyName string) SelectionStrategy {
	s.mu.RLock()
	if strategy, ok := s.strategyCache[strategyName]; ok {
		s.mu.RUnlock()
		return strategy
	}
	s.mu.RUnlock()

	var strategy SelectionStrategy
	switch strategyName {
	case "round_robin":
		strategy = NewRoundRobinStrategy()
	case "priority":
		strategy = NewPriorityStrategy()
	default:
		strategy = NewWeightedRandomStrategy()
	}

	s.mu.Lock()
	s.strategyCache[strategyName] = strategy
	s.mu.Unlock()

	return strategy
}

// SelectTarget 为指定 Group 选择 target
// 返回：选中的 target、是否还有其他可用 targets、错误
func (s *GroupTargetSelector) SelectTarget(
	ctx context.Context,
	groupID string,
	tried []string,
) (*SelectedTarget, bool, error) {
	// 构建已尝试的 map
	triedSet := make(map[string]bool, len(tried))
	for _, u := range tried {
		triedSet[u] = true
	}

	// 获取 Group 的可用 targets
	targets, err := s.repo.GetAvailableTargetsForGroup(groupID)
	if err != nil {
		s.logger.Error("failed to get available targets",
			zap.String("group_id", groupID),
			zap.Error(err),
		)
		return nil, false, fmt.Errorf("get available targets: %w", err)
	}

	if len(targets) == 0 {
		s.logger.Warn("no available targets for group",
			zap.String("group_id", groupID),
		)
		return nil, false, fmt.Errorf("no available targets for group %s", groupID)
	}

	// 获取 target set 以确定选择策略
	var targetSet *db.GroupTargetSet
	if groupID == "" {
		ts, err := s.repo.GetDefault()
		if err != nil {
			s.logger.Error("failed to get default target set",
				zap.Error(err),
			)
			return nil, false, fmt.Errorf("get default target set: %w", err)
		}
		targetSet = ts
	} else {
		// 获取该 group 的所有 target set，选择默认的或第一个
		sets, err := s.repo.ListByGroupID(groupID)
		if err != nil {
			s.logger.Error("failed to list target sets",
				zap.String("group_id", groupID),
				zap.Error(err),
			)
			return nil, false, fmt.Errorf("list target sets: %w", err)
		}

		if len(sets) == 0 {
			s.logger.Warn("no target sets found for group",
				zap.String("group_id", groupID),
			)
			return nil, false, fmt.Errorf("no target sets found for group %s", groupID)
		}

		// 策略: 优先选择 is_default=true 的，其次选择第一个
		targetSet = &sets[0]
		for i := range sets {
			if sets[i].IsDefault {
				targetSet = &sets[i]
				break
			}
		}
	}

	if targetSet == nil {
		s.logger.Warn("target set not found for group",
			zap.String("group_id", groupID),
		)
		return nil, false, fmt.Errorf("target set not found for group %s", groupID)
	}

	// 获取选择策略
	strategy := s.getStrategy(targetSet.Strategy)

	// 执行选择
	selected := strategy.Select(targets, triedSet)
	if selected == nil {
		s.logger.Warn("no healthy targets available",
			zap.String("group_id", groupID),
			zap.Int("tried_count", len(tried)),
		)
		return nil, false, fmt.Errorf("no healthy targets available for group %s", groupID)
	}

	// 检查是否还有其他可用 targets
	hasMore := false
	for _, t := range targets {
		if !triedSet[t.URL] && t.Healthy && t.URL != selected.URL {
			hasMore = true
			break
		}
	}

	result := &SelectedTarget{
		URL:      selected.URL,
		Weight:   selected.Weight,
		Priority: selected.Priority,
	}

	s.logger.Debug("target selected",
		zap.String("group_id", groupID),
		zap.String("target_url", result.URL),
		zap.Bool("has_more", hasMore),
	)

	return result, hasMore, nil
}

// UpdateTargetHealth 更新 target 的健康状态
func (s *GroupTargetSelector) UpdateTargetHealth(targetURL string, healthy bool) error {
	status := "healthy"
	if !healthy {
		status = "unhealthy"
	}

	// 更新所有包含该 target 的 target set members
	if err := s.repo.UpdateTargetHealth(targetURL, healthy); err != nil {
		s.logger.Error("failed to update target health",
			zap.String("target_url", targetURL),
			zap.String("status", status),
			zap.Error(err),
		)
		return fmt.Errorf("update target health: %w", err)
	}

	s.logger.Debug("target health updated",
		zap.String("target_url", targetURL),
		zap.String("status", status),
	)

	return nil
}
