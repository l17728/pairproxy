// Package worker 管理 Claude CLI worker
package worker

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/tools/loadtest/internal/metrics"
)

// Result 使用 metrics.Result

// Config worker 配置
type Config struct {
	WorkerID     int
	ClaudePath   string // Claude CLI 路径
	Timeout      time.Duration
	ThinkTimeMin time.Duration // 最小思考时间
	ThinkTimeMax time.Duration // 最大思考时间
	MaxRetries   int           // 最大重试次数
}

// Worker 代表一个 Claude CLI worker
type Worker struct {
	id      int
	config  Config
	prompts chan string
	results chan *metrics.Result
	stopCh  chan struct{}
	logger  *zap.Logger

	// 统计
	requestCount atomic.Int64
	successCount atomic.Int64
	failureCount atomic.Int64

	// 当前状态
	currentPrompt atomic.Value // string
	isRunning     atomic.Bool
}

// New 创建新的 worker
func New(cfg Config, prompts chan string, results chan *metrics.Result, logger *zap.Logger) *Worker {
	return &Worker{
		id:      cfg.WorkerID,
		config:  cfg,
		prompts: prompts,
		results: results,
		stopCh:  make(chan struct{}),
		logger:  logger.With(zap.Int("worker_id", cfg.WorkerID)),
	}
}

// Start 启动 worker
func (w *Worker) Start(ctx context.Context) {
	w.isRunning.Store(true)
	w.logger.Info("Worker started")

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("Worker stopped by context")
			w.isRunning.Store(false)
			return
		case <-w.stopCh:
			w.logger.Info("Worker stopped by signal")
			w.isRunning.Store(false)
			return
		case prompt, ok := <-w.prompts:
			if !ok {
				w.logger.Info("Prompt channel closed, worker exiting")
				w.isRunning.Store(false)
				return
			}
			w.processPrompt(ctx, prompt)
		}
	}
}

// Stop 停止 worker
func (w *Worker) Stop() {
	close(w.stopCh)
}

// IsRunning 返回 worker 是否正在运行
func (w *Worker) IsRunning() bool {
	return w.isRunning.Load()
}

// GetStats 返回 worker 统计
func (w *Worker) GetStats() (total, success, failure int64) {
	return w.requestCount.Load(), w.successCount.Load(), w.failureCount.Load()
}

// processPrompt 处理单个 prompt
func (w *Worker) processPrompt(ctx context.Context, prompt string) {
	w.currentPrompt.Store(prompt)
	w.requestCount.Add(1)

	result := &metrics.Result{
		WorkerID:  w.id,
		Prompt:    prompt,
		StartTime: time.Now(),
	}

	// 执行 Claude CLI
	output, err := w.executeClaude(ctx, prompt)

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	if err != nil {
		result.Success = false
		result.Error = err.Error()
		w.failureCount.Add(1)
		w.logger.Warn("Request failed",
			zap.String("prompt", truncate(prompt, 50)),
			zap.Duration("duration", result.Duration),
			zap.Error(err),
		)
	} else {
		result.Success = true
		result.OutputSize = len(output)
		w.successCount.Add(1)
		w.logger.Debug("Request succeeded",
			zap.String("prompt", truncate(prompt, 50)),
			zap.Duration("duration", result.Duration),
			zap.Int("output_size", result.OutputSize),
		)
	}

	// 发送结果
	select {
	case w.results <- result:
	case <-ctx.Done():
	}

	// 模拟思考时间
	w.simulateThinkTime()
}

// executeClaude 执行 Claude CLI
func (w *Worker) executeClaude(ctx context.Context, prompt string) (string, error) {
	// 构建命令
	// claude --no-interactive --prompt "xxx" --output-format json
	args := []string{
		"--no-interactive",
		"--prompt", prompt,
		"--output-format", "json",
	}

	// 创建带超时的 context
	execCtx, cancel := context.WithTimeout(ctx, w.config.Timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, w.config.ClaudePath, args...)

	// 捕获输出
	var output strings.Builder
	var stderr strings.Builder

	cmd.Stdout = &output
	cmd.Stderr = &stderr

	// 执行
	err := cmd.Run()
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude timeout: %w", err)
		}
		return "", fmt.Errorf("claude failed: %w, stderr: %s", err, stderr.String())
	}

	return output.String(), nil
}

// simulateThinkTime 模拟程序员思考时间
func (w *Worker) simulateThinkTime() {
	if w.config.ThinkTimeMin <= 0 && w.config.ThinkTimeMax <= 0 {
		return
	}

	// 正态分布，均值 (min+max)/2，标准差 (max-min)/6
	mean := float64(w.config.ThinkTimeMin+w.config.ThinkTimeMax) / 2
	stdDev := float64(w.config.ThinkTimeMax-w.config.ThinkTimeMin) / 6

	// 生成随机思考时间
	thinkTime := time.Duration(mean + stdDev*randNorm())

	// 限制在范围内
	if thinkTime < w.config.ThinkTimeMin {
		thinkTime = w.config.ThinkTimeMin
	}
	if thinkTime > w.config.ThinkTimeMax {
		thinkTime = w.config.ThinkTimeMax
	}

	time.Sleep(thinkTime)
}

// randNorm 返回标准正态分布的随机数 (Box-Muller 变换)
func randNorm() float64 {
	// 简化实现：使用均匀分布近似
	// 12 个均匀分布的均值接近正态分布
	sum := 0.0
	for i := 0; i < 12; i++ {
		sum += float64(time.Now().UnixNano()%1000) / 1000.0
	}
	return sum - 6.0
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Pool worker 池
type Pool struct {
	workers []*Worker
	config  Config
	mu      sync.RWMutex
}

// NewPool 创建 worker 池
func NewPool(size int, baseConfig Config, prompts chan string, results chan *metrics.Result, logger *zap.Logger) *Pool {
	workers := make([]*Worker, size)
	for i := 0; i < size; i++ {
		cfg := baseConfig
		cfg.WorkerID = i + 1
		workers[i] = New(cfg, prompts, results, logger)
	}

	return &Pool{
		workers: workers,
		config:  baseConfig,
	}
}

// Start 启动所有 worker
func (p *Pool) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, w := range p.workers {
		go w.Start(ctx)
	}
}

// Stop 停止所有 worker
func (p *Pool) Stop() {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, w := range p.workers {
		w.Stop()
	}
}

// Scale 动态调整 worker 数量（用于阶梯递增）
func (p *Pool) Scale(ctx context.Context, newSize int, prompts chan string, results chan *metrics.Result, logger *zap.Logger) {
	p.mu.Lock()
	defer p.mu.Unlock()

	currentSize := len(p.workers)

	if newSize > currentSize {
		// 增加 worker
		for i := currentSize; i < newSize; i++ {
			cfg := p.config
			cfg.WorkerID = i + 1
			w := New(cfg, prompts, results, logger)
			p.workers = append(p.workers, w)
			go w.Start(ctx)
			logger.Info("Worker added", zap.Int("worker_id", cfg.WorkerID))
		}
	} else if newSize < currentSize {
		// 减少 worker
		for i := newSize; i < currentSize; i++ {
			p.workers[i].Stop()
		}
		p.workers = p.workers[:newSize]
		logger.Info("Workers removed", zap.Int("new_pool_size", newSize))
	}
}

// GetStats 获取所有 worker 的统计
func (p *Pool) GetStats() (totalWorkers int, totalReqs, successReqs, failureReqs int64) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	totalWorkers = len(p.workers)
	for _, w := range p.workers {
		t, s, f := w.GetStats()
		totalReqs += t
		successReqs += s
		failureReqs += f
	}

	return totalWorkers, totalReqs, successReqs, failureReqs
}
