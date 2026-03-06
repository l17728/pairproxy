package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/term"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/alert"
	"github.com/l17728/pairproxy/internal/api"
	"github.com/l17728/pairproxy/internal/auth"
	"github.com/l17728/pairproxy/internal/cluster"
	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/dashboard"
	"github.com/l17728/pairproxy/internal/db"
	"github.com/l17728/pairproxy/internal/lb"
	"github.com/l17728/pairproxy/internal/metrics"
	pptel "github.com/l17728/pairproxy/internal/otel"
	"github.com/l17728/pairproxy/internal/preflight"
	"github.com/l17728/pairproxy/internal/proxy"
	"github.com/l17728/pairproxy/internal/quota"
	"github.com/l17728/pairproxy/internal/version"
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
	// 初始化日志（使用 AtomicLevel 支持 SIGHUP 动态调整日志级别）
	atom := zap.NewAtomicLevelAt(zapcore.InfoLevel)
	logger := buildLogger(atom)
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

	// 根据配置设置初始日志级别
	atom.SetLevel(parseZapLevel(cfg.Log.Level))
	if cfg.Log.Level != "" && cfg.Log.Level != "info" {
		logger.Debug("log level applied from config", zap.String("level", cfg.Log.Level))
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

	// Preflight 检查：DB 目录可写、监听端口未被占用
	if err := preflight.CheckSProxy(logger, cfg); err != nil {
		return fmt.Errorf("preflight: %w", err)
	}

	// F-7: OpenTelemetry 初始化（disabled 时为 noop，零开销）
	otelCfg := pptel.TelemetryConfig{
		Enabled:      cfg.Telemetry.Enabled,
		OTLPEndpoint: cfg.Telemetry.OTLPEndpoint,
		OTLPProtocol: cfg.Telemetry.OTLPProtocol,
		ServiceName:  cfg.Telemetry.ServiceName,
		SamplingRate: cfg.Telemetry.SamplingRate,
	}
	if otelCfg.ServiceName == "" {
		otelCfg.ServiceName = "pairproxy-sproxy"
	}
	otelShutdown, otelErr := pptel.Setup(context.Background(), otelCfg, logger)
	if otelErr != nil {
		logger.Warn("otel: init failed, continuing without tracing", zap.Error(otelErr))
	} else {
		defer otelShutdown(context.Background())
	}

	// 打开数据库
	database, err := db.OpenWithConfig(logger, cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	// P0-1: 确保进程退出时关闭数据库连接，释放文件锁和文件描述符
	defer closeGormDB(logger, database)

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
			URL:      t.URL,
			APIKey:   t.APIKey,
			Provider: t.Provider,
			Name:     t.Name,
			Weight:   t.Weight,
		})
		logger.Info("LLM target configured",
			zap.String("url", t.URL),
			zap.String("name", t.Name),
			zap.String("provider", t.Provider),
			zap.Int("weight", t.Weight),
		)
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
		// 确保 primary 自身始终包含在路由表中，不被 worker 心跳覆盖
		if cfg.Cluster.SelfAddr != "" {
			peerRegistry.SetSelfTarget(lb.Target{
				ID:      cfg.Cluster.SelfAddr,
				Addr:    cfg.Cluster.SelfAddr,
				Weight:  selfWeight,
				Healthy: true,
			})
		}

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
			SP1Addr:      cfg.Cluster.Primary,
			SelfID:       cfg.Cluster.SelfAddr,
			SelfAddr:     cfg.Cluster.SelfAddr,
			SelfWeight:   cfg.Cluster.SelfWeight,
			Interval:     reportInterval,
			SharedSecret: cfg.Cluster.SharedSecret, // P0-4: 集群内部 API 认证密钥
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
	// LLM 负载均衡 + 健康检查 + 绑定解析（可靠性特性）
	// ---------------------------------------------------------------------------

	{
		// 构建 LLM lb.Target 列表（ID = URL，Weight 来自配置）
		lbLLMTargets := make([]lb.Target, 0, len(cfg.LLM.Targets))
		healthPaths := make(map[string]string, len(cfg.LLM.Targets))
		for _, t := range cfg.LLM.Targets {
			w := t.Weight
			if w <= 0 {
				w = 1
			}
			lbLLMTargets = append(lbLLMTargets, lb.Target{
				ID:      t.URL,
				Addr:    t.URL,
				Weight:  w,
				Healthy: true,
			})
			if t.HealthCheckPath != "" {
				healthPaths[t.URL] = t.HealthCheckPath
			}
		}

		llmBalancer := lb.NewWeightedRandom(lbLLMTargets)
		hcOpts := []lb.HealthCheckerOption{
			lb.WithFailThreshold(3),
			lb.WithInterval(30 * time.Second),
		}
		if cfg.LLM.RecoveryDelay > 0 {
			hcOpts = append(hcOpts, lb.WithRecoveryDelay(cfg.LLM.RecoveryDelay))
		}
		if len(healthPaths) > 0 {
			hcOpts = append(hcOpts, lb.WithHealthPaths(healthPaths))
		}

		llmHC := lb.NewHealthChecker(llmBalancer, logger, hcOpts...)
		llmHC.Start(ctx)

		sp.SetLLMHealthChecker(llmBalancer, llmHC)
		sp.SetMaxRetries(cfg.LLM.MaxRetries)

		logger.Info("LLM balancer configured",
			zap.Int("targets", len(lbLLMTargets)),
			zap.Int("max_retries", cfg.LLM.MaxRetries),
			zap.Duration("recovery_delay", cfg.LLM.RecoveryDelay),
			zap.Int("health_check_paths", len(healthPaths)),
		)
	}

	// ---------------------------------------------------------------------------
	// 配额检查（Phase 4）
	// ---------------------------------------------------------------------------

	userRepo := db.NewUserRepo(database, logger)
	usageRepo := db.NewUsageRepo(database, logger)
	groupRepo := db.NewGroupRepo(database, logger)
	auditRepo := db.NewAuditRepo(logger, database) // P2-3: 审计日志仓库
	quotaCache := quota.NewQuotaCache(60 * time.Second)
	quotaChecker := quota.NewChecker(logger, userRepo, usageRepo, quotaCache)
	sp.SetQuotaChecker(quotaChecker)
	logger.Info("quota checker enabled")

	// P1-4: 设置 DB 连接供 /health 端点 ping 检查
	sp.SetDB(database)
	logger.Debug("health check: db ping enabled")

	// Phase 6: 费用计算
	if cfg.Pricing.DefaultInputPer1K > 0 || cfg.Pricing.DefaultOutputPer1K > 0 || len(cfg.Pricing.Models) > 0 {
		writer.SetCostFunc(cfg.Pricing.ComputeCost)
		logger.Info("pricing cost function enabled",
			zap.Float64("default_input_per_1k", cfg.Pricing.DefaultInputPer1K),
			zap.Float64("default_output_per_1k", cfg.Pricing.DefaultOutputPer1K),
			zap.Int("model_prices", len(cfg.Pricing.Models)),
		)
	}

	// Phase 6: 告警通知器（支持多 webhook 目标 + 事件过滤 + 自定义模板）
	var notifier *alert.Notifier
	{
		var targets []alert.WebhookTargetConfig
		for _, wt := range cfg.Cluster.AlertWebhooks {
			targets = append(targets, alert.WebhookTargetConfig{
				URL:      wt.URL,
				Events:   wt.Events,
				Template: wt.Template,
			})
		}
		notifier = alert.NewNotifierMulti(logger, targets, cfg.Cluster.AlertWebhook)
		quotaChecker.SetNotifier(notifier)
		totalTargets := len(targets)
		if cfg.Cluster.AlertWebhook != "" {
			totalTargets++ // legacy URL（若不重复）
		}
		if totalTargets > 0 {
			logger.Info("alert notifier enabled",
				zap.Int("targets", totalTargets),
				zap.String("legacy_webhook", cfg.Cluster.AlertWebhook),
			)
		}
	}

	// 活跃请求数阈值监控：alert_threshold > 0 时启用
	sp.SetNotifier(notifier)
	if cfg.Cluster.AlertThreshold > 0 {
		proxy.StartActiveRequestsMonitor(ctx, sp, int64(cfg.Cluster.AlertThreshold), notifier, sourceNode, logger)
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
	var trustedProxies []net.IPNet
	for _, cidr := range cfg.Auth.TrustedProxies {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			logger.Warn("invalid trusted_proxy CIDR, skipping",
				zap.String("cidr", cidr), zap.Error(err))
			continue
		}
		trustedProxies = append(trustedProxies, *ipNet)
	}
	authCfg := api.AuthConfig{
		AccessTokenTTL:  cfg.Auth.AccessTokenTTL,
		RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
		TrustedProxies:  trustedProxies,
		DefaultGroup:    cfg.Auth.DefaultGroup,
	}
	authHandler := api.NewAuthHandler(logger, jwtMgr, userRepo, tokenRepo, authCfg)

	// F-4: LDAP 认证提供者（provider="ldap" 时启用）
	if cfg.Auth.Provider == "ldap" {
		ldapCfg := auth.LDAPConfig{
			ServerAddr:   cfg.Auth.LDAP.ServerAddr,
			BaseDN:       cfg.Auth.LDAP.BaseDN,
			BindDN:       cfg.Auth.LDAP.BindDN,
			BindPassword: cfg.Auth.LDAP.BindPassword,
			UserFilter:   cfg.Auth.LDAP.UserFilter,
			UseTLS:       cfg.Auth.LDAP.UseTLS,
		}
		if ldapCfg.UserFilter == "" {
			ldapCfg.UserFilter = "(uid=%s)" // 默认过滤器
		}
		ldapProvider := auth.NewLDAPProvider(logger, ldapCfg)
		authHandler.SetProvider(ldapProvider)
		logger.Info("auth: LDAP provider enabled",
			zap.String("server", ldapCfg.ServerAddr),
			zap.String("base_dn", ldapCfg.BaseDN),
		)
	}

	adminTokenTTL := cfg.Auth.AccessTokenTTL
	if adminTokenTTL <= 0 {
		adminTokenTTL = 24 * time.Hour
	}
	adminHandler := api.NewAdminHandler(
		logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo,
		cfg.Admin.PasswordHash, adminTokenTTL,
	)

	// F-5: 多 API Key 管理（需要 key_encryption_key 配置）
	if cfg.Admin.KeyEncryptionKey != "" {
		apiKeyRepo := db.NewAPIKeyRepo(database, logger)
		encryptFn := func(plain string) (string, error) {
			return auth.Encrypt(plain, cfg.Admin.KeyEncryptionKey)
		}
		decryptFn := func(encrypted string) (string, error) {
			return auth.Decrypt(encrypted, cfg.Admin.KeyEncryptionKey)
		}
		adminHandler.SetAPIKeyRepo(apiKeyRepo, encryptFn)
		// 在 Director 中动态查找并使用 DB 里的 API Key
		sp.SetAPIKeyResolver(func(userID string) (string, bool) {
			user, err := userRepo.GetByID(userID)
			if err != nil || user == nil {
				return "", false
			}
			groupID := ""
			if user.GroupID != nil {
				groupID = *user.GroupID
			}
			key, err := apiKeyRepo.FindForUser(userID, groupID)
			if err != nil || key == nil {
				return "", false
			}
			plain, err := decryptFn(key.EncryptedValue)
			if err != nil {
				logger.Warn("failed to decrypt api key",
					zap.String("key_name", key.Name),
					zap.Error(err),
				)
				return "", false
			}
			return plain, true
		})
		logger.Info("F-5: dynamic api key management enabled")
	}

	// LLM 绑定仓库 + 绑定解析器
	llmBindingRepo := db.NewLLMBindingRepo(database, logger)
	sp.SetBindingResolver(func(userID, groupID string) (string, bool) {
		targetURL, found, err := llmBindingRepo.FindForUser(userID, groupID)
		if err != nil {
			logger.Warn("llm binding lookup failed", zap.Error(err))
			return "", false
		}
		return targetURL, found
	})
	adminHandler.SetLLMBindingRepo(llmBindingRepo)
	adminHandler.SetLLMHealthFn(sp.LLMTargetStatuses)
	adminHandler.SetTokenRepo(tokenRepo)
	logger.Info("LLM binding repo configured")

	// 排水控制函数
	adminHandler.SetDrainFunctions(sp.Drain, sp.Undrain, sp.GetDrainStatus)
	logger.Info("drain control functions configured")

	// debug 文件日志：转发内容双向记录（log.debug_file 配置时启用）
	if cfg.Log.DebugFile != "" {
		debugLogger, dbgErr := buildDebugFileLogger(cfg.Log.DebugFile)
		if dbgErr != nil {
			logger.Warn("failed to init debug file logger, debug logging disabled",
				zap.String("path", cfg.Log.DebugFile),
				zap.Error(dbgErr),
			)
		} else {
			sp.SetDebugLogger(debugLogger)
			logger.Info("debug file logging enabled", zap.String("path", cfg.Log.DebugFile))
		}
	}

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
	adminHandler.RegisterLLMRoutes(mux)
	logger.Info("admin API registered at /api/admin/")

	// 用户自助服务 API（F-10 WebUI 增强）
	userHandler := api.NewUserHandler(logger, jwtMgr, userRepo, groupRepo, usageRepo)
	userHandler.RegisterRoutes(mux)
	logger.Info("user self-service API registered at /api/user/")

	// 集群内部 API（仅 primary）
	if peerRegistry != nil {
		// P0-4: 使用 cluster.shared_secret 作为内部 API 认证密钥，而非节点地址
		clusterHandler := api.NewClusterHandler(logger, peerRegistry, writer, cfg.Cluster.SharedSecret)
		clusterHandler.RegisterRoutes(mux)
		if cfg.Cluster.SharedSecret == "" {
			logger.Warn("cluster.shared_secret is not configured; internal API will reject all requests (fail-closed). " +
				"Set cluster.shared_secret in sproxy.yaml to enable worker heartbeat.")
		} else {
			logger.Info("cluster handler registered with shared secret authentication")
		}
	}

	// Dashboard（Phase 5）
	if cfg.Dashboard.Enabled || isPrimary {
		dashHandler := dashboard.NewHandler(
			logger, jwtMgr, userRepo, groupRepo, usageRepo, auditRepo,
			cfg.Admin.PasswordHash, adminTokenTTL,
		)
		dashHandler.SetLLMDeps(llmBindingRepo, sp.LLMTargetStatuses)
		dashHandler.SetTokenRepo(tokenRepo)
		dashHandler.SetDrainFunctions(sp.Drain, sp.Undrain, sp.GetDrainStatus)
		dashHandler.RegisterRoutes(mux)
		logger.Info("dashboard registered at /dashboard/")
	}

	// Phase 6: Prometheus metrics 端点
	metricsHandler := metrics.NewHandler(logger, usageRepo, userRepo)
	metricsHandler.SetDBPath(cfg.Database.Path)           // P2-2: 数据库文件大小指标
	metricsHandler.SetQuotaCacheStats(quotaCache)         // P2-2: 配额缓存命中/未命中指标
	if reporter != nil {
		metricsHandler.SetReporterStats(reporter)         // P2-2: worker 心跳延迟/失败指标
	}
	metricsHandler.RegisterRoutes(mux)
	logger.Info("metrics endpoint registered at GET /metrics")

	// 代理所有其他请求（需要 JWT 认证）
	// F-7: 若 OTel 启用，用 otelhttp 包装以捕获 HTTP 层 span
	proxyHandler := sp.Handler()
	if cfg.Telemetry.Enabled {
		proxyHandler = wrapOtelHTTP(proxyHandler, "pairproxy.sproxy")
	}
	mux.Handle("/", proxyHandler)

	addr := cfg.Listen.Addr()
	server := &http.Server{
		Addr:        addr,
		Handler:     mux,
		ReadTimeout: 60 * time.Second, // 读取请求头+请求体（请求体为小 JSON，60s 足够）
		// WriteTimeout 设为 0（禁用）：LLM extended thinking 可能静默超过 30 分钟，
		// 任何固定值都会误杀长流；依赖客户端断开检测终止挂起连接。
		WriteTimeout: 0,
		IdleTimeout:  2 * time.Minute, // keep-alive 空闲超时（与活跃流无关）
	}

	// SIGHUP 热重载（Unix/Linux only；Windows 上为 no-op）
	// 重新加载：log.level 动态切换；debug_file 开关切换；其他字段（端口、DB 路径）需重启生效。
	currentDebugFile := cfg.Log.DebugFile // 仅在 SIGHUP goroutine 中读写，无需加锁
	sighupCh := make(chan os.Signal, 1)
	notifySIGHUP(sighupCh)
	go func() {
		for range sighupCh {
			logger.Info("SIGHUP received — reloading config", zap.String("config", cfgPath))
			newCfg, newWarnings, reloadErr := config.LoadSProxyConfig(cfgPath)
			if reloadErr != nil {
				logger.Error("config reload failed, keeping current config",
					zap.String("config", cfgPath),
					zap.Error(reloadErr),
				)
				continue
			}
			for _, w := range newWarnings {
				logger.Warn("config reload warning", zap.String("detail", w))
			}
			// 动态更新日志级别（立即生效，无需重启）
			newLevel := parseZapLevel(newCfg.Log.Level)
			oldLevel := atom.Level()
			levelChanged := newLevel != oldLevel
			if levelChanged {
				atom.SetLevel(newLevel)
				logger.Info("log level changed via SIGHUP",
					zap.String("old", oldLevel.String()),
					zap.String("new", newLevel.String()),
				)
			}
			// 动态切换 debug 文件日志（log.debug_file 变更时立即生效）
			newDebugFile := newCfg.Log.DebugFile
			debugFileChanged := newDebugFile != currentDebugFile
			if debugFileChanged {
				if newDebugFile != "" {
					newDL, dlErr := buildDebugFileLogger(newDebugFile)
					if dlErr != nil {
						logger.Warn("failed to init debug file logger via SIGHUP",
							zap.String("path", newDebugFile), zap.Error(dlErr))
					} else {
						sp.SyncAndSetDebugLogger(newDL) // flush 旧 logger，原子切换为新 logger
						logger.Info("debug file logging enabled via SIGHUP",
							zap.String("path", newDebugFile))
					}
				} else {
					sp.SyncAndSetDebugLogger(nil) // flush 旧 logger，关闭 debug 日志
					logger.Info("debug file logging disabled via SIGHUP")
				}
				currentDebugFile = newDebugFile
			}
			if !levelChanged && !debugFileChanged {
				logger.Info("config reloaded (no changes requiring restart)",
					zap.String("log_level", newLevel.String()),
				)
			}
		}
	}()

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
	adminCmd.AddCommand(adminUserCmd, adminGroupCmd, adminStatsCmd, adminTokenCmd, adminBackupCmd, adminRestoreCmd, adminLogsCmd, adminExportCmd, adminApikeyCmd, adminLLMCmd, adminQuotaCmd, adminAuditCmd, adminDrainCmd)
}

// closeGormDB 优雅关闭 GORM 数据库连接，释放文件锁和文件描述符。
// 应通过 defer 调用，确保在任何退出路径下都能执行。
func closeGormDB(logger *zap.Logger, database *gorm.DB) {
	sqlDB, err := database.DB()
	if err != nil {
		logger.Error("failed to get underlying sql.DB for close", zap.Error(err))
		return
	}
	if err := sqlDB.Close(); err != nil {
		logger.Error("failed to close database connection", zap.Error(err))
	} else {
		logger.Info("database connection closed")
	}
}

// openAdminDB 加载配置并打开数据库，供 admin CLI 子命令使用。
// 调用方必须在使用完毕后调用 defer closeGormDB(logger, database) 以防止连接泄漏。
func openAdminDB() (*db.UserRepo, *db.GroupRepo, *db.UsageRepo, *db.RefreshTokenRepo, *zap.Logger, *gorm.DB, error) {
	logger, err := zap.NewProduction()
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("init logger: %w", err)
	}
	cfgPath := adminConfigFlag
	if cfgPath == "" {
		cfgPath = "sproxy.yaml"
	}
	cfg, _, err := config.LoadSProxyConfig(cfgPath)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("load config from %q: %w", cfgPath, err)
	}
	database, err := db.OpenWithConfig(logger, cfg.Database)
	if err != nil {
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		// 迁移失败：关闭已打开的 DB，防止泄漏
		closeGormDB(logger, database)
		return nil, nil, nil, nil, nil, nil, fmt.Errorf("migrate database: %w", err)
	}
	return db.NewUserRepo(database, logger),
		db.NewGroupRepo(database, logger),
		db.NewUsageRepo(database, logger),
		db.NewRefreshTokenRepo(database, logger),
		logger,
		database, // P0-2: 返回 DB handle 供调用方 defer close
		nil
}

// auditCLI 为 CLI 管理操作写入审计日志（失败时仅警告，不阻断操作）。
func auditCLI(gormDB *gorm.DB, logger *zap.Logger, action, target, detail string) {
	repo := db.NewAuditRepo(logger, gormDB)
	if err := repo.Create("cli-admin", action, target, detail); err != nil {
		logger.Warn("cli audit write failed", zap.String("action", action), zap.Error(err))
	}
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
		adminUserSetGroupCmd,
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

		userRepo, groupRepo, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

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
		auditCLI(database, logger, "user.create", username, fmt.Sprintf("group=%s", userAddGroup))
		fmt.Printf("User %q created (id: %s)\n", username, user.ID)
		return nil
	},
}

func init() {
	adminUserAddCmd.Flags().StringVar(&userAddPassword, "password", "", "password (prompted if omitted)")
	adminUserAddCmd.Flags().StringVar(&userAddGroup, "group", "", "group name to assign")
}

// --- user list ---

var userListGroup string

var adminUserListCmd = &cobra.Command{
	Use:   "list",
	Short: "List users",
	RunE: func(cmd *cobra.Command, args []string) error {
		userRepo, groupRepo, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		filterGroupID := ""
		if userListGroup != "" {
			g, err := groupRepo.GetByName(userListGroup)
			if err != nil {
				return fmt.Errorf("lookup group: %w", err)
			}
			if g == nil {
				return fmt.Errorf("group %q not found", userListGroup)
			}
			filterGroupID = g.ID
		}

		users, err := userRepo.ListByGroup(filterGroupID)
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

func init() {
	adminUserListCmd.Flags().StringVar(&userListGroup, "group", "", "filter by group name")
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
	logger, _ := zap.NewProduction()
	userRepo, _, _, _, _, database, err := openAdminDB()
	if err != nil {
		return err
	}
	defer closeGormDB(logger, database)
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
	auditAction := "user.enable"
	if !active {
		action = "disabled"
		auditAction = "user.disable"
	}
	auditCLI(database, logger, auditAction, username, "")
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

		userRepo, _, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)
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
		auditCLI(database, logger, "user.reset_password", username, "")
		fmt.Printf("Password for %q has been reset\n", username)
		return nil
	},
}

func init() {
	adminUserResetPasswordCmd.Flags().StringVar(&userResetPassword, "password", "", "new password (prompted if omitted)")
}

// --- user set-group ---

var (
	userSetGroupName   string
	userSetGroupRemove bool
)

var adminUserSetGroupCmd = &cobra.Command{
	Use:   "set-group <username>",
	Short: "Assign or remove a user's group",
	Long: `Assign a user to a group (--group <name>) or remove them from any group (--ungroup).

Examples:
  sproxy admin user set-group alice --group enterprise
  sproxy admin user set-group alice --ungroup`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !userSetGroupRemove && userSetGroupName == "" {
			return fmt.Errorf("provide --group <name> to assign, or --ungroup to remove")
		}
		userRepo, groupRepo, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		u, err := userRepo.GetByUsername(args[0])
		if err != nil {
			return fmt.Errorf("lookup user: %w", err)
		}
		if u == nil {
			return fmt.Errorf("user %q not found", args[0])
		}

		var groupID *string
		if !userSetGroupRemove {
			g, err := groupRepo.GetByName(userSetGroupName)
			if err != nil {
				return fmt.Errorf("lookup group: %w", err)
			}
			if g == nil {
				return fmt.Errorf("group %q not found", userSetGroupName)
			}
			groupID = &g.ID
		}

		if err := userRepo.SetGroup(u.ID, groupID); err != nil {
			return err
		}
		if userSetGroupRemove {
			auditCLI(database, logger, "user.ungroup", args[0], "")
			fmt.Printf("User %q removed from group\n", args[0])
		} else {
			auditCLI(database, logger, "user.set_group", args[0], "group="+userSetGroupName)
			fmt.Printf("User %q assigned to group %q\n", args[0], userSetGroupName)
		}
		return nil
	},
}

func init() {
	adminUserSetGroupCmd.Flags().StringVar(&userSetGroupName, "group", "", "target group name")
	adminUserSetGroupCmd.Flags().BoolVar(&userSetGroupRemove, "ungroup", false, "remove user from any group")
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
		adminGroupDeleteCmd,
	)
}

// --- group add ---

var (
	groupAddDailyLimit        int64
	groupAddMonthlyLimit      int64
	groupAddRPM               int
	groupAddMaxReqTokens      int64
	groupAddConcurrentReqs    int
)

var adminGroupAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Create a new group",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_, groupRepo, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

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
		if cmd.Flags().Changed("max-tokens-per-request") {
			g.MaxTokensPerRequest = &groupAddMaxReqTokens
		}
		if cmd.Flags().Changed("concurrent-requests") {
			g.ConcurrentRequests = &groupAddConcurrentReqs
		}
		if err := groupRepo.Create(g); err != nil {
			return fmt.Errorf("create group: %w", err)
		}
		auditCLI(database, zap.NewNop(), "group.create", name, "")
		fmt.Printf("Group %q created (id: %s)\n", name, g.ID)
		return nil
	},
}

func init() {
	adminGroupAddCmd.Flags().Int64Var(&groupAddDailyLimit, "daily-limit", 0, "daily token limit (0 = unlimited)")
	adminGroupAddCmd.Flags().Int64Var(&groupAddMonthlyLimit, "monthly-limit", 0, "monthly token limit (0 = unlimited)")
	adminGroupAddCmd.Flags().IntVar(&groupAddRPM, "rpm", 0, "max requests per minute (0 = unlimited)")
	adminGroupAddCmd.Flags().Int64Var(&groupAddMaxReqTokens, "max-tokens-per-request", 0, "max max_tokens a user may request per call (0 = unlimited)")
	adminGroupAddCmd.Flags().IntVar(&groupAddConcurrentReqs, "concurrent-requests", 0, "max concurrent requests per user (0 = unlimited)")
}

// --- group list ---

var adminGroupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List groups",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, groupRepo, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)
		groups, err := groupRepo.List()
		if err != nil {
			return fmt.Errorf("list groups: %w", err)
		}
		fmt.Printf("%-36s  %-20s  %-15s  %-15s  %-10s  %-20s  %-20s\n", "ID", "Name", "Daily Limit", "Monthly Limit", "RPM", "Max Req Tokens", "Concurrent")
		fmt.Println("----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------")
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
			maxReqTok := "unlimited"
			if g.MaxTokensPerRequest != nil {
				maxReqTok = strconv.FormatInt(*g.MaxTokensPerRequest, 10)
			}
			concurrent := "unlimited"
			if g.ConcurrentRequests != nil {
				concurrent = strconv.Itoa(*g.ConcurrentRequests)
			}
			fmt.Printf("%-36s  %-20s  %-15s  %-15s  %-10s  %-20s  %-20s\n", g.ID, g.Name, daily, monthly, rpm, maxReqTok, concurrent)
		}
		return nil
	},
}

// --- group set-quota ---

var (
	setQuotaDaily          int64
	setQuotaMonthly        int64
	setQuotaRPM            int
	setQuotaMaxReqTokens   int64
	setQuotaConcurrentReqs int
)

var adminGroupSetQuotaCmd = &cobra.Command{
	Use:   "set-quota <name>",
	Short: "Set token quota for a group",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_, groupRepo, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)
		grp, err := groupRepo.GetByName(name)
		if err != nil {
			return fmt.Errorf("lookup group: %w", err)
		}
		if grp == nil {
			return fmt.Errorf("group %q not found", name)
		}

		var daily, monthly *int64
		var rpm *int
		var maxReqTokens *int64
		var concurrentReqs *int
		if cmd.Flags().Changed("daily") {
			daily = &setQuotaDaily
		}
		if cmd.Flags().Changed("monthly") {
			monthly = &setQuotaMonthly
		}
		if cmd.Flags().Changed("rpm") {
			rpm = &setQuotaRPM
		}
		if cmd.Flags().Changed("max-tokens-per-request") {
			maxReqTokens = &setQuotaMaxReqTokens
		}
		if cmd.Flags().Changed("concurrent-requests") {
			concurrentReqs = &setQuotaConcurrentReqs
		}
		if err := groupRepo.SetQuota(grp.ID, daily, monthly, rpm, maxReqTokens, concurrentReqs); err != nil {
			return err
		}
		auditCLI(database, zap.NewNop(), "group.set_quota", name, fmt.Sprintf("daily=%v monthly=%v rpm=%v", daily, monthly, rpm))
		fmt.Printf("Quota updated for group %q\n", name)
		return nil
	},
}

func init() {
	adminGroupSetQuotaCmd.Flags().Int64Var(&setQuotaDaily, "daily", 0, "daily token limit (0 = remove limit)")
	adminGroupSetQuotaCmd.Flags().Int64Var(&setQuotaMonthly, "monthly", 0, "monthly token limit (0 = remove limit)")
	adminGroupSetQuotaCmd.Flags().IntVar(&setQuotaRPM, "rpm", 0, "max requests per minute (0 = remove limit)")
	adminGroupSetQuotaCmd.Flags().Int64Var(&setQuotaMaxReqTokens, "max-tokens-per-request", 0, "max max_tokens per request (0 = remove limit)")
	adminGroupSetQuotaCmd.Flags().IntVar(&setQuotaConcurrentReqs, "concurrent-requests", 0, "max concurrent requests per user (0 = remove limit)")
}

// --- group delete ---

var groupDeleteForce bool

var adminGroupDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a group",
	Long: `Delete a group by name.

If the group has members, the command fails unless --force is specified.
With --force, all members are automatically ungrouped before deletion.

Examples:
  sproxy admin group delete trial
  sproxy admin group delete trial --force`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, groupRepo, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		g, err := groupRepo.GetByName(args[0])
		if err != nil {
			return fmt.Errorf("lookup group: %w", err)
		}
		if g == nil {
			return fmt.Errorf("group %q not found", args[0])
		}
		if err := groupRepo.Delete(g.ID, groupDeleteForce); err != nil {
			return err
		}
		auditCLI(database, logger, "group.delete", args[0], fmt.Sprintf("force=%v", groupDeleteForce))
		fmt.Printf("Group %q deleted\n", args[0])
		return nil
	},
}

func init() {
	adminGroupDeleteCmd.Flags().BoolVar(&groupDeleteForce, "force", false, "ungroup members and delete")
}

// ---------------------------------------------------------------------------
// sproxy admin stats
// ---------------------------------------------------------------------------

var (
	statsUser   string
	statsDays   int
	statsFormat string
)

var adminStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show token usage statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		if statsFormat != "text" && statsFormat != "json" && statsFormat != "csv" {
			return fmt.Errorf("invalid format %q: must be text, json, or csv", statsFormat)
		}

		userRepo, _, usageRepo, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

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
			return printUserStats(os.Stdout, statsFormat, user.Username, user.ID, input, output, statsDays, from, now)
		}

		// 全局统计
		stats, err := usageRepo.GlobalSumTokens(from, now)
		if err != nil {
			return err
		}
		rows, err := usageRepo.UserStats(from, now, 10)
		if err != nil {
			return err
		}
		return printGlobalStats(os.Stdout, statsFormat, stats, rows, statsDays, from, now)
	},
}

func init() {
	adminStatsCmd.Flags().StringVar(&statsUser, "user", "", "filter by username")
	adminStatsCmd.Flags().IntVar(&statsDays, "days", 7, "number of days to include")
	adminStatsCmd.Flags().StringVar(&statsFormat, "format", "text", "output format: text|json|csv")
}

// printUserStats formats and writes per-user statistics to w in the requested format.
func printUserStats(w io.Writer, format, username, userID string, input, output int64, days int, from, to time.Time) error {
	total := input + output
	switch format {
	case "json":
		out := map[string]interface{}{
			"user":          username,
			"user_id":       userID,
			"period_days":   days,
			"from":          from.Format("2006-01-02"),
			"to":            to.Format("2006-01-02"),
			"input_tokens":  input,
			"output_tokens": output,
			"total_tokens":  total,
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	case "csv":
		fmt.Fprintln(w, "username,user_id,period_days,from,to,input_tokens,output_tokens,total_tokens")
		fmt.Fprintf(w, "%s,%s,%d,%s,%s,%d,%d,%d\n",
			username, userID, days,
			from.Format("2006-01-02"), to.Format("2006-01-02"),
			input, output, total)
	default: // text
		fmt.Fprintf(w, "User: %s (%s)\n", username, userID)
		fmt.Fprintf(w, "Period: last %d day(s) (%s ~ %s)\n", days,
			from.Format("2006-01-02"), to.Format("2006-01-02"))
		fmt.Fprintf(w, "Input tokens:  %d\n", input)
		fmt.Fprintf(w, "Output tokens: %d\n", output)
		fmt.Fprintf(w, "Total tokens:  %d\n", total)
	}
	return nil
}

// printGlobalStats formats and writes global statistics (including top-users) to w.
func printGlobalStats(w io.Writer, format string, stats db.GlobalStats, rows []db.UserStatRow, days int, from, to time.Time) error {
	type topUserEntry struct {
		UserID       string `json:"user_id"`
		InputTokens  int64  `json:"input_tokens"`
		OutputTokens int64  `json:"output_tokens"`
		TotalTokens  int64  `json:"total_tokens"`
	}
	switch format {
	case "json":
		topUsers := make([]topUserEntry, len(rows))
		for i, r := range rows {
			topUsers[i] = topUserEntry{
				UserID:       r.UserID,
				InputTokens:  r.TotalInput,
				OutputTokens: r.TotalOutput,
				TotalTokens:  r.TotalInput + r.TotalOutput,
			}
		}
		out := map[string]interface{}{
			"period_days":   days,
			"from":          from.Format("2006-01-02"),
			"to":            to.Format("2006-01-02"),
			"input_tokens":  stats.TotalInput,
			"output_tokens": stats.TotalOutput,
			"total_tokens":  stats.TotalTokens,
			"requests":      stats.RequestCount,
			"errors":        stats.ErrorCount,
			"top_users":     topUsers,
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	case "csv":
		fmt.Fprintln(w, "period_days,from,to,input_tokens,output_tokens,total_tokens,requests,errors")
		fmt.Fprintf(w, "%d,%s,%s,%d,%d,%d,%d,%d\n",
			days, from.Format("2006-01-02"), to.Format("2006-01-02"),
			stats.TotalInput, stats.TotalOutput, stats.TotalTokens,
			stats.RequestCount, stats.ErrorCount)
		if len(rows) > 0 {
			fmt.Fprintln(w, "user_id,input_tokens,output_tokens,total_tokens")
			for _, r := range rows {
				fmt.Fprintf(w, "%s,%d,%d,%d\n", r.UserID, r.TotalInput, r.TotalOutput, r.TotalInput+r.TotalOutput)
			}
		}
	default: // text
		fmt.Fprintf(w, "Period: last %d day(s) (%s ~ %s)\n", days,
			from.Format("2006-01-02"), to.Format("2006-01-02"))
		fmt.Fprintf(w, "Input tokens:   %d\n", stats.TotalInput)
		fmt.Fprintf(w, "Output tokens:  %d\n", stats.TotalOutput)
		fmt.Fprintf(w, "Total tokens:   %d\n", stats.TotalTokens)
		fmt.Fprintf(w, "Requests:       %d\n", stats.RequestCount)
		fmt.Fprintf(w, "Errors:         %d\n", stats.ErrorCount)
		if len(rows) > 0 {
			fmt.Fprintln(w, "\nTop users:")
			fmt.Fprintf(w, "  %-36s  %-12s\n", "User ID", "Total Tokens")
			for _, r := range rows {
				fmt.Fprintf(w, "  %-36s  %-12d\n", r.UserID, r.TotalInput+r.TotalOutput)
			}
		}
	}
	return nil
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
		userRepo, _, _, tokenRepo, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)
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
		auditCLI(database, zap.NewNop(), "token.revoke_all", username, "")
		fmt.Printf("All refresh tokens revoked for user %q\n", username)
		fmt.Println("Note: existing access tokens will expire within their TTL (up to 24h)")
		return nil
	},
}

// ---------------------------------------------------------------------------
// sproxy admin quota
// ---------------------------------------------------------------------------

var adminQuotaCmd = &cobra.Command{
	Use:   "quota",
	Short: "Inspect quota usage",
}

var (
	quotaStatusUser  string
	quotaStatusGroup string
)

var adminQuotaStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current quota usage vs limits for a user or group",
	Long: `Show today's and this month's token usage compared to the configured limits.

Examples:
  sproxy admin quota status --user alice
  sproxy admin quota status --group enterprise`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if quotaStatusUser == "" && quotaStatusGroup == "" {
			return fmt.Errorf("provide --user <username> or --group <name>")
		}
		userRepo, groupRepo, usageRepo, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		now := time.Now()
		todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

		if quotaStatusUser != "" {
			u, err := userRepo.GetByUsername(quotaStatusUser)
			if err != nil {
				return fmt.Errorf("lookup user: %w", err)
			}
			if u == nil {
				return fmt.Errorf("user %q not found", quotaStatusUser)
			}

			todayIn, todayOut, err := usageRepo.SumTokens(u.ID, todayStart, now)
			if err != nil {
				return err
			}
			monthIn, monthOut, err := usageRepo.SumTokens(u.ID, monthStart, now)
			if err != nil {
				return err
			}
			todayTotal := todayIn + todayOut
			monthTotal := monthIn + monthOut

			fmt.Printf("User: %s (%s)\n", u.Username, u.ID)
			if u.GroupID != nil {
				fmt.Printf("Group: %s\n", u.Group.Name)
			} else {
				fmt.Println("Group: (none)")
			}
			fmt.Println()

			var grp *db.Group
			if u.GroupID != nil {
				grp, _ = groupRepo.GetByID(*u.GroupID)
			}
			printQuotaRow("Today  (tokens)", todayTotal, ptrInt64Val(grp, "daily"))
			printQuotaRow("Month  (tokens)", monthTotal, ptrInt64Val(grp, "monthly"))
			return nil
		}

		// 分组统计
		g, err := groupRepo.GetByName(quotaStatusGroup)
		if err != nil {
			return fmt.Errorf("lookup group: %w", err)
		}
		if g == nil {
			return fmt.Errorf("group %q not found", quotaStatusGroup)
		}
		members, err := userRepo.ListByGroup(g.ID)
		if err != nil {
			return err
		}

		var todayTotal, monthTotal int64
		for _, m := range members {
			ti, to, _ := usageRepo.SumTokens(m.ID, todayStart, now)
			mi, mo, _ := usageRepo.SumTokens(m.ID, monthStart, now)
			todayTotal += ti + to
			monthTotal += mi + mo
		}

		fmt.Printf("Group: %s (%d member(s))\n\n", g.Name, len(members))
		printQuotaRow("Today  (tokens)", todayTotal, ptrInt64(g.DailyTokenLimit))
		printQuotaRow("Month  (tokens)", monthTotal, ptrInt64(g.MonthlyTokenLimit))
		if g.RequestsPerMinute != nil {
			fmt.Printf("RPM limit:         %d req/min\n", *g.RequestsPerMinute)
		} else {
			fmt.Println("RPM limit:         unlimited")
		}
		return nil
	},
}

// ptrInt64Val extracts the appropriate limit field from a Group (nil group = unlimited).
func ptrInt64Val(g *db.Group, which string) int64 {
	if g == nil {
		return 0
	}
	switch which {
	case "daily":
		return ptrInt64(g.DailyTokenLimit)
	case "monthly":
		return ptrInt64(g.MonthlyTokenLimit)
	}
	return 0
}

func ptrInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func printQuotaRow(label string, used, limit int64) {
	if limit <= 0 {
		fmt.Printf("%-20s  used=%-12d  limit=unlimited\n", label, used)
		return
	}
	pct := float64(used) * 100 / float64(limit)
	status := "OK"
	if pct >= 100 {
		status = "EXCEEDED"
	} else if pct >= 80 {
		status = "WARNING"
	}
	fmt.Printf("%-20s  used=%-12d  limit=%-12d  %.1f%%  [%s]\n", label, used, limit, pct, status)
}

func init() {
	adminQuotaCmd.AddCommand(adminQuotaStatusCmd)
	adminQuotaStatusCmd.Flags().StringVar(&quotaStatusUser, "user", "", "username to inspect")
	adminQuotaStatusCmd.Flags().StringVar(&quotaStatusGroup, "group", "", "group name to inspect")
}

// ---------------------------------------------------------------------------
// sproxy admin backup
// ---------------------------------------------------------------------------

var adminBackupOutput string

var adminBackupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Copy the SQLite database to a backup file",
	RunE: func(cmd *cobra.Command, args []string) error {
		logger, err := zap.NewProduction()
		if err != nil {
			return fmt.Errorf("init logger: %w", err)
		}
		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, _, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		src := cfg.Database.Path
		if src == "" {
			return fmt.Errorf("database.path is not set in config")
		}

		dst := adminBackupOutput
		if dst == "" {
			dst = src + ".bak"
		}

		logger.Info("starting database backup",
			zap.String("src", src),
			zap.String("dst", dst),
		)

		in, err := os.Open(src)
		if err != nil {
			return fmt.Errorf("open source database: %w", err)
		}
		defer in.Close()

		out, err := os.Create(dst)
		if err != nil {
			return fmt.Errorf("create backup file: %w", err)
		}
		defer out.Close()

		if _, err := io.Copy(out, in); err != nil {
			return fmt.Errorf("copy database: %w", err)
		}

		fmt.Printf("Backup created: %s\n", dst)
		logger.Info("database backup complete", zap.String("dst", dst))
		return nil
	},
}

func init() {
	adminBackupCmd.Flags().StringVar(&adminBackupOutput, "output", "", "backup file path (default: <db-path>.bak)")
}

// ---------------------------------------------------------------------------
// sproxy admin restore
// ---------------------------------------------------------------------------

var adminRestoreCmd = &cobra.Command{
	Use:   "restore <backup-file>",
	Short: "Restore the database from a backup file",
	Long: `Replace the current database with a backup copy.

WARNING: This overwrites the live database. Ensure sproxy is not running.

Example:
  sproxy admin restore pairproxy.db.bak`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		logger, err := zap.NewProduction()
		if err != nil {
			return fmt.Errorf("init logger: %w", err)
		}
		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, _, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		dst := cfg.Database.Path
		if dst == "" {
			return fmt.Errorf("database.path is not set in config")
		}
		src := args[0]
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("backup file not found: %w", err)
		}

		// 创建当前 DB 的安全备份
		safeBak := dst + ".pre-restore"
		if _, err := os.Stat(dst); err == nil {
			in, err := os.Open(dst)
			if err != nil {
				return fmt.Errorf("open current database for backup: %w", err)
			}
			out, err := os.Create(safeBak)
			if err != nil {
				in.Close()
				return fmt.Errorf("create pre-restore backup file: %w", err)
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				in.Close()
				os.Remove(safeBak)
				return fmt.Errorf("copy database to backup: %w", err)
			}
			if err := out.Close(); err != nil {
				in.Close()
				return fmt.Errorf("close backup file: %w", err)
			}
			in.Close()
			logger.Info("pre-restore backup saved", zap.String("path", safeBak))
		}

		in, err := os.Open(src)
		if err != nil {
			return fmt.Errorf("open backup: %w", err)
		}
		defer in.Close()

		out, err := os.Create(dst)
		if err != nil {
			return fmt.Errorf("overwrite database: %w", err)
		}

		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return fmt.Errorf("copy backup to database: %w", err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("flush restored database: %w", err)
		}
		fmt.Printf("Database restored from: %s\n", src)
		fmt.Printf("(Previous database saved to: %s)\n", safeBak)
		logger.Info("database restored", zap.String("src", src), zap.String("dst", dst))
		return nil
	},
}

// ---------------------------------------------------------------------------
// sproxy admin logs
// ---------------------------------------------------------------------------

var adminLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Manage usage logs",
}

var logsPurgeBefore string

var adminLogsPurgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Delete usage logs older than a given date",
	Long: `Delete usage log records with created_at older than the specified date.

Examples:
  sproxy admin logs purge --before 2025-01-01
  sproxy admin logs purge --days 90`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, usageRepo, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		var before time.Time
		if logsPurgeBefore != "" {
			before, err = time.Parse("2006-01-02", logsPurgeBefore)
			if err != nil {
				return fmt.Errorf("invalid date %q: expected YYYY-MM-DD", logsPurgeBefore)
			}
		} else if cmd.Flags().Changed("days") {
			days, _ := cmd.Flags().GetInt("days")
			before = time.Now().AddDate(0, 0, -days).Truncate(24 * time.Hour)
		} else {
			return fmt.Errorf("provide --before <YYYY-MM-DD> or --days <n>")
		}

		deleted, err := usageRepo.DeleteBefore(before)
		if err != nil {
			return err
		}
		fmt.Printf("Deleted %d usage log record(s) older than %s\n", deleted, before.Format("2006-01-02"))
		return nil
	},
}

func init() {
	adminLogsCmd.AddCommand(adminLogsPurgeCmd)
	adminLogsPurgeCmd.Flags().StringVar(&logsPurgeBefore, "before", "", "delete records before this date (YYYY-MM-DD)")
	adminLogsPurgeCmd.Flags().Int("days", 0, "delete records older than N days")
}

// ---------------------------------------------------------------------------
// sproxy admin export
// ---------------------------------------------------------------------------

var (
	exportFormat string
	exportFrom   string
	exportTo     string
	exportOutput string
)

var adminExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export usage logs to CSV or JSON file",
	Long: `Export usage logs from the SQLite database to a CSV or JSON file.

Time range defaults to the last 30 days when --from/--to are not specified.

Examples:
  sproxy admin export --format csv --output logs.csv
  sproxy admin export --format json --from 2024-01-01 --to 2024-01-31 --output jan.json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if exportFormat != "csv" && exportFormat != "json" {
			return fmt.Errorf("invalid format %q: must be csv or json", exportFormat)
		}

		_, _, usageRepo, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		now := time.Now().UTC()
		from := now.AddDate(0, 0, -30).Truncate(24 * time.Hour)
		to := now

		if exportFrom != "" {
			t, perr := time.Parse("2006-01-02", exportFrom)
			if perr != nil {
				return fmt.Errorf("invalid --from date (expected YYYY-MM-DD): %w", perr)
			}
			from = t.UTC()
		}
		if exportTo != "" {
			t, perr := time.Parse("2006-01-02", exportTo)
			if perr != nil {
				return fmt.Errorf("invalid --to date (expected YYYY-MM-DD): %w", perr)
			}
			to = t.UTC().Add(24*time.Hour - time.Nanosecond)
		}

		// 确定输出目标
		var out io.Writer = os.Stdout
		var outFile *os.File
		if exportOutput != "" {
			outFile, err = os.Create(exportOutput)
			if err != nil {
				return fmt.Errorf("create output file: %w", err)
			}
			defer outFile.Close()
			out = outFile
		}

		logger.Info("exporting usage logs",
			zap.String("format", exportFormat),
			zap.Time("from", from),
			zap.Time("to", to),
			zap.String("output", exportOutput),
		)

		exported := 0
		var exportErr error

		if exportFormat == "csv" {
			// UTF-8 BOM for Excel compatibility
			if _, err := fmt.Fprint(out, "\xEF\xBB\xBF"); err != nil {
				return fmt.Errorf("write BOM: %w", err)
			}
			cw := csv.NewWriter(out)
			headers := []string{
				"id", "request_id", "user_id", "model",
				"input_tokens", "output_tokens", "total_tokens",
				"is_streaming", "status_code", "duration_ms",
				"cost_usd", "source_node", "upstream_url", "created_at",
			}
			if err := cw.Write(headers); err != nil {
				return fmt.Errorf("write CSV header: %w", err)
			}
			exportErr = usageRepo.ExportLogs(from, to, func(l db.UsageLog) error {
				row := []string{
					strconv.FormatUint(uint64(l.ID), 10),
					l.RequestID, l.UserID, l.Model,
					strconv.Itoa(l.InputTokens),
					strconv.Itoa(l.OutputTokens),
					strconv.Itoa(l.TotalTokens),
					strconv.FormatBool(l.IsStreaming),
					strconv.Itoa(l.StatusCode),
					strconv.FormatInt(l.DurationMs, 10),
					fmt.Sprintf("%.6f", l.CostUSD),
					l.SourceNode, l.UpstreamURL,
					l.CreatedAt.UTC().Format(time.RFC3339),
				}
				exported++
				return cw.Write(row)
			})
			cw.Flush()
			if cw.Error() != nil && exportErr == nil {
				exportErr = cw.Error()
			}
		} else {
			// NDJSON
			enc := json.NewEncoder(out)
			exportErr = usageRepo.ExportLogs(from, to, func(l db.UsageLog) error {
				exported++
				return enc.Encode(map[string]interface{}{
					"id": l.ID, "request_id": l.RequestID,
					"user_id": l.UserID, "model": l.Model,
					"input_tokens": l.InputTokens, "output_tokens": l.OutputTokens,
					"total_tokens": l.TotalTokens, "is_streaming": l.IsStreaming,
					"status_code": l.StatusCode, "duration_ms": l.DurationMs,
					"cost_usd": l.CostUSD, "source_node": l.SourceNode,
					"upstream_url": l.UpstreamURL,
					"created_at":   l.CreatedAt.UTC().Format(time.RFC3339),
				})
			})
		}

		if exportErr != nil {
			return fmt.Errorf("export failed after %d rows: %w", exported, exportErr)
		}

		dest := exportOutput
		if dest == "" {
			dest = "stdout"
		}
		fmt.Fprintf(os.Stderr, "Exported %d rows (%s → %s)\n", exported, exportFormat, dest)
		return nil
	},
}

func init() {
	adminExportCmd.Flags().StringVar(&exportFormat, "format", "csv", "output format: csv|json")
	adminExportCmd.Flags().StringVar(&exportFrom, "from", "", "start date YYYY-MM-DD (default: 30 days ago)")
	adminExportCmd.Flags().StringVar(&exportTo, "to", "", "end date YYYY-MM-DD (default: today)")
	adminExportCmd.Flags().StringVar(&exportOutput, "output", "", "output file path (default: stdout)")
}

// ---------------------------------------------------------------------------
// 日志级别辅助函数（支持 SIGHUP 热重载）
// ---------------------------------------------------------------------------

// wrapOtelHTTP 用 otelhttp.NewHandler 包装 handler，为每个 HTTP 请求创建 OTel span。
// 仅在 cfg.Telemetry.Enabled=true 时调用；disabled 时 sp.Handler() 直接注册，无开销。
func wrapOtelHTTP(h http.Handler, operation string) http.Handler {
	return otelhttp.NewHandler(h, operation)
}

// buildLogger 使用给定的 AtomicLevel 构建一个结构化 JSON logger。
// AtomicLevel 允许在运行时（例如通过 SIGHUP）动态修改日志级别。
func buildLogger(atom zap.AtomicLevel) *zap.Logger {
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.TimeKey = "ts"
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.AddSync(os.Stderr),
		atom,
	)
	return zap.New(core, zap.AddCaller())
}

// buildDebugFileLogger 创建写入独立文件的 DEBUG 级日志器，用于转发内容记录。
// 使用 JSON 格式，DEBUG 级别（不受主日志 level 限制），适合高频写入。
func buildDebugFileLogger(path string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zapcore.DebugLevel)
	cfg.OutputPaths = []string{path}
	cfg.ErrorOutputPaths = []string{path}
	cfg.EncoderConfig.TimeKey = "ts"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	return cfg.Build()
}

// parseZapLevel 将配置文件中的 log.level 字符串转换为 zapcore.Level。
// 未知字符串默认返回 InfoLevel。
func parseZapLevel(level string) zapcore.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return zapcore.DebugLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
}

// ---------------------------------------------------------------------------
// sproxy admin config validate
// ---------------------------------------------------------------------------

var adminConfigValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate sproxy.yaml configuration file",
	Long: `Load and validate the sproxy configuration file, reporting any errors or warnings.

Examples:
  sproxy admin config validate
  sproxy admin config validate --config /etc/pairproxy/sproxy.yaml`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, warns, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			fmt.Printf("Config file: %s\n\n", cfgPath)
			fmt.Printf("✗ FAILED: %v\n", err)
			return err
		}

		fmt.Printf("Config file: %s\n", cfgPath)
		if len(warns) > 0 {
			fmt.Printf("\nWarnings (%d):\n", len(warns))
			for _, w := range warns {
				fmt.Printf("  ⚠  %s\n", w)
			}
		}

		if err := cfg.Validate(); err != nil {
			fmt.Printf("\n✗ Validation failed: %v\n", err)
			return err
		}

		fmt.Printf("\nEffective configuration:\n")
		fmt.Printf("  Listen:          %s\n", cfg.Listen.Addr())
		fmt.Printf("  Database:        %s\n", cfg.Database.Path)
		fmt.Printf("  LLM targets:     %d\n", len(cfg.LLM.Targets))
		fmt.Printf("  Max retries:     %d\n", cfg.LLM.MaxRetries)
		fmt.Printf("  Recovery delay:  %s\n", cfg.LLM.RecoveryDelay)
		fmt.Printf("  Dashboard:       %v\n", cfg.Dashboard.Enabled)
		fmt.Printf("  Telemetry:       %v\n", cfg.Telemetry.Enabled)
		clusterMode := "standalone"
		if cfg.Cluster.Role == "primary" || cfg.Cluster.Role == "worker" {
			clusterMode = cfg.Cluster.Role
		}
		fmt.Printf("  Cluster role:    %s\n", clusterMode)
		fmt.Printf("\n✓ All checks passed\n")
		return nil
	},
}

func init() {
	adminCmd.AddCommand(adminConfigValidateCmd)
}

// ---------------------------------------------------------------------------
// sproxy admin audit
// ---------------------------------------------------------------------------

var adminAuditLimit int

var adminAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Show recent admin operation audit log",
	Long: `Display the most recent entries from the admin audit log.
All operations performed via the Dashboard or REST API are recorded here.

Examples:
  sproxy admin audit
  sproxy admin audit --limit 50`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		auditRepo := db.NewAuditRepo(logger, database)
		logs, err := auditRepo.ListRecent(adminAuditLimit)
		if err != nil {
			return err
		}
		if len(logs) == 0 {
			fmt.Println("No audit records found.")
			return nil
		}
		fmt.Printf("%-20s  %-12s  %-30s  %-30s  %s\n", "TIME", "OPERATOR", "ACTION", "TARGET", "DETAIL")
		fmt.Println(strings.Repeat("-", 110))
		for _, l := range logs {
			detail := l.Detail
			if len(detail) > 40 {
				detail = detail[:37] + "..."
			}
			fmt.Printf("%-20s  %-12s  %-30s  %-30s  %s\n",
				l.CreatedAt.Format("2006-01-02 15:04:05"),
				l.Operator,
				l.Action,
				l.Target,
				detail,
			)
		}
		return nil
	},
}

func init() {
	adminAuditCmd.Flags().IntVar(&adminAuditLimit, "limit", 100, "max number of records to show")
}

// ---------------------------------------------------------------------------
// sproxy admin apikey
// ---------------------------------------------------------------------------

// openAdminConfig 加载配置并打开 DB，另外返回 config（供 apikey 命令使用）。
func openAdminConfig() (*config.SProxyFullConfig, *db.APIKeyRepo, *zap.Logger, *gorm.DB, error) {
	logger, err := zap.NewProduction()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("init logger: %w", err)
	}
	cfgPath := adminConfigFlag
	if cfgPath == "" {
		cfgPath = "sproxy.yaml"
	}
	cfg, _, err := config.LoadSProxyConfig(cfgPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("load config from %q: %w", cfgPath, err)
	}
	database, err := db.OpenWithConfig(logger, cfg.Database)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("open database: %w", err)
	}
	if err := db.Migrate(logger, database); err != nil {
		closeGormDB(logger, database)
		return nil, nil, nil, nil, fmt.Errorf("migrate database: %w", err)
	}
	return cfg, db.NewAPIKeyRepo(database, logger), logger, database, nil
}

var adminApikeyCmd = &cobra.Command{
	Use:   "apikey",
	Short: "Manage API keys",
}

// --- apikey add ---

var (
	apikeyAddValue    string
	apikeyAddProvider string
)

var adminApikeyAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Add a new API key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, repo, logger, database, err := openAdminConfig()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)
		if cfg.Admin.KeyEncryptionKey == "" {
			return fmt.Errorf("admin.key_encryption_key is not configured in sproxy.yaml")
		}
		value := apikeyAddValue
		if value == "" {
			value, err = readPassword("API Key value: ")
			if err != nil {
				return fmt.Errorf("read api key: %w", err)
			}
		}
		encrypted, err := auth.Encrypt(value, cfg.Admin.KeyEncryptionKey)
		if err != nil {
			return fmt.Errorf("encrypt key: %w", err)
		}
		key, err := repo.Create(name, encrypted, apikeyAddProvider)
		if err != nil {
			return fmt.Errorf("create api key: %w", err)
		}
		fmt.Printf("API key %q created (id: %s, provider: %s)\n", name, key.ID, key.Provider)
		return nil
	},
}

func init() {
	adminApikeyAddCmd.Flags().StringVar(&apikeyAddValue, "value", "", "API key value (omit to read from prompt)")
	adminApikeyAddCmd.Flags().StringVar(&apikeyAddProvider, "provider", "anthropic", "provider: anthropic|openai|ollama")
}

// --- apikey list ---

var adminApikeyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List API keys",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, repo, logger, database, err := openAdminConfig()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)
		keys, err := repo.List()
		if err != nil {
			return fmt.Errorf("list api keys: %w", err)
		}
		fmt.Printf("%-36s  %-20s  %-12s  %-8s  %s\n", "ID", "Name", "Provider", "Active", "Created")
		fmt.Println("-------------------------------------------------------------------------------------------")
		for _, k := range keys {
			active := "yes"
			if !k.IsActive {
				active = "no"
			}
			fmt.Printf("%-36s  %-20s  %-12s  %-8s  %s\n",
				k.ID, k.Name, k.Provider, active, k.CreatedAt.Format("2006-01-02"))
		}
		return nil
	},
}

// --- apikey assign ---

var (
	apikeyAssignUser  string
	apikeyAssignGroup string
)

var adminApikeyAssignCmd = &cobra.Command{
	Use:   "assign <name>",
	Short: "Assign an API key to a user or group",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if apikeyAssignUser == "" && apikeyAssignGroup == "" {
			return fmt.Errorf("--user or --group is required")
		}
		_, repo, logger, database, err := openAdminConfig()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)
		key, err := repo.GetByName(name)
		if err != nil {
			return fmt.Errorf("lookup api key: %w", err)
		}
		if key == nil {
			return fmt.Errorf("api key %q not found", name)
		}
		var userID, groupID *string
		if apikeyAssignUser != "" {
			userID = &apikeyAssignUser
		}
		if apikeyAssignGroup != "" {
			groupID = &apikeyAssignGroup
		}
		if err := repo.Assign(key.ID, userID, groupID); err != nil {
			return fmt.Errorf("assign api key: %w", err)
		}
		fmt.Printf("API key %q assigned\n", name)
		return nil
	},
}

func init() {
	adminApikeyAssignCmd.Flags().StringVar(&apikeyAssignUser, "user", "", "user ID to assign to")
	adminApikeyAssignCmd.Flags().StringVar(&apikeyAssignGroup, "group", "", "group ID to assign to")
}

// --- apikey revoke ---

var adminApikeyRevokeCmd = &cobra.Command{
	Use:   "revoke <name>",
	Short: "Revoke (deactivate) an API key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		_, repo, logger, database, err := openAdminConfig()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)
		key, err := repo.GetByName(name)
		if err != nil {
			return fmt.Errorf("lookup api key: %w", err)
		}
		if key == nil {
			return fmt.Errorf("api key %q not found", name)
		}
		if err := repo.Revoke(key.ID); err != nil {
			return fmt.Errorf("revoke api key: %w", err)
		}
		fmt.Printf("API key %q revoked\n", name)
		return nil
	},
}

func init() {
	adminApikeyCmd.AddCommand(adminApikeyAddCmd, adminApikeyListCmd, adminApikeyAssignCmd, adminApikeyRevokeCmd)
}

// ---------------------------------------------------------------------------
// admin llm — LLM binding 子命令
// ---------------------------------------------------------------------------

var adminLLMCmd = &cobra.Command{
	Use:   "llm",
	Short: "Manage LLM target bindings",
}

// --- llm targets ---

var adminLLMTargetsCmd = &cobra.Command{
	Use:   "targets",
	Short: "List all configured LLM targets (reads from config; no live health info in CLI mode)",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, _, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		llmBindingRepo := db.NewLLMBindingRepo(database, logger)
		bindings, err := llmBindingRepo.List()
		if err != nil {
			return fmt.Errorf("list bindings: %w", err)
		}
		boundCount := map[string]int{}
		for _, b := range bindings {
			boundCount[b.TargetURL]++
		}

		fmt.Printf("%-50s %-10s %-8s %s\n", "TARGET URL", "PROVIDER", "WEIGHT", "BOUND_USERS")
		for _, t := range cfg.LLM.Targets {
			w := t.Weight
			if w <= 0 {
				w = 1
			}
			prov := t.Provider
			if prov == "" {
				prov = "anthropic"
			}
			fmt.Printf("%-50s %-10s %-8d %d\n", t.URL, prov, w, boundCount[t.URL])
		}
		return nil
	},
}

// --- llm bind ---

var (
	llmBindTarget string
	llmBindGroup  string
)

var adminLLMBindCmd = &cobra.Command{
	Use:   "bind <username-or-user-id>",
	Short: "Bind a user (or group) to a specific LLM target",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		userRepo, _, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		if llmBindTarget == "" {
			return fmt.Errorf("--target is required")
		}
		llmBindingRepo := db.NewLLMBindingRepo(database, logger)

		if llmBindGroup != "" {
			// 分组绑定
			groupRepo := db.NewGroupRepo(database, logger)
			g, err := groupRepo.GetByName(llmBindGroup)
			if err != nil || g == nil {
				return fmt.Errorf("group %q not found", llmBindGroup)
			}
			if err := llmBindingRepo.Set(llmBindTarget, nil, &g.ID); err != nil {
				return fmt.Errorf("bind group: %w", err)
			}
			auditCLI(database, logger, "llm.bind_group", llmBindGroup, "target="+llmBindTarget)
			fmt.Printf("Group %q bound to %s\n", llmBindGroup, llmBindTarget)
			return nil
		}

		if len(args) == 0 {
			return fmt.Errorf("username or --group is required")
		}
		// 用户绑定
		u, err := userRepo.GetByUsername(args[0])
		if err != nil || u == nil {
			return fmt.Errorf("user %q not found", args[0])
		}
		if err := llmBindingRepo.Set(llmBindTarget, &u.ID, nil); err != nil {
			return fmt.Errorf("bind user: %w", err)
		}
		auditCLI(database, logger, "llm.bind_user", args[0], "target="+llmBindTarget)
		fmt.Printf("User %q bound to %s\n", args[0], llmBindTarget)
		return nil
	},
}

func init() {
	adminLLMBindCmd.Flags().StringVar(&llmBindTarget, "target", "", "LLM target URL to bind to")
	adminLLMBindCmd.Flags().StringVar(&llmBindGroup, "group", "", "group name (instead of user)")
}

// --- llm unbind ---

var adminLLMUnbindCmd = &cobra.Command{
	Use:   "unbind <username>",
	Short: "Remove user-level LLM binding",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		userRepo, _, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		u, err := userRepo.GetByUsername(args[0])
		if err != nil || u == nil {
			return fmt.Errorf("user %q not found", args[0])
		}
		llmBindingRepo := db.NewLLMBindingRepo(database, logger)
		bindings, err := llmBindingRepo.List()
		if err != nil {
			return fmt.Errorf("list bindings: %w", err)
		}
		deleted := 0
		for _, b := range bindings {
			if b.UserID != nil && *b.UserID == u.ID {
				if err := llmBindingRepo.Delete(b.ID); err != nil {
					return fmt.Errorf("delete binding: %w", err)
				}
				deleted++
			}
		}
		if deleted == 0 {
			fmt.Printf("No binding found for user %q\n", args[0])
		} else {
			auditCLI(database, logger, "llm.unbind_user", args[0], fmt.Sprintf("removed=%d", deleted))
			fmt.Printf("Removed %d binding(s) for user %q\n", deleted, args[0])
		}
		return nil
	},
}

// --- llm distribute ---

var adminLLMDistributeCmd = &cobra.Command{
	Use:   "distribute",
	Short: "Evenly distribute all active users across LLM targets (round-robin)",
	RunE: func(cmd *cobra.Command, args []string) error {
		userRepo, _, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, _, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		var targetURLs []string
		for _, t := range cfg.LLM.Targets {
			targetURLs = append(targetURLs, t.URL)
		}
		if len(targetURLs) == 0 {
			return fmt.Errorf("no LLM targets configured in %s", cfgPath)
		}

		users, err := userRepo.ListByGroup("")
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		var userIDs []string
		for _, u := range users {
			if u.IsActive {
				userIDs = append(userIDs, u.ID)
			}
		}

		llmBindingRepo := db.NewLLMBindingRepo(database, logger)
		if err := llmBindingRepo.EvenDistribute(userIDs, targetURLs); err != nil {
			return fmt.Errorf("distribute: %w", err)
		}
		auditCLI(database, logger, "llm.distribute", "all", fmt.Sprintf("users=%d targets=%d", len(userIDs), len(targetURLs)))
		fmt.Printf("Distributed %d user(s) across %d target(s)\n", len(userIDs), len(targetURLs))
		return nil
	},
}

// --- llm list ---

var adminLLMListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all LLM bindings",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, database, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		llmBindingRepo := db.NewLLMBindingRepo(database, logger)
		bindings, err := llmBindingRepo.List()
		if err != nil {
			return fmt.Errorf("list: %w", err)
		}
		if len(bindings) == 0 {
			fmt.Println("No LLM bindings configured.")
			return nil
		}
		fmt.Printf("%-36s %-15s %-30s %s\n", "ID", "TYPE", "SUBJECT", "TARGET URL")
		for _, b := range bindings {
			bindType := "group"
			subject := ""
			if b.UserID != nil {
				bindType = "user"
				subject = *b.UserID
			} else if b.GroupID != nil {
				subject = *b.GroupID
			}
			fmt.Printf("%-36s %-15s %-30s %s\n", b.ID, bindType, subject, b.TargetURL)
		}
		return nil
	},
}

func init() {
	adminLLMCmd.AddCommand(adminLLMTargetsCmd, adminLLMBindCmd, adminLLMUnbindCmd, adminLLMDistributeCmd, adminLLMListCmd)
}

// ---------------------------------------------------------------------------
// admin drain — 排水控制子命令
// ---------------------------------------------------------------------------

var adminDrainCmd = &cobra.Command{
	Use:   "drain",
	Short: "Manage drain mode for graceful rolling upgrades",
	Long: `Drain mode allows graceful shutdown of a node for rolling upgrades.

When a node is in drain mode:
- It stops accepting new requests from the load balancer
- Existing requests continue to be processed
- The node can be safely stopped once active requests reach zero

This enables zero-downtime rolling upgrades in multi-node clusters.`,
}

// --- drain (enter drain mode) ---

var adminDrainEnterCmd = &cobra.Command{
	Use:   "enter",
	Short: "Enter drain mode (stop accepting new traffic)",
	Long: `Put the local node into drain mode.

The node will:
1. Stop accepting new requests from the load balancer
2. Continue processing existing requests
3. Update the cluster routing table to notify other nodes

Use "sproxy admin drain status" to monitor active requests.
Use "sproxy admin drain wait" to block until all requests complete.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, _, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		// 调用本地 drain API
		addr := cfg.Listen.Addr()
		url := fmt.Sprintf("http://%s/api/admin/drain", addr)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		// 使用 admin JWT 认证
		token, err := createAdminToken(cfg)
		if err != nil {
			return fmt.Errorf("create admin token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("drain request failed: %w (is sproxy running?)", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("drain failed: %s", string(body))
		}

		fmt.Println("Node entered drain mode.")
		fmt.Println("Use 'sproxy admin drain status' to monitor active requests.")
		return nil
	},
}

// --- drain exit (undrain) ---

var adminDrainExitCmd = &cobra.Command{
	Use:   "exit",
	Short: "Exit drain mode (resume accepting traffic)",
	Long: `Return the local node to normal operation.

The node will resume accepting new requests from the load balancer.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, _, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		addr := cfg.Listen.Addr()
		url := fmt.Sprintf("http://%s/api/admin/undrain", addr)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		token, err := createAdminToken(cfg)
		if err != nil {
			return fmt.Errorf("create admin token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("undrain request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("undrain failed: %s", string(body))
		}

		fmt.Println("Node exited drain mode and is now accepting traffic.")
		return nil
	},
}

// --- drain status ---

var adminDrainStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show drain status and active request count",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, _, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		addr := cfg.Listen.Addr()
		url := fmt.Sprintf("http://%s/api/admin/drain/status", addr)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		token, err := createAdminToken(cfg)
		if err != nil {
			return fmt.Errorf("create admin token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("status request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("status failed: %s", string(body))
		}

		var status struct {
			Draining       bool   `json:"draining"`
			ActiveRequests int64  `json:"active_requests"`
			DrainStarted   string `json:"drain_started,omitempty"`
			DrainReason    string `json:"drain_reason,omitempty"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		if status.Draining {
			fmt.Printf("Status: DRAINING\n")
			fmt.Printf("Active requests: %d\n", status.ActiveRequests)
			if status.DrainStarted != "" {
				fmt.Printf("Drain started: %s\n", status.DrainStarted)
			}
			if status.DrainReason != "" {
				fmt.Printf("Reason: %s\n", status.DrainReason)
			}
		} else {
			fmt.Printf("Status: NORMAL\n")
			fmt.Printf("Active requests: %d\n", status.ActiveRequests)
		}
		return nil
	},
}

// --- drain wait ---

var drainWaitTimeout time.Duration

var adminDrainWaitCmd = &cobra.Command{
	Use:   "wait",
	Short: "Wait until all active requests complete",
	Long: `Block until the number of active requests reaches zero.

This is useful for rolling upgrades:
1. Enter drain mode: sproxy admin drain enter
2. Wait for drain: sproxy admin drain wait --timeout 60s
3. Stop the node: systemctl stop sproxy
4. Upgrade and restart`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfgPath := adminConfigFlag
		if cfgPath == "" {
			cfgPath = "sproxy.yaml"
		}
		cfg, _, err := config.LoadSProxyConfig(cfgPath)
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}

		addr := cfg.Listen.Addr()
		baseURL := fmt.Sprintf("http://%s/api/admin/drain", addr)

		ctx := context.Background()
		if drainWaitTimeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, drainWaitTimeout)
			defer cancel()
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		fmt.Println("Waiting for active requests to reach zero...")
		start := time.Now()

		for {
			select {
			case <-ctx.Done():
				return fmt.Errorf("timeout waiting for drain (waited %v)", time.Since(start))
			case <-ticker.C:
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/status", nil)
				if err != nil {
					return fmt.Errorf("create request: %w", err)
				}

				token, err := createAdminToken(cfg)
				if err != nil {
					return fmt.Errorf("create admin token: %w", err)
				}
				req.Header.Set("Authorization", "Bearer "+token)

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					fmt.Printf("Warning: status check failed: %v\n", err)
					continue
				}

				var status struct {
					ActiveRequests int64 `json:"active_requests"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
					resp.Body.Close()
					fmt.Printf("Warning: failed to decode drain status response: %v\n", err)
					continue
				}
				resp.Body.Close()

				if status.ActiveRequests == 0 {
					fmt.Printf("Drain complete! All requests finished (waited %v)\n", time.Since(start))
					return nil
				}
				fmt.Printf("\rActive requests: %d (waiting...)", status.ActiveRequests)
			}
		}
	},
}

func init() {
	adminDrainWaitCmd.Flags().DurationVar(&drainWaitTimeout, "timeout", 0, "maximum time to wait (0 = no limit)")
	adminDrainCmd.AddCommand(adminDrainEnterCmd, adminDrainExitCmd, adminDrainStatusCmd, adminDrainWaitCmd)
}

// createAdminToken creates a short-lived admin JWT for CLI-to-API authentication.
func createAdminToken(cfg *config.SProxyFullConfig) (string, error) {
	jwtMgr, err := auth.NewManager(zap.NewNop(), cfg.Auth.JWTSecret)
	if err != nil {
		return "", fmt.Errorf("create JWT manager: %w", err)
	}
	token, err := jwtMgr.Sign(auth.JWTClaims{
		UserID:   "__admin__",
		Username: "admin",
		Role:     "admin",
	}, 5*time.Minute)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return token, nil
}
