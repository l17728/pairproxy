package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/term"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/l17728/pairproxy/internal/preflight"
	"github.com/l17728/pairproxy/internal/proxy"
	"github.com/l17728/pairproxy/internal/version"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:     "cproxy",
	Short:   "PairProxy client-side proxy for Claude Code",
	Long:    "cproxy intercepts Claude Code requests, injects user JWT, and forwards to s-proxy.",
	Version: version.Short(),
}

func init() {
	rootCmd.AddCommand(loginCmd, startCmd, statusCmd, logoutCmd, versionCmd, configCmd)
}

// ---------------------------------------------------------------------------
// cproxy version
// ---------------------------------------------------------------------------

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.Full("cproxy"))
	},
}

// ---------------------------------------------------------------------------
// cproxy login
// ---------------------------------------------------------------------------

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with s-proxy and save token locally",
	RunE:  runLogin,
}

var loginServerFlag string

func init() {
	loginCmd.Flags().StringVar(&loginServerFlag, "server", "", "s-proxy server URL (e.g. http://proxy.company.com:9000)")
	_ = loginCmd.MarkFlagRequired("server")
}

func runLogin(cmd *cobra.Command, args []string) error {
	logger, _ := zap.NewProduction()
	defer logger.Sync() //nolint:errcheck

	// 提示输入用户名和密码
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Username: ")
	username, _ := reader.ReadString('\n')
	username = strings.TrimSpace(username)

	fmt.Print("Password: ")
	var password string
	// 尝试无回显输入
	if term.IsTerminal(int(syscall.Stdin)) {
		pw, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		password = string(pw)
	} else {
		pw, _ := reader.ReadString('\n')
		password = strings.TrimSpace(pw)
	}

	if username == "" || password == "" {
		return fmt.Errorf("username and password are required")
	}

	// POST /auth/login
	loginURL := strings.TrimRight(loginServerFlag, "/") + "/auth/login"
	body, _ := json.Marshal(map[string]string{"username": username, "password": password})

	resp, err := http.Post(loginURL, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("login failed (HTTP %d): %s", resp.StatusCode, errResp["message"])
	}

	var loginResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Username     string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}

	// 保存 token 到本地
	store := auth.NewTokenStore(logger, 30*time.Minute)
	tokenDir := auth.DefaultTokenDir()
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	tf := &auth.TokenFile{
		AccessToken:  loginResp.AccessToken,
		RefreshToken: loginResp.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(loginResp.ExpiresIn) * time.Second),
		ServerAddr:   loginServerFlag,
		Username:     loginResp.Username,
	}
	if err := store.Save(tokenDir, tf); err != nil {
		return fmt.Errorf("save token: %w", err)
	}

	fmt.Printf("✓ Login successful. Token saved to %s\n", tokenDir)
	fmt.Printf("  Token expires at: %s\n", tf.ExpiresAt.Format(time.RFC3339))
	return nil
}

// ---------------------------------------------------------------------------
// cproxy start
// ---------------------------------------------------------------------------

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the c-proxy listener",
	RunE:  runStart,
}

var startConfigFlag string

var startDaemonFlag bool

func init() {
	startCmd.Flags().StringVar(&startConfigFlag, "config", "", "path to cproxy.yaml (default: ~/.config/pairproxy/cproxy.yaml)")
	startCmd.Flags().BoolVar(&startDaemonFlag, "daemon", false, "run in background, detached from the terminal (Linux/macOS only; use 'cproxy install-service' on Windows)")
}

func runStart(cmd *cobra.Command, args []string) error {
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	// 加载配置
	cfgPath := startConfigFlag
	if cfgPath == "" {
		cfgPath = config.DefaultConfigDir() + "/cproxy.yaml"
	}

	cfg, warnings, err := config.LoadCProxyConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load config from %q: %w", cfgPath, err)
	}
	for _, w := range warnings {
		logger.Warn("config warning", zap.String("detail", w))
	}

	// 设置日志级别
	if cfg.Log.Level == "debug" {
		logger, _ = zap.NewDevelopment()
	}

	logger.Info("cproxy starting",
		zap.String("listen", cfg.Listen.Addr()),
		zap.String("sproxy_primary", cfg.SProxy.Primary),
		zap.Strings("sproxy_targets", cfg.SProxy.Targets),
	)

	// Preflight 检查：监听端口未被占用
	if err := preflight.CheckCProxy(logger, cfg); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	// 路由表缓存目录：与 token 文件放在同一目录；必须在构建 balancer 之前设置，
	// 因为 buildInitialTargets 会读取磁盘缓存。
	tokenDir := auth.DefaultTokenDir()
	cacheDir := tokenDir
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		logger.Warn("failed to create cache dir, routing cache will be skipped",
			zap.String("dir", cacheDir), zap.Error(err))
		cacheDir = ""
	}

	// 构建 s-proxy 初始目标列表：合并三个来源
	//   1. sproxy.primary      — 配置文件种子节点（优先）
	//   2. sproxy.targets      — 配置文件静态 worker 列表（优先）
	//   3. routing-cache.json  — 上次运行持久化的路由表（兜底）
	// 主节点故障时 c-proxy 仍可将流量路由到已知 worker，避免 502。
	initialTargets, err := buildInitialTargets(&cfg.SProxy, cacheDir, logger)
	if err != nil {
		return err
	}
	balancer := lb.NewWeightedRandom(initialTargets)

	// 健康检查器（主动检查 s-proxy 节点健康状态）
	hc := lb.NewHealthChecker(balancer, logger,
		lb.WithInterval(cfg.SProxy.HealthCheckInterval),
		lb.WithTimeout(cfg.SProxy.HealthCheckTimeout),
		lb.WithFailThreshold(cfg.SProxy.HealthCheckFailureThreshold),
		lb.WithRecoveryDelay(cfg.SProxy.HealthCheckRecoveryDelay),
	)

	// Token store
	tokenStore := auth.NewTokenStore(logger, cfg.Auth.RefreshThreshold)

	// 检查 token 有效性
	tf, err := tokenStore.Load(tokenDir)
	if err != nil {
		logger.Warn("failed to load token, requests will be rejected until login", zap.Error(err))
	} else if tf != nil && tokenStore.IsValid(tf) {
		logger.Info("token loaded",
			zap.String("server_addr", tf.ServerAddr),
			zap.Time("expires_at", tf.ExpiresAt),
			zap.Duration("remaining", time.Until(tf.ExpiresAt)),
		)
	} else {
		logger.Warn("no valid token found; run 'cproxy login' first")
	}

	// 构建 c-proxy handler
	cp, err := proxy.NewCProxy(logger, tokenStore, tokenDir, balancer, cacheDir)
	if err != nil {
		return fmt.Errorf("create cproxy: %w", err)
	}

	// 改进项3：注入健康检查器（被动熔断上报）
	cp.SetHealthChecker(hc)

	// 改进项5：注入请求级重试配置
	cp.SetRetryConfig(cfg.SProxy.Retry)

	// 改进项4：注入路由表主动发现配置
	cp.SetRoutingPoller(cfg.SProxy.SharedSecret, cfg.SProxy.RoutingPollInterval)

	// 可选：debug 文件日志（记录双向转发内容）
	if cfg.Log.DebugFile != "" {
		debugLogger, dbgErr := buildDebugFileLogger(cfg.Log.DebugFile)
		if dbgErr != nil {
			logger.Warn("failed to init debug file logger",
				zap.String("path", cfg.Log.DebugFile), zap.Error(dbgErr))
		} else {
			cp.SetDebugLogger(debugLogger)
			logger.Info("debug file logging enabled", zap.String("path", cfg.Log.DebugFile))
		}
	}

	// 启动健康检查（在独立 goroutine 中运行）
	hcCtx, hcCancel := context.WithCancel(context.Background())
	defer hcCancel()
	hc.Start(hcCtx)

	// 改进项4：启动路由表主动发现 goroutine
	cp.StartRoutingPoller(hcCtx)

	mux := http.NewServeMux()
	mux.Handle("/", cp.Handler())

	addr := cfg.Listen.Addr()
	logger.Info("cproxy listening", zap.String("addr", addr))

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Windows service mode: lifecycle managed by the Service Control Manager.
	if isWindowsService() {
		logger.Info("detected Windows service mode, handing off to SCM")
		return runAsWindowsService(server, logger)
	}

	// Daemon mode: re-exec detached from the terminal (--daemon flag).
	// On Windows, daemonize() returns an error directing the user to install-service.
	if startDaemonFlag {
		logger.Info("daemon flag set, daemonizing process", zap.String("config", startConfigFlag))
		return daemonize(startConfigFlag, logger)
	}

	// Foreground mode.
	fmt.Printf("cproxy listening on http://%s\n", addr)
	fmt.Println("Press Ctrl+C to stop.")

	// 启动 HTTP 服务（阻塞）
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// buildInitialTargets
// ---------------------------------------------------------------------------

// buildInitialTargets merges three independent sources of s-proxy knowledge
// into the starting target list for the load balancer:
//
//  1. cfg.Primary  — the seed node declared in sproxy.primary
//  2. cfg.Targets  — additional static worker addresses in sproxy.targets
//  3. routing-cache.json — the routing table persisted by the previous run
//
// Config entries are considered authoritative (always included and marked
// Healthy:true). Disk-cache entries fill in addresses not already in config;
// their Healthy flag is preserved as-is (they were persisted by the previous
// run's health-checker view).
//
// Deduplication is by Addr string.  ID is set to Addr for config-originated
// entries (matching the convention used elsewhere in cproxy).
//
// Returns an error only when no targets can be assembled from any source.
func buildInitialTargets(cfg *config.SProxySect, cacheDir string, logger *zap.Logger) ([]lb.Target, error) {
	logger.Debug("buildInitialTargets: starting target assembly",
		zap.String("primary", cfg.Primary),
		zap.Strings("static_targets", cfg.Targets),
		zap.String("cache_dir", cacheDir),
	)

	seen := make(map[string]bool)
	var targets []lb.Target

	// Source 1: primary from config.
	if cfg.Primary != "" {
		targets = append(targets, lb.Target{
			ID:      cfg.Primary,
			Addr:    cfg.Primary,
			Weight:  1,
			Healthy: true,
		})
		seen[cfg.Primary] = true
		logger.Info("buildInitialTargets: added primary from config",
			zap.String("addr", cfg.Primary))
	} else {
		logger.Debug("buildInitialTargets: no primary configured, skipping source 1")
	}

	// Source 2: static targets from config.
	for _, addr := range cfg.Targets {
		if addr == "" {
			logger.Warn("buildInitialTargets: empty string in sproxy.targets, skipping")
			continue
		}
		if seen[addr] {
			logger.Debug("buildInitialTargets: static target already present (duplicate of primary), skipping",
				zap.String("addr", addr))
			continue
		}
		targets = append(targets, lb.Target{
			ID:      addr,
			Addr:    addr,
			Weight:  1,
			Healthy: true,
		})
		seen[addr] = true
		logger.Info("buildInitialTargets: added static target from config",
			zap.String("addr", addr))
	}

	configCount := len(targets)
	logger.Debug("buildInitialTargets: config sources yielded targets",
		zap.Int("count", configCount))

	// Source 3: routing cache from disk (fills in addresses not already known).
	if cacheDir == "" {
		logger.Debug("buildInitialTargets: cacheDir is empty, skipping disk cache source")
	} else {
		cached, loadErr := cluster.LoadFromDir(cacheDir)
		if loadErr != nil {
			logger.Warn("buildInitialTargets: failed to load routing cache (ignored)",
				zap.String("cache_dir", cacheDir), zap.Error(loadErr))
		} else if cached == nil {
			logger.Debug("buildInitialTargets: no routing cache file found",
				zap.String("cache_dir", cacheDir))
		} else {
			logger.Debug("buildInitialTargets: routing cache found",
				zap.Int64("cache_version", cached.Version),
				zap.Int("cache_entries", len(cached.Entries)))
			for _, e := range cached.Entries {
				if e.Addr == "" {
					logger.Warn("buildInitialTargets: routing cache entry with empty addr, skipping",
						zap.String("id", e.ID))
					continue
				}
				if seen[e.Addr] {
					logger.Debug("buildInitialTargets: cache entry already covered by config, skipping",
						zap.String("addr", e.Addr))
					continue
				}
				targets = append(targets, lb.Target{
					ID:      e.ID,
					Addr:    e.Addr,
					Weight:  e.Weight,
					Healthy: e.Healthy,
				})
				seen[e.Addr] = true
				logger.Info("buildInitialTargets: added target from routing cache",
					zap.String("addr", e.Addr),
					zap.Bool("healthy", e.Healthy),
					zap.Int64("cache_version", cached.Version))
			}
			if fromCache := len(targets) - configCount; fromCache > 0 {
				logger.Info("buildInitialTargets: supplemented targets from routing cache",
					zap.Int("from_cache", fromCache),
					zap.Int64("cache_version", cached.Version))
			} else {
				logger.Debug("buildInitialTargets: all cache entries already covered by config")
			}
		}
	}

	if len(targets) == 0 {
		logger.Error("buildInitialTargets: no s-proxy targets found in any source; " +
			"set sproxy.primary or sproxy.targets in cproxy.yaml")
		return nil, fmt.Errorf(
			"no s-proxy targets configured; set sproxy.primary or sproxy.targets in cproxy.yaml")
	}

	logger.Info("buildInitialTargets: target assembly complete",
		zap.Int("total", len(targets)),
		zap.Int("from_config", configCount),
		zap.Int("from_cache", len(targets)-configCount))
	return targets, nil
}

// ---------------------------------------------------------------------------
// cproxy status
// ---------------------------------------------------------------------------

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current token status and configuration",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	logger, _ := zap.NewProduction()
	store := auth.NewTokenStore(logger, 30*time.Minute)
	tokenDir := auth.DefaultTokenDir()

	tf, err := store.Load(tokenDir)
	if err != nil {
		return fmt.Errorf("load token: %w", err)
	}
	if tf == nil {
		fmt.Println("Status: Not authenticated (run 'cproxy login')")
		return nil
	}

	remaining := time.Until(tf.ExpiresAt)
	status := "valid"
	if !store.IsValid(tf) {
		if remaining < 0 {
			status = "expired"
		} else {
			status = "near expiry (needs refresh)"
		}
	}

	fmt.Printf("Status:  %s\n", status)
	if tf.Username != "" {
		fmt.Printf("User:    %s\n", tf.Username)
	}
	fmt.Printf("Server:  %s\n", tf.ServerAddr)
	fmt.Printf("Expires: %s\n", tf.ExpiresAt.Format(time.RFC3339))

	// Token TTL progress bar (assumes 24h standard access token lifetime)
	const totalTTL = 24 * time.Hour
	pct := 0
	if remaining > 0 {
		pct = int(remaining * 100 / totalTTL)
		if pct > 100 {
			pct = 100
		}
	}
	bar := renderProgressBar(pct, 20)
	fmt.Printf("TTL:     %s %d%% (%s remaining)\n", bar, pct, remaining.Truncate(time.Second))

	fmt.Printf("Version: %s\n", version.Short())
	return nil
}

// renderProgressBar renders a Unicode block progress bar.
// pct is clamped to [0, 100]; width is the bar character count.
func renderProgressBar(pct, width int) string {
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	filled := pct * width / 100
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}

// ---------------------------------------------------------------------------
// cproxy logout
// ---------------------------------------------------------------------------

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Delete local token and notify s-proxy",
	RunE:  runLogout,
}

func runLogout(cmd *cobra.Command, args []string) error {
	logger, _ := zap.NewProduction()
	store := auth.NewTokenStore(logger, 30*time.Minute)
	tokenDir := auth.DefaultTokenDir()

	tf, err := store.Load(tokenDir)
	if err != nil {
		return fmt.Errorf("load token: %w", err)
	}

	// 通知 s-proxy 撤销 refresh token
	if tf != nil && tf.ServerAddr != "" {
		logoutURL := strings.TrimRight(tf.ServerAddr, "/") + "/auth/logout"
		body, _ := json.Marshal(map[string]string{"refresh_token": tf.RefreshToken})
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, logoutURL, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tf.AccessToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logger.Warn("failed to notify s-proxy of logout", zap.Error(err))
		} else {
			resp.Body.Close()
			logger.Info("s-proxy notified of logout", zap.Int("status", resp.StatusCode))
		}
	}

	if err := store.Delete(tokenDir); err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	fmt.Println("✓ Logged out successfully")
	return nil
}

// ---------------------------------------------------------------------------
// cproxy config validate
// ---------------------------------------------------------------------------

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Configuration management commands",
}

var configValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate cproxy configuration file and report issues",
	RunE:  runConfigValidate,
}

var configValidateConfigFlag string

func init() {
	configCmd.AddCommand(configValidateCmd)
	configValidateCmd.Flags().StringVar(&configValidateConfigFlag, "config", "",
		"path to cproxy.yaml (default: "+config.DefaultCProxyConfigPath()+")")
}

// buildDebugFileLogger 构建写入独立文件的 debug 日志器，
// 用于记录 c-proxy 的双向转发内容（请求体、响应体、streaming chunks）。
func buildDebugFileLogger(path string) (*zap.Logger, error) {
	// zap 内置 sink 无需检查目录
	if path != "stderr" && path != "stdout" {
		dir := filepath.Dir(path)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return nil, fmt.Errorf("directory %q does not exist; please create it manually before starting (mkdir -p %s)", dir, dir)
		}
	}
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	cfg.OutputPaths = []string{path}
	cfg.ErrorOutputPaths = []string{path}
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	return cfg.Build()
}

func runConfigValidate(cmd *cobra.Command, args []string) error {
	cfgPath := configValidateConfigFlag
	if cfgPath == "" {
		cfgPath = config.DefaultCProxyConfigPath()
	}

	fmt.Printf("Config file: %s\n\n", cfgPath)

	// Parse without validation so we can always show the effective config.
	cfg, warnings, parseErr := config.ParseCProxyConfig(cfgPath)
	if parseErr != nil {
		fmt.Printf("✗ Load error: %v\n", parseErr)
		os.Exit(1)
	}

	// Show missing env-var warnings.
	if len(warnings) > 0 {
		fmt.Println("Warnings:")
		for _, w := range warnings {
			fmt.Printf("  ⚠  %s\n", w)
		}
		fmt.Println()
	}

	// Show effective configuration (after defaults are applied).
	fmt.Println("Effective configuration:")
	fmt.Printf("  Listen:             %s\n", cfg.Listen.Addr())
	sproxySummary := cfg.SProxy.Primary
	if sproxySummary == "" && len(cfg.SProxy.Targets) > 0 {
		sproxySummary = strings.Join(cfg.SProxy.Targets, ", ")
	}
	if sproxySummary == "" {
		sproxySummary = "(not set — will rely on routing cache)"
	}
	fmt.Printf("  S-Proxy primary:    %s\n", sproxySummary)
	if len(cfg.SProxy.Targets) > 0 {
		fmt.Printf("  S-Proxy targets:    %s\n", strings.Join(cfg.SProxy.Targets, ", "))
	}
	fmt.Printf("  Health check:       every %s\n", cfg.SProxy.HealthCheckInterval)
	fmt.Printf("  Request timeout:    %s\n", cfg.SProxy.RequestTimeout)
	fmt.Printf("  Auto-refresh:       %v\n", cfg.Auth.AutoRefresh)
	fmt.Printf("  Refresh threshold:  %s\n", cfg.Auth.RefreshThreshold)
	fmt.Printf("  Log level:          %s\n", cfg.Log.Level)
	fmt.Println()

	// Run validation and report result.
	if err := cfg.Validate(); err != nil {
		fmt.Printf("Validation: ✗ FAILED\n%v\n", err)
		return fmt.Errorf("configuration is invalid")
	}

	fmt.Println("Validation: ✓ All checks passed")
	return nil
}
