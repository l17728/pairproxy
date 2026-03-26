// Package controller 主控逻辑，协调所有组件
package controller

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/tools/loadtest/internal/metrics"
	"github.com/l17728/pairproxy/tools/loadtest/internal/prompts"
	"github.com/l17728/pairproxy/tools/loadtest/internal/worker"
)

// Config 控制器配置
type Config struct {
	// 基本配置
	ClaudePath  string // Claude CLI 路径
	PromptsPath string // Prompts 文件路径
	OutputPath  string // 输出报告路径

	// Worker 配置
	InitialWorkers int
	MaxWorkers     int
	StepSize       int
	StepDuration   time.Duration
	RampUpInterval time.Duration
	ThinkTimeMin   time.Duration
	ThinkTimeMax   time.Duration
	Timeout        time.Duration

	// 测试模式
	Mode string // "ramp-up", "fixed", "spike"

	// 固定模式配置
	FixedWorkers int
	Duration     time.Duration

	// 阶梯递增配置
	EnableRampUp bool

	// 实时报告
	ReportInterval time.Duration

	// 熔断配置
	CircuitBreakerEnabled   bool
	CircuitBreakerThreshold float64 // 错误率阈值
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		ClaudePath:              "claude",
		InitialWorkers:          1,
		MaxWorkers:              50,
		StepSize:                5,
		StepDuration:            60 * time.Second,
		RampUpInterval:          30 * time.Second,
		ThinkTimeMin:            10 * time.Second,
		ThinkTimeMax:            120 * time.Second,
		Timeout:                 120 * time.Second,
		Mode:                    "ramp-up",
		FixedWorkers:            10,
		Duration:                10 * time.Minute,
		EnableRampUp:            true,
		ReportInterval:          10 * time.Second,
		CircuitBreakerEnabled:   true,
		CircuitBreakerThreshold: 0.05, // 5% 错误率
	}
}

// Controller 测试控制器
type Controller struct {
	config *Config
	logger *zap.Logger

	// 组件
	promptLoader *prompts.Loader
	metrics      *metrics.Collector
	reporter     *metrics.Reporter
	pool         *worker.Pool

	// 通道
	promptsCh chan string
	resultsCh chan *metrics.Result

	// 状态
	startTime      time.Time
	stopCh         chan struct{}
	mu             sync.RWMutex
	currentWorkers int
	shouldStop     bool
}

// New 创建新的控制器
func New(cfg *Config, logger *zap.Logger) (*Controller, error) {
	// 加载 prompts
	var promptLoader *prompts.Loader
	var err error

	if cfg.PromptsPath != "" && fileExists(cfg.PromptsPath) {
		promptLoader, err = prompts.NewLoader(cfg.PromptsPath)
		if err != nil {
			return nil, fmt.Errorf("load prompts: %w", err)
		}
	} else {
		// 使用默认 prompts
		promptLoader = prompts.NewLoaderWithData(prompts.DefaultPrompts())
	}

	return &Controller{
		config:       cfg,
		logger:       logger,
		promptLoader: promptLoader,
		promptsCh:    make(chan string, cfg.MaxWorkers*2),
		resultsCh:    make(chan *metrics.Result, cfg.MaxWorkers*2),
		stopCh:       make(chan struct{}),
	}, nil
}

// Run 运行测试
func (c *Controller) Run(ctx context.Context) error {
	c.logger.Info("Starting load test",
		zap.String("mode", c.config.Mode),
		zap.Int("initial_workers", c.config.InitialWorkers),
		zap.Int("max_workers", c.config.MaxWorkers),
	)

	c.startTime = time.Now()

	// 创建 metrics collector
	c.metrics = metrics.NewCollector(c.logger)

	// 创建 worker pool
	workerCfg := worker.Config{
		ClaudePath:   c.config.ClaudePath,
		Timeout:      c.config.Timeout,
		ThinkTimeMin: c.config.ThinkTimeMin,
		ThinkTimeMax: c.config.ThinkTimeMax,
	}

	c.pool = worker.NewPool(c.config.InitialWorkers, workerCfg, c.promptsCh, c.resultsCh, c.logger)

	// 启动 worker pool
	poolCtx, poolCancel := context.WithCancel(ctx)
	defer poolCancel()
	c.pool.Start(poolCtx)
	c.currentWorkers = c.config.InitialWorkers

	// 启动 metrics reporter
	c.reporter = metrics.NewReporter(c.metrics, c.config.ReportInterval, c.logger)
	go c.reporter.Start(func() int { return c.currentWorkers })

	// 启动 result processor
	go c.processResults(ctx)

	// 启动 prompt feeder
	go c.feedPrompts(ctx)

	// 根据模式执行测试
	switch c.config.Mode {
	case "ramp-up":
		if err := c.runRampUp(ctx); err != nil {
			return err
		}
	case "fixed":
		if err := c.runFixed(ctx); err != nil {
			return err
		}
	case "spike":
		if err := c.runSpike(ctx); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown mode: %s", c.config.Mode)
	}

	return nil
}

// runRampUp 阶梯递增模式
func (c *Controller) runRampUp(ctx context.Context) error {
	c.logger.Info("Running ramp-up mode",
		zap.Int("step_size", c.config.StepSize),
		zap.Duration("step_duration", c.config.StepDuration),
	)

	// 阶梯递增
	for workers := c.config.InitialWorkers; workers <= c.config.MaxWorkers; workers += c.config.StepSize {
		if c.shouldStop {
			break
		}

		c.logger.Info("Scaling workers", zap.Int("new_count", workers))
		c.pool.Scale(ctx, workers, c.promptsCh, c.resultsCh, c.logger)
		c.currentWorkers = workers

		// 等待 StepDuration
		time.Sleep(c.config.StepDuration)
	}

	// 保持在最大并发直到手动停止或熔断
	c.logger.Info("Reached max workers, holding", zap.Int("workers", c.config.MaxWorkers))

	// 等待停止信号
	select {
	case <-ctx.Done():
	case <-c.stopCh:
	}

	return nil
}

// runFixed 固定并发模式
func (c *Controller) runFixed(ctx context.Context) error {
	c.logger.Info("Running fixed mode",
		zap.Int("workers", c.config.FixedWorkers),
		zap.Duration("duration", c.config.Duration),
	)

	// 调整到固定 worker 数
	if c.config.FixedWorkers != c.config.InitialWorkers {
		c.pool.Scale(ctx, c.config.FixedWorkers, c.promptsCh, c.resultsCh, c.logger)
		c.currentWorkers = c.config.FixedWorkers
	}

	// 运行指定时间
	timer := time.NewTimer(c.config.Duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		c.logger.Info("Fixed duration completed")
	case <-ctx.Done():
	case <-c.stopCh:
	}

	return nil
}

// runSpike 脉冲模式
func (c *Controller) runSpike(ctx context.Context) error {
	c.logger.Info("Running spike mode",
		zap.Int("workers", c.config.MaxWorkers),
	)

	// 瞬间增加到最大 worker 数
	c.pool.Scale(ctx, c.config.MaxWorkers, c.promptsCh, c.resultsCh, c.logger)
	c.currentWorkers = c.config.MaxWorkers
	c.logger.Info("Spike: scaled to max workers instantly")

	// 运行指定时间
	timer := time.NewTimer(c.config.Duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		c.logger.Info("Spike duration completed")
	case <-ctx.Done():
	case <-c.stopCh:
	}

	return nil
}

// processResults 处理结果
func (c *Controller) processResults(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case result := <-c.resultsCh:
			if result == nil {
				continue
			}

			// 记录 metrics
			c.metrics.Record(&metrics.Result{
				Timestamp:  time.Now(),
				WorkerID:   result.WorkerID,
				Duration:   result.Duration,
				Success:    result.Success,
				Error:      result.Error,
				OutputSize: result.OutputSize,
			})

			// 检查熔断
			if c.config.CircuitBreakerEnabled {
				c.checkCircuitBreaker()
			}
		}
	}
}

// feedPrompts 持续提供 prompts
func (c *Controller) feedPrompts(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		default:
		}

		// 随机获取 prompt
		prompt := c.promptLoader.GetRandom()

		// 发送到 channel（非阻塞）
		select {
		case c.promptsCh <- prompt:
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		}
	}
}

// checkCircuitBreaker 检查熔断条件
func (c *Controller) checkCircuitBreaker() {
	snapshot := c.metrics.GetSnapshot(c.currentWorkers)

	if snapshot.TotalRequests == 0 {
		return
	}

	errorRate := 1.0 - (snapshot.SuccessRate / 100.0)
	if errorRate >= c.config.CircuitBreakerThreshold {
		c.logger.Error("Circuit breaker triggered",
			zap.Float64("error_rate", errorRate),
			zap.Float64("threshold", c.config.CircuitBreakerThreshold),
		)
		c.Stop()
	}
}

// Stop 停止测试
func (c *Controller) Stop() {
	c.mu.Lock()
	c.shouldStop = true
	c.mu.Unlock()

	close(c.stopCh)

	if c.reporter != nil {
		c.reporter.Stop()
	}

	if c.pool != nil {
		c.pool.Stop()
	}
}

// WaitForSignal 等待中断信号
func (c *Controller) WaitForSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		c.logger.Info("Received signal, stopping test", zap.String("signal", sig.String()))
		c.Stop()
	}()
}

// GenerateReport 生成最终报告
func (c *Controller) GenerateReport() (*metrics.Report, error) {
	if c.metrics == nil {
		return nil, fmt.Errorf("test not started")
	}

	report := c.metrics.GenerateFinalReport(c.currentWorkers)

	// 打印到控制台
	report.PrintReport()

	// 保存到文件
	if c.config.OutputPath != "" {
		if err := report.SaveToFile(c.config.OutputPath); err != nil {
			return report, fmt.Errorf("save report: %w", err)
		}
		c.logger.Info("Report saved", zap.String("path", c.config.OutputPath))
	}

	return report, nil
}

// fileExists 检查文件是否存在
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}
