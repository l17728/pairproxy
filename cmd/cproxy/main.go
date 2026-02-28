package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/term"

	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/lb"
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
	rootCmd.AddCommand(loginCmd, startCmd, statusCmd, logoutCmd, versionCmd)
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
	)

	// 构建 s-proxy 负载均衡器（初始配置仅 Primary 节点）
	if cfg.SProxy.Primary == "" {
		return fmt.Errorf("sproxy.primary is required in config")
	}
	initialTarget := lb.Target{
		ID:      cfg.SProxy.Primary,
		Addr:    cfg.SProxy.Primary,
		Weight:  1,
		Healthy: true,
	}
	balancer := lb.NewWeightedRandom([]lb.Target{initialTarget})
	logger.Info("s-proxy primary target configured", zap.String("url", cfg.SProxy.Primary))

	// 健康检查器（主动检查 s-proxy 节点健康状态）
	hc := lb.NewHealthChecker(balancer, logger,
		lb.WithInterval(cfg.SProxy.HealthCheckInterval),
	)

	// Token store
	tokenStore := auth.NewTokenStore(logger, cfg.Auth.RefreshThreshold)
	tokenDir := auth.DefaultTokenDir()

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

	// 路由表缓存目录（用于 c-proxy 重启后恢复路由信息）
	cacheDir := tokenDir // 与 token 文件放在同一目录
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		logger.Warn("failed to create cache dir", zap.String("dir", cacheDir), zap.Error(err))
		cacheDir = ""
	}

	// 尝试从缓存加载路由表（重启恢复）
	if cached, loadErr := cluster.LoadFromDir(cacheDir); loadErr == nil && cached != nil {
		targets := make([]lb.Target, 0, len(cached.Entries))
		for _, e := range cached.Entries {
			targets = append(targets, lb.Target{
				ID:      e.ID,
				Addr:    e.Addr,
				Weight:  e.Weight,
				Healthy: e.Healthy,
			})
		}
		if len(targets) > 0 {
			balancer.UpdateTargets(targets)
			logger.Info("routing table loaded from cache",
				zap.Int64("version", cached.Version),
				zap.Int("entries", len(targets)),
			)
		}
	}

	// 构建 c-proxy handler
	cp, err := proxy.NewCProxy(logger, tokenStore, tokenDir, balancer, cacheDir)
	if err != nil {
		return fmt.Errorf("create cproxy: %w", err)
	}

	// 启动健康检查（在独立 goroutine 中运行）
	hcCtx, hcCancel := context.WithCancel(context.Background())
	defer hcCancel()
	hc.Start(hcCtx)

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
		return runAsWindowsService(server, logger)
	}

	// Daemon mode: re-exec detached from the terminal (--daemon flag).
	// On Windows, daemonize() returns an error directing the user to install-service.
	if startDaemonFlag {
		return daemonize(startConfigFlag)
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
	fmt.Printf("Expires: %s (%s remaining)\n", tf.ExpiresAt.Format(time.RFC3339), remaining.Truncate(time.Second))
	fmt.Printf("Version: %s\n", version.Short())
	return nil
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
