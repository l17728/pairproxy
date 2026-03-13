package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/config"
	"github.com/l17728/pairproxy/internal/db"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// sproxy admin model — 模型路由管理
// ---------------------------------------------------------------------------

var adminModelCmd = &cobra.Command{
	Use:   "model",
	Short: "管理模型路由条目",
	Long: `管理 LLM Target 的模型条目，用于按模型 ID 路由请求到指定 target。

模型路由优先级（低于用户/分组绑定，高于加权负载均衡）：
  用户绑定 > 分组绑定 > 模型路由 > 加权随机负载均衡`,
}

// openModelAdminDB 加载配置并打开数据库，返回 cfg + modelRepo + logger + gormDB。
func openModelAdminDB() (*config.SProxyFullConfig, *db.LLMTargetModelRepo, *zap.Logger, *gorm.DB, error) {
	cfg, _, logger, database, err := openAdminConfig()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return cfg, db.NewLLMTargetModelRepo(database, logger), logger, database, nil
}

// ---------------------------------------------------------------------------
// sproxy admin model list
// ---------------------------------------------------------------------------

var adminModelListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出所有模型路由条目",
	Long:  "列出数据库中所有活跃的模型路由条目（含模型 ID、别名、所属 target、上游名称）。",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, repo, _, database, err := openModelAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(zap.NewNop(), database)

		models, err := repo.ListAll()
		if err != nil {
			return fmt.Errorf("list models: %w", err)
		}

		if len(models) == 0 {
			fmt.Println("（无模型路由条目）")
			fmt.Println()
			fmt.Println("提示：在 sproxy.yaml 的 llm.targets[*].models 字段添加模型声明，重启后自动同步。")
			return nil
		}

		// 表头
		fmt.Printf("%-35s %-40s %-25s %-8s %-8s\n", "模型 ID", "Target URL", "上游名称", "默认", "来源")
		fmt.Println(strings.Repeat("-", 120))
		for _, m := range models {
			isDefault := " "
			if m.IsDefault {
				isDefault = "✓"
			}
			upstream := m.UpstreamName
			if upstream == "" || upstream == m.ModelID {
				upstream = "（同 ID）"
			}
			aliases := m.Aliases()
			aliasStr := ""
			if len(aliases) > 0 {
				aliasStr = " [别名: " + strings.Join(aliases, ", ") + "]"
			}
			fmt.Printf("%-35s %-40s %-25s %-8s %-8s%s\n",
				m.ModelID, m.TargetURL, upstream, isDefault, m.Source, aliasStr)
		}
		fmt.Printf("\n共 %d 条记录\n", len(models))

		// 全局默认模型
		if cfg.LLM.DefaultModel != "" {
			fmt.Printf("\n全局默认模型 (llm.default_model): %s\n", cfg.LLM.DefaultModel)
		}
		return nil
	},
}

// ---------------------------------------------------------------------------
// sproxy admin model add
// ---------------------------------------------------------------------------

var (
	modelAddTarget   string
	modelAddID       string
	modelAddUpstream string
	modelAddAliases  []string
	modelAddDefault  bool
)

var adminModelAddCmd = &cobra.Command{
	Use:   "add",
	Short: "为 target 添加模型路由条目",
	Long: `为指定 LLM Target 添加一条模型路由条目（source=database，运行时生效，无需重启）。

配置文件中声明的条目（source=config）在每次启动时自动同步，
此命令适用于手动运行时添加模型条目。`,
	Example: `  # 为 Ollama target 添加 llama3.2 模型
  sproxy admin model add --target "http://localhost:11434" \
    --id "llama3.2" --upstream "llama3.2" --default

  # 将 claude-haiku 请求转发给 Ollama 的 llama3.2:1b
  sproxy admin model add --target "http://localhost:11434" \
    --id "claude-haiku-4-5" --upstream "llama3.2:1b"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, repo, logger, database, err := openModelAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		upstream := modelAddUpstream
		if upstream == "" {
			upstream = modelAddID
		}
		entry, err := repo.Create(modelAddTarget, modelAddID, upstream, modelAddAliases, modelAddDefault)
		if err != nil {
			return fmt.Errorf("add model: %w", err)
		}
		fmt.Printf("✓ 已添加模型路由条目\n")
		fmt.Printf("  模型 ID: %s\n", entry.ModelID)
		fmt.Printf("  Target:  %s\n", entry.TargetURL)
		fmt.Printf("  上游名:  %s\n", entry.UpstreamName)
		if len(modelAddAliases) > 0 {
			fmt.Printf("  别名:    %s\n", strings.Join(modelAddAliases, ", "))
		}
		if modelAddDefault {
			fmt.Printf("  默认:    ✓\n")
		}
		return nil
	},
}

// ---------------------------------------------------------------------------
// sproxy admin model remove
// ---------------------------------------------------------------------------

var (
	modelRemoveTarget string
	modelRemoveID     string
)

var adminModelRemoveCmd = &cobra.Command{
	Use:     "remove",
	Short:   "删除 target 的模型路由条目",
	Example: `  sproxy admin model remove --target "http://localhost:11434" --id "llama3.2"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, repo, logger, database, err := openModelAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		if err := repo.Delete(modelRemoveTarget, modelRemoveID); err != nil {
			return fmt.Errorf("remove model: %w", err)
		}
		fmt.Printf("✓ 已删除模型路由条目: %s (target: %s)\n", modelRemoveID, modelRemoveTarget)
		return nil
	},
}

// ---------------------------------------------------------------------------
// sproxy admin model default
// ---------------------------------------------------------------------------

var adminModelDefaultCmd = &cobra.Command{
	Use:   "default",
	Short: "查看全局默认模型配置",
	Long:  "显示 sproxy.yaml 中 llm.default_model 的当前值（auto 模式无绑定时使用）。",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, _, logger, database, err := openModelAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		if cfg.LLM.DefaultModel == "" {
			fmt.Println("未配置全局默认模型 (llm.default_model)")
			fmt.Println()
			fmt.Println("提示：在 sproxy.yaml 中添加：")
			fmt.Println("  llm:")
			fmt.Println("    default_model: \"claude-sonnet-4-5\"")
		} else {
			fmt.Printf("全局默认模型: %s\n", cfg.LLM.DefaultModel)
		}
		return nil
	},
}

// ---------------------------------------------------------------------------
// sproxy admin model sync
// ---------------------------------------------------------------------------

var adminModelSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "从配置文件同步模型条目到数据库",
	Long:  "读取 sproxy.yaml 中所有 target 的 models 字段，将 source=config 条目同步到数据库（幂等操作）。",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, repo, logger, database, err := openModelAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, database)

		total := 0
		for _, t := range cfg.LLM.Targets {
			if len(t.Models) == 0 {
				continue
			}
			if syncErr := repo.UpsertFromConfig(t.URL, t.Models); syncErr != nil {
				return fmt.Errorf("sync models for target %s: %w", t.URL, syncErr)
			}
			fmt.Printf("  ✓ %s: 同步 %d 条模型记录\n", t.URL, len(t.Models))
			total += len(t.Models)
		}
		if total == 0 {
			fmt.Println("（配置文件中无模型声明）")
		} else {
			fmt.Printf("\n共同步 %d 条模型记录\n", total)
		}
		return nil
	},
}

// ---------------------------------------------------------------------------
// init
// ---------------------------------------------------------------------------

func init() {
	// model add flags
	adminModelAddCmd.Flags().StringVar(&modelAddTarget, "target", "", "LLM Target URL（必填）")
	adminModelAddCmd.Flags().StringVar(&modelAddID, "id", "", "对外暴露的模型 ID（必填）")
	adminModelAddCmd.Flags().StringVar(&modelAddUpstream, "upstream", "", "上游实际模型名（空=同 ID）")
	adminModelAddCmd.Flags().StringArrayVar(&modelAddAliases, "alias", nil, "别名（可多次指定）")
	adminModelAddCmd.Flags().BoolVar(&modelAddDefault, "default", false, "设为该 target 的默认模型")
	_ = adminModelAddCmd.MarkFlagRequired("target")
	_ = adminModelAddCmd.MarkFlagRequired("id")

	// model remove flags
	adminModelRemoveCmd.Flags().StringVar(&modelRemoveTarget, "target", "", "LLM Target URL（必填）")
	adminModelRemoveCmd.Flags().StringVar(&modelRemoveID, "id", "", "要删除的模型 ID（必填）")
	_ = adminModelRemoveCmd.MarkFlagRequired("target")
	_ = adminModelRemoveCmd.MarkFlagRequired("id")

	adminModelCmd.AddCommand(
		adminModelListCmd,
		adminModelAddCmd,
		adminModelRemoveCmd,
		adminModelDefaultCmd,
		adminModelSyncCmd,
	)
}
