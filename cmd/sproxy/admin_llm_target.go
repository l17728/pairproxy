package main

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/db"
)

// llmTargetCmd LLM target 管理命令（父命令）
var llmTargetCmd = &cobra.Command{
	Use:   "target",
	Short: "Manage individual LLM targets",
	Long:  "Add, update, delete, enable, or disable individual LLM targets",
}

func init() {
	// 注册子命令
	llmTargetCmd.AddCommand(llmTargetAddCmd)
}

// ---------------------------------------------------------------------------
// llm target add
// ---------------------------------------------------------------------------

var (
	// Add command flags
	addURL             string
	addProvider        string
	addAPIKeyID        string
	addName            string
	addWeight          int
	addHealthCheckPath string
)

// llmTargetAddCmd 添加 LLM target
var llmTargetAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a new LLM target",
	Long:  "Add a new LLM target to the database (database-sourced, editable)",
	Example: `  sproxy admin llm target add \
    --url http://ollama.local:11434 \
    --provider ollama \
    --api-key-id key-abc123 \
    --name "Local Ollama" \
    --weight 1`,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		repo := db.NewLLMTargetRepo(gormDB, logger)

		// 验证 URL 唯一性
		exists, err := repo.URLExists(addURL)
		if err != nil {
			return fmt.Errorf("check url exists: %w", err)
		}
		if exists {
			return fmt.Errorf("URL already exists in config file or database: %s", addURL)
		}

		// 验证 API Key 存在性
		var apiKey db.APIKey
		if err := gormDB.Where("id = ?", addAPIKeyID).First(&apiKey).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return fmt.Errorf("API Key not found: %s", addAPIKeyID)
			}
			return fmt.Errorf("query api key: %w", err)
		}

		// 创建 target
		target := &db.LLMTarget{
			ID:              uuid.NewString(),
			URL:             addURL,
			APIKeyID:        &addAPIKeyID,
			Provider:        addProvider,
			Name:            addName,
			Weight:          addWeight,
			HealthCheckPath: addHealthCheckPath,
			Source:          "database",
			IsEditable:      true,
			IsActive:        true,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		}

		if err := repo.Create(target); err != nil {
			return fmt.Errorf("create target: %w", err)
		}

		// 记录审计日志
		auditCLI(gormDB, logger, "llm_target.add", addURL, fmt.Sprintf("provider=%s name=%s", addProvider, addName))

		// 输出成功信息
		fmt.Printf("✓ LLM target added successfully\n")
		fmt.Printf("  ID:       %s\n", target.ID)
		fmt.Printf("  URL:      %s\n", target.URL)
		fmt.Printf("  Provider: %s\n", target.Provider)
		fmt.Printf("  Name:     %s\n", target.Name)
		fmt.Printf("  Weight:   %d\n", target.Weight)
		fmt.Printf("  Source:   %s\n", target.Source)

		return nil
	},
}

func init() {
	llmTargetAddCmd.Flags().StringVar(&addURL, "url", "", "LLM endpoint URL (required)")
	llmTargetAddCmd.Flags().StringVar(&addProvider, "provider", "anthropic", "Provider: anthropic, openai, ollama")
	llmTargetAddCmd.Flags().StringVar(&addAPIKeyID, "api-key-id", "", "API Key ID (required)")
	llmTargetAddCmd.Flags().StringVar(&addName, "name", "", "Display name")
	llmTargetAddCmd.Flags().IntVar(&addWeight, "weight", 1, "Load balancing weight")
	llmTargetAddCmd.Flags().StringVar(&addHealthCheckPath, "health-check-path", "", "Health check path")

	llmTargetAddCmd.MarkFlagRequired("url")
	llmTargetAddCmd.MarkFlagRequired("api-key-id")
}
