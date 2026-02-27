package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/term"

	"github.com/pairproxy/pairproxy/internal/alert"
	"github.com/pairproxy/pairproxy/internal/api"
	"github.com/pairproxy/pairproxy/internal/auth"
	"github.com/pairproxy/pairproxy/internal/cluster"
	"github.com/pairproxy/pairproxy/internal/config"
	"github.com/pairproxy/pairproxy/internal/dashboard"
	"github.com/pairproxy/pairproxy/internal/db"
	"github.com/pairproxy/pairproxy/internal/lb"
	"github.com/pairproxy/pairproxy/internal/metrics"
	"github.com/pairproxy/pairproxy/internal/proxy"
	"github.com/pairproxy/pairproxy/internal/quota"
	"github.com/pairproxy/pairproxy/internal/version"
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:     "sproxy",
	Short:   "PairProxy server-side proxy",
	Long:    "sproxy validates user JWTs, forwards requests to LLM APIs, and tracks token usage.",
	Version: version.Short(),
}

func init() {
	rootCmd.AddCommand(startCmd, adminCmd, hashPasswordCmd, spVersionCmd)
}

// ---------------------------------------------------------------------------
// sproxy version
// ---------------------------------------------------------------------------

var spVersionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version.Full("sproxy"))
	},
}

// ---------------------------------------------------------------------------
// sproxy start
// ---------------------------------------------------------------------------

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the s-proxy server",
	RunE:  runStart,
}

var startConfigFlag string

func init() {
	startCmd.Flags().StringVar(&startConfigFlag, "config", "", "path to sproxy.yaml")
}

func runStart(cmd *cobra.Command, args []string) error {
	// 初始化日志（先用 production logger，配置加载后再根据 log.level 重建）
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	// 加载配置
	cfgPath := startConfigFlag
	if cfgPath == "" {
		cfgPath = "sproxy.yaml"
	}
	cfg, warnings, err := config.LoadSProxyConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load config from %q: %w", cfgPath, err)
	}
	for _, w := range warnings {
		logger.Warn("config warning", zap.String("detail", w))
	}

	if cfg.Log.Level == "debug" {
		logger, _ = zap.NewDevelopment()
	}

	role := "primary"
	if cfg.Cluster.Role != "" {
		role = cfg.Cluster.Role
	}
	logger.Info("sproxy starting",
		zap.String("role", role),
		zap.String("listen", cfg.Listen.Addr()),
		zap.Int("llm_targets", len(cfg.LLM.Targets)),
	)

	// 打开数据库
	database, err := db.Open(logger, cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	// 初始化后台用量写入器
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	writer := db.NewUsageWriter(database, logger,
		cfg.Database.WriteBufferSize,
		cfg.Database.FlushInterval,
	)
	writer.Start(ctx)

	// 初始化 JWT Manager
	jwtMgr, err := auth.NewManager(logger, cfg.Auth.JWTSecret)
	if err != nil {
		return fmt.Errorf("init JWT manager: %w", err)
	}
	// 启动 JTI 黑名单定期清理
	jwtMgr.StartCleanup(ctx)

	// 构建 LLM 目标列表
	llmTargets := make([]proxy.LLMTarget, 0, len(cfg.LLM.Targets))
	for _, t := range cfg.LLM.Targets {
		llmTargets = append(llmTargets, proxy.LLMTarget{
			URL:    t.URL,
			APIKey: t.APIKey,
		})
		logger.Info("LLM target configured", zap.String("url", t.URL))
	}

	// ---------------------------------------------------------------------------
	// 集群配置：primary 或 worker 模式
	// ---------------------------------------------------------------------------

	var clusterMgr *cluster.Manager
	var peerRegistry *cluster.PeerRegistry
	var reporter *cluster.Reporter
	sourceNode := "local"

	if cfg.Cluster.SelfAddr != "" {
		sourceNode = cfg.Cluster.SelfAddr
	}

	isPrimary := cfg.Cluster.Role == "primary" || cfg.Cluster.Role == ""

	if isPrimary {
		// sp-1：管理 peer 路由表，向 c-proxy 下发路由更新
		selfWeight := cfg.Cluster.SelfWeight
		if selfWeight <= 0 {
			selfWeight = 50
		}

		// 初始时路由表只有自身
		initialLBTargets := []lb.Target{}
		if cfg.Cluster.SelfAddr != "" {
			initialLBTargets = append(initialLBTargets, lb.Target{
				ID:      cfg.Cluster.SelfAddr,
				Addr:    cfg.Cluster.SelfAddr,
				Weight:  selfWeight,
				Healthy: true,
			})
		}

		peerBalancer := lb.NewWeightedRandom(initialLBTargets)
		cacheDir := cfg.Database.Path + ".routing" // 路由表缓存文件目录
		if err := os.MkdirAll(cacheDir, 0o700); err != nil {
			logger.Warn("failed to create routing cache dir", zap.Error(err))
			cacheDir = ""
		}
		clusterMgr = cluster.NewManager(logger, peerBalancer, initialLBTargets, cacheDir)
		peerRegistry = cluster.NewPeerRegistry(logger, clusterMgr)

		logger.Info("cluster: running as primary (sp-1)",
			zap.String("self_addr", cfg.Cluster.SelfAddr),
		)

		// 启动 peer 监控（定期驱逐过期节点）
		monitorInterval := cfg.Cluster.PeerMonitorInterval
		if monitorInterval <= 0 {
			monitorInterval = 30 * time.Second
		}
		go func() {
			ticker := time.NewTicker(monitorInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					peerRegistry.EvictStale()
				}
			}
		}()

	} else {
		// sp-2：定期向 sp-1 注册心跳 + 用量上报
		if cfg.Cluster.Primary == "" {
			return fmt.Errorf("cluster.primary is required for worker role")
		}
		usageRepo := db.NewUsageRepo(database, logger)
		reportInterval := cfg.Cluster.ReportInterval
		if reportInterval <= 0 {
			reportInterval = 30 * time.Second
		}
		reporter = cluster.NewReporter(logger, cluster.ReporterConfig{
			SP1Addr:    cfg.Cluster.Primary,
			SelfID:     cfg.Cluster.SelfAddr,
			SelfAddr:   cfg.Cluster.SelfAddr,
			SelfWeight: cfg.Cluster.SelfWeight,
			Interval:   reportInterval,
		}, usageRepo)
		reporter.Start(ctx)

		logger.Info("cluster: running as worker (sp-2)",
			zap.String("self_addr", cfg.Cluster.SelfAddr),
			zap.String("primary", cfg.Cluster.Primary),
		)
	}

	// ---------------------------------------------------------------------------
	// 初始化 s-proxy
	// ---------------------------------------------------------------------------

	var sp *proxy.SProxy
	if clusterMgr != nil {
		sp, err = proxy.NewSProxyWithCluster(logger, jwtMgr, writer, llmTargets, clusterMgr, sourceNode)
	} else {
		sp, err = proxy.NewSProxy(logger, jwtMgr, writer, llmTargets)
	}
	if err != nil {
		return fmt.Errorf("create sproxy: %w", err)
	}

	// ---------------------------------------------------------------------------
	// 配额检查（Phase 4）
	// ---------------------------------------------------------------------------

	userRepo := db.NewUserRepo(database, logger)
	usageRepo := db.NewUsageRepo(database, logger)
	groupRepo := db.NewGroupRepo(database, logger)
	quotaCache := quota.NewQuotaCache(60 * time.Second)
	quotaChecker := quota.NewChecker(logger, userRepo, usageRepo, quotaCache)
	sp.SetQuotaChecker(quotaChecker)
	logger.Info("quota checker enabled")

	// Phase 6: 费用计算
	if cfg.Pricing.DefaultInputPer1K > 0 || cfg.Pricing.DefaultOutputPer1K > 0 || len(cfg.Pricing.Models) > 0 {
		writer.SetCostFunc(cfg.Pricing.ComputeCost)
		logger.Info("pricing cost function enabled",
			zap.Float64("default_input_per_1k", cfg.Pricing.DefaultInputPer1K),
			zap.Float64("default_output_per_1k", cfg.Pricing.DefaultOutputPer1K),
			zap.Int("model_prices", len(cfg.Pricing.Models)),
		)
	}

	// Phase 6: 告警通知器
	if cfg.Cluster.AlertWebhook != "" {
		notifier := alert.NewNotifier(logger, cfg.Cluster.AlertWebhook)
		quotaChecker.SetNotifier(notifier)
		logger.Info("alert notifier enabled", zap.String("webhook", cfg.Cluster.AlertWebhook))
	}

	// Phase 6: 速率限制器定期清理（每分钟清理过期窗口）
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				quotaChecker.PurgeRateLimiter()
			}
		}
	}()

	// ---------------------------------------------------------------------------
	// 认证 + 管理 API（Phase 5）
	// ---------------------------------------------------------------------------

	tokenRepo := db.NewRefreshTokenRepo(database, logger)
	authCfg := api.AuthConfig{
		AccessTokenTTL:  cfg.Auth.AccessTokenTTL,
		RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
	}
	authHandler := api.NewAuthHandler(logger, jwtMgr, userRepo, tokenRepo, authCfg)

	adminTokenTTL := cfg.Auth.AccessTokenTTL
	if adminTokenTTL <= 0 {
		adminTokenTTL = 24 * time.Hour
	}
	adminHandler := api.NewAdminHandler(
		logger, jwtMgr, userRepo, groupRepo, usageRepo,
		cfg.Admin.PasswordHash, adminTokenTTL,
	)

	// ---------------------------------------------------------------------------
	// 注册路由
	// ---------------------------------------------------------------------------

	mux := http.NewServeMux()

	// 健康检查（无需认证）
	mux.HandleFunc("GET /health", sp.HealthHandler())

	// 用户认证 API
	authHandler.RegisterRoutes(mux)

	// 管理 REST API
	adminHandler.RegisterRoutes(mux)
	logger.Info("admin API registered at /api/admin/")

	// 集群内部 API（仅 primary）
	if peerRegistry != nil {
		clusterHandler := api.NewClusterHandler(logger, peerRegistry, writer, cfg.Cluster.SelfAddr)
		clusterHandler.RegisterRoutes(mux)
		logger.Info("cluster handler registered")
	}

	// Dashboard（Phase 5）
	if cfg.Dashboard.Enabled || isPrimary {
		dashHandler := dashboard.NewHandler(
			logger, jwtMgr, userRepo, groupRepo, usageRepo,
			cfg.Admin.PasswordHash, adminTokenTTL,
		)
		dashHandler.RegisterRoutes(mux)
		logger.Info("dashboard registered at /dashboard/")
	}

	// Phase 6: Prometheus metrics 端点
	metricsHandler := metrics.NewHandler(logger, usageRepo, userRepo)
	metricsHandler.RegisterRoutes(mux)
	logger.Info("metrics endpoint registered at GET /metrics")

	// 代理所有其他请求（需要 JWT 认证）
	mux.Handle("/", sp.Handler())

	addr := cfg.Listen.Addr()
	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Minute,  // SSE 流可能很长
		WriteTimeout: 10 * time.Minute, // 同上
		IdleTimeout:  2 * time.Minute,
	}

	// 优雅停机
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		logger.Info("received shutdown signal", zap.String("signal", sig.String()))
		cancel() // 停止后台 goroutine

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("server shutdown error", zap.Error(err))
		}

		// 等待用量写入器完成
		writer.Wait()
		logger.Info("sproxy shutdown complete")
	}()

	logger.Info("sproxy listening", zap.String("addr", addr), zap.String("role", role))
	fmt.Printf("sproxy [%s] listening on http://%s\n", role, addr)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("listen: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// sproxy hash-password（顶层命令，不依赖 DB）
// ---------------------------------------------------------------------------

var hashPasswordFlag string

var hashPasswordCmd = &cobra.Command{
	Use:   "hash-password",
	Short: "Generate a bcrypt hash for a password (used for admin.password_hash in sproxy.yaml)",
	RunE: func(cmd *cobra.Command, args []string) error {
		password := hashPasswordFlag
		if password == "" {
			var err error
			password, err = readPassword("Password: ")
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			confirm, err := readPassword("Confirm password: ")
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			if password != confirm {
				return fmt.Errorf("passwords do not match")
			}
		}
		if password == "" {
			return fmt.Errorf("password must not be empty")
		}
		logger, _ := zap.NewProduction()
		hash, err := auth.HashPassword(logger, password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		fmt.Println(hash)
		return nil
	},
}

func init() {
	hashPasswordCmd.Flags().StringVar(&hashPasswordFlag, "password", "", "password to hash (prompted if omitted)")
}

// ---------------------------------------------------------------------------
// sproxy admin
// ---------------------------------------------------------------------------

var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Admin commands for managing users, groups, and tokens",
}

var adminConfigFlag string

func init() {
	adminCmd.PersistentFlags().StringVar(&adminConfigFlag, "config", "", "path to sproxy.yaml (default: sproxy.yaml)")
	adminCmd.AddCommand(adminUserCmd, adminGroupCmd, adminStatsCmd, adminTokenCmd)
}

// openAdminDB 加载配置并打开数据库，供 admin CLI 子命令使用
func openAdminDB() (*db.UserRepo, *db.GroupRepo, *db.UsageRepo, *db.RefreshTokenRepo, *zap.Logger, error) {
	logger, err := zap.NewProduction()
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("init logger: %w", err)
	}
	cfgPath := adminConfigFlag
	if cfgPath == "" {
		cfgPath = "sproxy.yaml"
	}
	cfg, _, err := config.LoadSProxyConfig(cfgPath)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("load config from %q: %w", cfgPath, err)
	}
	database, err := db.Open(logger, cfg.Database.Path)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("migrate database: %w", err)
	}
	return db.NewUserRepo(database, logger),
		db.NewGroupRepo(database, logger),
		db.NewUsageRepo(database, logger),
		db.NewRefreshTokenRepo(database, logger),
		logger,
		nil
}

// readPassword 从终端读取密码（无回显），如果非终端则直接读取
func readPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	// 非终端（管道等）直接读一行
	var s string
	_, err := fmt.Scanln(&s)
	return s, err
}

// ---------------------------------------------------------------------------
// sproxy admin user
// ---------------------------------------------------------------------------

var adminUserCmd = &cobra.Command{
	Use:   "user",
	Short: "Manage users",
}

func init() {
	adminUserCmd.AddCommand(
		adminUserAddCmd,
		adminUserListCmd,
		adminUserDisableCmd,
		adminUserEnableCmd,
		adminUserResetPasswordCmd,
	)
}

// --- user add ---

var (
	userAddPassword string
	userAddGroup    string
)

var adminUserAddCmd = &cobra.Command{
	Use:   "add <username>",
	Short: "Create a new user",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		username := args[0]
		password := userAddPassword
		if password == "" {
			var err error
			password, err = readPassword("Password: ")
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
		}
		if password == "" {
			return fmt.Errorf("password must not be empty")
		}

		userRepo, groupRepo, _, _, logger, err := openAdminDB()
		if err != nil {
			return err
		}

		hash, err := auth.HashPassword(logger, password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}

		var groupID *string
		if userAddGroup != "" {
			grp, err := groupRepo.GetByName(userAddGroup)
			if err != nil {
				return fmt.Errorf("lookup group: %w", err)
			}
			if grp == nil {
				return fmt.Errorf("group %q not found", userAddGroup)
			}
			groupID = &grp.ID
		}

		user := &db.User{
			Username:     username,
			PasswordHash: hash,
			GroupID:      groupID,
			IsActive:     true,
		}
		if err := userRepo.Create(user); err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		fmt.Printf("User %q created (id: %s)\n", username, user.ID)
		return nil
	},
}

func init() {
	adminUserAddCmd.Flags().StringVar(&userAddPassword, "password", "", "password (prompted if omitted)")
	adminUserAddCmd.Flags().StringVar(&userAddGroup, "group", "", "group name to assign")
}

// --- user list ---

var adminUserListCmd = &cobra.Command{
	Use:   "list",
	Short: "List users",
	RunE: func(cmd *cobra.Command, args []string) error {
		userRepo, _, _, _, _, err := openAdminDB()
		if err != nil {
			return err
		}
		users, err := userRepo.ListByGroup("")
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		fmt.Printf("%-36s  %-20s  %-20s  %-8s\n", "ID", "Username", "Group", "Active")
		fmt.Println("--------------------------------------------------------------------------------------------")
		for _, u := range users {
			grp := ""
			if u.GroupID != nil {
				grp = u.Group.Name
			}
			active := "yes"
			if !u.IsActive {
				active = "no"
			}
			fmt.Printf("%-36s  %-20s  %-20s  %-8s\n", u.ID, u.Username, grp, active)
		}
		return nil
	},
}

// --- user disable ---

var adminUserDisableCmd = &cobra.Command{
	Use:   "disable <username>",
	Short: "Disable a user account",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setUserActive(args[0], false)
	},
}

// --- user enable ---

var adminUserEnableCmd = &cobra.Command{
	Use:   "enable <username>",
	Short: "Enable a user account",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setUserActive(args[0], true)
	},
}

func setUserActive(username string, active bool) error {
	userRepo, _, _, _, _, err := openAdminDB()
	if err != nil {
		return err
	}
	user, err := userRepo.GetByUsername(username)
	if err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}
	if user == nil {
		return fmt.Errorf("user %q not found", username)
	}
	if err := userRepo.SetActive(user.ID, active); err != nil {
		return err
	}
	action := "enabled"
	if !active {
		action = "disabled"
	}
	fmt.Printf("User %q %s\n", username, action)
	return nil
}

// --- user reset-password ---

var userResetPassword string

var adminUserResetPasswordCmd = &cobra.Command{
	Use:   "reset-password <username>",
	Short: "Reset a user's password",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		username := args[0]
		password := userResetPassword
		if password == "" {
			var err error
			password, err = readPassword("New password: ")
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
		}
		if password == "" {
			return fmt.Errorf("password must not be empty")
		}

		userRepo, _, _, _, logger, err := openAdminDB()
		if err != nil {
			return err
		}
		user, err := userRepo.GetByUsername(username)
		if err != nil {
			return fmt.Errorf("lookup user: %w", err)
		}
		if user == nil {
			return fmt.Errorf("user %q not found", username)
		}
		hash, err := auth.HashPassword(logger, password)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		if err := userRepo.UpdatePassword(user.ID, hash); err != nil {
			return err
		}
		fmt.Printf("Password for %q has been reset\n", username)
		return nil
	},
}

func init() {
	adminUserResetPasswordCmd.Flags().StringVar(&userResetPassword, "password", "", "new password (prompted if omitted)")
}

// ---------------------------------------------------------------------------
// sproxy admin group
// ---------------------------------------------------------------------------

var adminGroupCmd = &cobra.Command{
	Use:   "group",
	Short: "Manage groups",
}

func init() {
	adminGroupCmd.AddCommand(
		adminGroupAddCmd,
		adminGroupListCmd,
		adminGroupSetQuotaCmd,
	)
}

// --- group add ---

var (
	groupAddDailyLimit   int64
	groupAddMonthlyLimit int64
	groupAddRPM          int
)

var adminGroupAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new group",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_, groupRepo, _, _, _, err := openAdminDB()
		if err != nil {
			return err
		}

		g := &db.Group{Name: name}
		if cmd.Flags().Changed("daily-limit") {
			g.DailyTokenLimit = &groupAddDailyLimit
		}
		if cmd.Flags().Changed("monthly-limit") {
			g.MonthlyTokenLimit = &groupAddMonthlyLimit
		}
		if cmd.Flags().Changed("rpm") {
			g.RequestsPerMinute = &groupAddRPM
		}
		if err := groupRepo.Create(g); err != nil {
			return fmt.Errorf("create group: %w", err)
		}
		fmt.Printf("Group %q created (id: %s)\n", name, g.ID)
		return nil
	},
}

func init() {
	adminGroupAddCmd.Flags().Int64Var(&groupAddDailyLimit, "daily-limit", 0, "daily token limit (0 = unlimited)")
	adminGroupAddCmd.Flags().Int64Var(&groupAddMonthlyLimit, "monthly-limit", 0, "monthly token limit (0 = unlimited)")
	adminGroupAddCmd.Flags().IntVar(&groupAddRPM, "rpm", 0, "max requests per minute (0 = unlimited)")
}

// --- group list ---

var adminGroupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List groups",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, groupRepo, _, _, _, err := openAdminDB()
		if err != nil {
			return err
		}
		groups, err := groupRepo.List()
		if err != nil {
			return fmt.Errorf("list groups: %w", err)
		}
		fmt.Printf("%-36s  %-20s  %-15s  %-15s  %-10s\n", "ID", "Name", "Daily Limit", "Monthly Limit", "RPM")
		fmt.Println("-----------------------------------------------------------------------------------------------------------")
		for _, g := range groups {
			daily := "unlimited"
			if g.DailyTokenLimit != nil {
				daily = strconv.FormatInt(*g.DailyTokenLimit, 10)
			}
			monthly := "unlimited"
			if g.MonthlyTokenLimit != nil {
				monthly = strconv.FormatInt(*g.MonthlyTokenLimit, 10)
			}
			rpm := "unlimited"
			if g.RequestsPerMinute != nil {
				rpm = strconv.Itoa(*g.RequestsPerMinute)
			}
			fmt.Printf("%-36s  %-20s  %-15s  %-15s  %-10s\n", g.ID, g.Name, daily, monthly, rpm)
		}
		return nil
	},
}

// --- group set-quota ---

var (
	setQuotaDaily   int64
	setQuotaMonthly int64
	setQuotaRPM     int
)

var adminGroupSetQuotaCmd = &cobra.Command{
	Use:   "set-quota <name>",
	Short: "Set token quota for a group",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_, groupRepo, _, _, _, err := openAdminDB()
		if err != nil {
			return err
		}
		grp, err := groupRepo.GetByName(name)
		if err != nil {
			return fmt.Errorf("lookup group: %w", err)
		}
		if grp == nil {
			return fmt.Errorf("group %q not found", name)
		}

		var daily, monthly *int64
		var rpm *int
		if cmd.Flags().Changed("daily") {
			daily = &setQuotaDaily
		}
		if cmd.Flags().Changed("monthly") {
			monthly = &setQuotaMonthly
		}
		if cmd.Flags().Changed("rpm") {
			rpm = &setQuotaRPM
		}
		if err := groupRepo.SetQuota(grp.ID, daily, monthly, rpm); err != nil {
			return err
		}
		fmt.Printf("Quota updated for group %q\n", name)
		return nil
	},
}

func init() {
	adminGroupSetQuotaCmd.Flags().Int64Var(&setQuotaDaily, "daily", 0, "daily token limit (0 = remove limit)")
	adminGroupSetQuotaCmd.Flags().Int64Var(&setQuotaMonthly, "monthly", 0, "monthly token limit (0 = remove limit)")
	adminGroupSetQuotaCmd.Flags().IntVar(&setQuotaRPM, "rpm", 0, "max requests per minute (0 = remove limit)")
}

// ---------------------------------------------------------------------------
// sproxy admin stats
// ---------------------------------------------------------------------------

var (
	statsUser string
	statsDays int
)

var adminStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show token usage statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		userRepo, _, usageRepo, _, _, err := openAdminDB()
		if err != nil {
			return err
		}

		now := time.Now()
		from := now.AddDate(0, 0, -statsDays+1).Truncate(24 * time.Hour)

		if statsUser != "" {
			// 单用户统计
			user, err := userRepo.GetByUsername(statsUser)
			if err != nil {
				return fmt.Errorf("lookup user: %w", err)
			}
			if user == nil {
				return fmt.Errorf("user %q not found", statsUser)
			}
			input, output, err := usageRepo.SumTokens(user.ID, from, now)
			if err != nil {
				return err
			}
			fmt.Printf("User: %s (%s)\n", user.Username, user.ID)
			fmt.Printf("Period: last %d day(s) (%s ~ %s)\n", statsDays,
				from.Format("2006-01-02"), now.Format("2006-01-02"))
			fmt.Printf("Input tokens:  %d\n", input)
			fmt.Printf("Output tokens: %d\n", output)
			fmt.Printf("Total tokens:  %d\n", input+output)
		} else {
			// 全局统计
			stats, err := usageRepo.GlobalSumTokens(from, now)
			if err != nil {
				return err
			}
			fmt.Printf("Period: last %d day(s) (%s ~ %s)\n", statsDays,
				from.Format("2006-01-02"), now.Format("2006-01-02"))
			fmt.Printf("Input tokens:   %d\n", stats.TotalInput)
			fmt.Printf("Output tokens:  %d\n", stats.TotalOutput)
			fmt.Printf("Total tokens:   %d\n", stats.TotalTokens)
			fmt.Printf("Requests:       %d\n", stats.RequestCount)
			fmt.Printf("Errors:         %d\n", stats.ErrorCount)

			// 按用户排行
			rows, err := usageRepo.UserStats(from, now, 10)
			if err != nil {
				return err
			}
			if len(rows) > 0 {
				fmt.Println("\nTop users:")
				fmt.Printf("  %-36s  %-12s\n", "User ID", "Total Tokens")
				for _, r := range rows {
					fmt.Printf("  %-36s  %-12d\n", r.UserID, r.TotalInput+r.TotalOutput)
				}
			}
		}
		return nil
	},
}

func init() {
	adminStatsCmd.Flags().StringVar(&statsUser, "user", "", "filter by username")
	adminStatsCmd.Flags().IntVar(&statsDays, "days", 7, "number of days to include")
}

// ---------------------------------------------------------------------------
// sproxy admin token
// ---------------------------------------------------------------------------

var adminTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage user tokens",
}

func init() {
	adminTokenCmd.AddCommand(adminTokenRevokeCmd)
}

var adminTokenRevokeCmd = &cobra.Command{
	Use:   "revoke <username>",
	Short: "Revoke all refresh tokens for a user (force logout)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		username := args[0]
		userRepo, _, _, tokenRepo, _, err := openAdminDB()
		if err != nil {
			return err
		}
		user, err := userRepo.GetByUsername(username)
		if err != nil {
			return fmt.Errorf("lookup user: %w", err)
		}
		if user == nil {
			return fmt.Errorf("user %q not found", username)
		}
		if err := tokenRepo.RevokeAllForUser(user.ID); err != nil {
			return err
		}
		fmt.Printf("All refresh tokens revoked for user %q\n", username)
		fmt.Println("Note: existing access tokens will expire within their TTL (up to 24h)")
		return nil
	},
}
