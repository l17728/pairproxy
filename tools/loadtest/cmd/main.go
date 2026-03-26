// Claude Load Tester
// 分布式并发压力测试工具，用于测试 PairProxy 网关和大模型后端的并发上限
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/l17728/pairproxy/tools/loadtest/internal/controller"
	"github.com/l17728/pairproxy/tools/loadtest/internal/metrics"
)

var (
	// 全局配置
	cfg = controller.DefaultConfig()

	// Logger
	logger *zap.Logger

	// 版本信息
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	var err error
	logger, err = initLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	rootCmd := &cobra.Command{
		Use:   "claude-load-tester",
		Short: "Distributed load testing tool for PairProxy and LLM backends",
		Long: `Claude Load Tester is a distributed load testing tool that simulates
multiple headless Claude CLI instances to test the concurrency limits of
PairProxy gateway and LLM backends.`,
		Version: fmt.Sprintf("%s (built %s)", Version, BuildTime),
	}

	// 添加子命令
	rootCmd.AddCommand(
		newRunCmd(),
		newAggregateCmd(),
	)

	// 全局标志
	rootCmd.PersistentFlags().StringVar(&cfg.ClaudePath, "claude-path", cfg.ClaudePath, "Path to Claude CLI executable")
	rootCmd.PersistentFlags().StringVar(&cfg.OutputPath, "output", "", "Output file path for test report (JSON)")
	rootCmd.PersistentFlags().StringVar(&cfg.PromptsPath, "prompts", "", "Path to prompts YAML file")

	if err := rootCmd.Execute(); err != nil {
		logger.Fatal("Command failed", zap.Error(err))
	}
}

// newRunCmd 创建 run 子命令
func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run load test",
		Long:  `Start a load test with specified configuration.`,
		RunE:  runTest,
	}

	// 模式选择
	cmd.Flags().StringVar(&cfg.Mode, "mode", cfg.Mode, "Test mode: ramp-up, fixed, spike")

	// Worker 配置
	cmd.Flags().IntVar(&cfg.InitialWorkers, "initial", cfg.InitialWorkers, "Initial number of workers (ramp-up mode)")
	cmd.Flags().IntVar(&cfg.MaxWorkers, "max", cfg.MaxWorkers, "Maximum number of workers")
	cmd.Flags().IntVar(&cfg.FixedWorkers, "workers", cfg.FixedWorkers, "Fixed number of workers (fixed mode)")

	// 阶梯递增配置
	cmd.Flags().IntVar(&cfg.StepSize, "step-size", cfg.StepSize, "Number of workers to add per step (ramp-up mode)")
	cmd.Flags().DurationVar(&cfg.StepDuration, "step-duration", cfg.StepDuration, "Duration to hold each step (ramp-up mode)")
	cmd.Flags().DurationVar(&cfg.RampUpInterval, "ramp-interval", cfg.RampUpInterval, "Interval between ramp-up steps")

	// 时间配置
	cmd.Flags().DurationVar(&cfg.Duration, "duration", cfg.Duration, "Total test duration (fixed/spike mode)")
	cmd.Flags().DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "Request timeout")

	// 思考时间配置
	cmd.Flags().DurationVar(&cfg.ThinkTimeMin, "think-min", cfg.ThinkTimeMin, "Minimum think time between requests")
	cmd.Flags().DurationVar(&cfg.ThinkTimeMax, "think-max", cfg.ThinkTimeMax, "Maximum think time between requests")

	// 报告配置
	cmd.Flags().DurationVar(&cfg.ReportInterval, "report-interval", cfg.ReportInterval, "Real-time report interval")

	// 熔断配置
	cmd.Flags().BoolVar(&cfg.CircuitBreakerEnabled, "circuit-breaker", cfg.CircuitBreakerEnabled, "Enable circuit breaker")
	cmd.Flags().Float64Var(&cfg.CircuitBreakerThreshold, "circuit-threshold", cfg.CircuitBreakerThreshold, "Circuit breaker error rate threshold")

	return cmd
}

// newAggregateCmd 创建 aggregate 子命令
func newAggregateCmd() *cobra.Command {
	var inputFiles []string
	var outputFile string

	cmd := &cobra.Command{
		Use:   "aggregate",
		Short: "Aggregate results from multiple test nodes",
		Long:  `Aggregate test results from multiple JSON report files.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(inputFiles) == 0 {
				return fmt.Errorf("no input files specified")
			}

			logger.Info("Aggregating reports", zap.Strings("files", inputFiles))

			// 加载所有报告
			agg, err := metrics.LoadFromFiles(inputFiles)
			if err != nil {
				return fmt.Errorf("load reports: %w", err)
			}

			// 生成聚合报告
			report := agg.Aggregate()

			// 打印报告
			report.PrintReport()

			// 保存到文件
			if outputFile != "" {
				if err := report.SaveToFile(outputFile); err != nil {
					return fmt.Errorf("save report: %w", err)
				}
				logger.Info("Aggregated report saved", zap.String("path", outputFile))
			}

			return nil
		},
	}

	cmd.Flags().StringSliceVar(&inputFiles, "inputs", nil, "Input JSON report files (comma-separated)")
	cmd.Flags().StringVar(&outputFile, "output", "", "Output file for aggregated report")
	cmd.MarkFlagRequired("inputs")

	return cmd
}

// runTest 运行测试
func runTest(cmd *cobra.Command, args []string) error {
	logger.Info("Starting load test",
		zap.String("mode", cfg.Mode),
		zap.String("claude_path", cfg.ClaudePath),
		zap.Int("max_workers", cfg.MaxWorkers),
	)

	// 验证 Claude CLI 存在
	if _, err := os.Stat(cfg.ClaudePath); err != nil {
		// 尝试从 PATH 中查找
		if path, err := findInPath("claude"); err == nil {
			cfg.ClaudePath = path
			logger.Info("Found Claude CLI in PATH", zap.String("path", path))
		} else {
			return fmt.Errorf("Claude CLI not found at %s and not in PATH", cfg.ClaudePath)
		}
	}

	// 创建控制器
	ctrl, err := controller.New(cfg, logger)
	if err != nil {
		return fmt.Errorf("create controller: %w", err)
	}

	// 设置信号处理
	ctrl.WaitForSignal()

	// 运行测试
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startTime := time.Now()

	if err := ctrl.Run(ctx); err != nil {
		return fmt.Errorf("run test: %w", err)
	}

	// 等待测试完成或中断
	waitForCompletion(ctrl, cancel)

	elapsed := time.Since(startTime)
	logger.Info("Test completed", zap.Duration("elapsed", elapsed))

	// 生成报告
	report, err := ctrl.GenerateReport()
	if err != nil {
		return fmt.Errorf("generate report: %w", err)
	}

	// 根据成功率返回退出码
	if report.SuccessRate < 95.0 {
		logger.Warn("Test had significant failures",
			zap.Float64("success_rate", report.SuccessRate),
		)
		os.Exit(1)
	}

	return nil
}

// waitForCompletion 等待测试完成
func waitForCompletion(ctrl *controller.Controller, cancel context.CancelFunc) {
	// 等待中断信号或手动停止
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 在另一个 goroutine 中等待信号
	go func() {
		sig := <-sigCh
		logger.Info("Received signal, stopping test", zap.String("signal", sig.String()))
		cancel()
		ctrl.Stop()
	}()

	// 阻塞直到停止
	// 实际停止由 controller 处理，这里只是等待
	select {}
}

// initLogger 初始化 Logger
func initLogger() (*zap.Logger, error) {
	config := zap.NewProductionConfig()
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	// 简化输出
	config.EncoderConfig.StacktraceKey = ""

	return config.Build()
}

// findInPath 在 PATH 中查找可执行文件
func findInPath(name string) (string, error) {
	// 简单的 PATH 查找
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		return "", fmt.Errorf("PATH not set")
	}

	// Windows 和 Unix 的处理
	var paths []string
	sep := ":"
	if os.PathSeparator == '\\' {
		sep = ";"
	}

	start := 0
	for i := 0; i < len(pathEnv); i++ {
		if pathEnv[i] == sep[0] {
			if i > start {
				paths = append(paths, pathEnv[start:i])
			}
			start = i + 1
		}
	}
	if start < len(pathEnv) {
		paths = append(paths, pathEnv[start:])
	}

	// 查找可执行文件
	for _, dir := range paths {
		fullPath := dir + string(os.PathSeparator) + name
		if os.PathSeparator == '\\' {
			fullPath += ".exe"
		}
		if _, err := os.Stat(fullPath); err == nil {
			return fullPath, nil
		}
	}

	return "", fmt.Errorf("%s not found in PATH", name)
}
