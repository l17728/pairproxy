package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/l17728/pairproxy/internal/db"
)

// resolveUniqueTarget 将 UUID 或 URL 解析为单一 LLMTarget。
// 若输入是已知 UUID，直接按 ID 查找。
// 若输入是 URL 且对应多条记录（同 URL 多 APIKey），返回错误并列出所有匹配的 UUID，
// 提示用户改用 UUID 明确指定目标。
func resolveUniqueTarget(repo *db.LLMTargetRepo, uuidOrURL string) (*db.LLMTarget, error) {
	// 先尝试按 ID 精确查
	if t, err := repo.GetByID(uuidOrURL); err == nil && t != nil {
		return t, nil
	}
	// 再按 URL 查，检测是否有歧义
	matches, err := repo.ListByURL(uuidOrURL)
	if err != nil {
		return nil, fmt.Errorf("target lookup failed: %w", err)
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("LLM target not found: %s", uuidOrURL)
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			keyInfo := "(no api key)"
			if m.APIKeyID != nil {
				keyInfo = "api_key_id=" + *m.APIKeyID
			}
			ids[i] = fmt.Sprintf("  %s  [%s]", m.ID, keyInfo)
		}
		return nil, fmt.Errorf(
			"URL %q matches %d targets (multiple API keys configured for this URL).\n"+
				"Please use a UUID to specify which target:\n%s",
			uuidOrURL, len(matches), strings.Join(ids, "\n"))
	}
}

// llmTargetCmd LLM target 管理命令（父命令）
var llmTargetCmd = &cobra.Command{
	Use:   "target",
	Short: "Manage individual LLM targets",
	Long:  "Add, update, delete, enable, or disable individual LLM targets",
}

func init() {
	// 注册子命令
	llmTargetCmd.AddCommand(llmTargetAddCmd)
	llmTargetCmd.AddCommand(llmTargetUpdateCmd)
	llmTargetCmd.AddCommand(llmTargetDeleteCmd)
	llmTargetCmd.AddCommand(llmTargetEnableCmd)
	llmTargetCmd.AddCommand(llmTargetDisableCmd)
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
	addSupportedModels []string
	addAutoModel       string
	addModelMapping    string // JSON string, e.g. '{"*":"llama3.2"}'
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

		// 验证 (URL, APIKeyID) 组合唯一性
		var apiKeyIDPtr *string
		if addAPIKeyID != "" {
			apiKeyIDPtr = &addAPIKeyID
		}
		exists, err := repo.ComboExists(addURL, apiKeyIDPtr)
		if err != nil {
			return fmt.Errorf("check url exists: %w", err)
		}
		if exists {
			return fmt.Errorf("URL+API-key combination already exists: %s", addURL)
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
		supportedModelsJSON := "[]"
		if len(addSupportedModels) > 0 {
			if b, err := json.Marshal(addSupportedModels); err == nil {
				supportedModelsJSON = string(b)
			}
		}

		modelMappingJSON := "{}"
		if addModelMapping != "" {
			var mm map[string]string
			if err := json.Unmarshal([]byte(addModelMapping), &mm); err != nil {
				return fmt.Errorf("invalid --model-mapping JSON: %w\nExample: --model-mapping '{\"*\":\"llama3.2\"}'", err)
			}
			if b, err := json.Marshal(mm); err == nil {
				modelMappingJSON = string(b)
			}
		}

		target := &db.LLMTarget{
			ID:                  uuid.NewString(),
			URL:                 addURL,
			APIKeyID:            &addAPIKeyID,
			Provider:            addProvider,
			Name:                addName,
			Weight:              addWeight,
			HealthCheckPath:     addHealthCheckPath,
			SupportedModelsJSON: supportedModelsJSON,
			ModelMappingJSON:    modelMappingJSON,
			AutoModel:           addAutoModel,
			Source:              "database",
			IsEditable:          true,
			IsActive:            true,
			CreatedAt:           time.Now(),
			UpdatedAt:           time.Now(),
		}

		if err := repo.Create(target); err != nil {
			return fmt.Errorf("create target: %w", err)
		}

		// 记录审计日志
		auditDetails := fmt.Sprintf("provider=%s name=%s", addProvider, addName)
		if len(addSupportedModels) > 0 {
			auditDetails += fmt.Sprintf(" supported_models=%v", addSupportedModels)
		}
		if addAutoModel != "" {
			auditDetails += fmt.Sprintf(" auto_model=%s", addAutoModel)
		}
		if addModelMapping != "" {
			auditDetails += fmt.Sprintf(" model_mapping=%s", modelMappingJSON)
		}
		auditCLI(gormDB, logger, "llm_target.add", addURL, auditDetails)

		// 输出成功信息
		fmt.Printf("✓ LLM target added successfully\n")
		fmt.Printf("  ID:               %s\n", target.ID)
		fmt.Printf("  URL:              %s\n", target.URL)
		fmt.Printf("  Provider:         %s\n", target.Provider)
		fmt.Printf("  Name:             %s\n", target.Name)
		fmt.Printf("  Weight:           %d\n", target.Weight)
		if len(addSupportedModels) > 0 {
			fmt.Printf("  Supported Models: %v\n", addSupportedModels)
		}
		if addAutoModel != "" {
			fmt.Printf("  Auto Model:       %s\n", addAutoModel)
		}
		if addModelMapping != "" {
			fmt.Printf("  Model Mapping:    %s\n", modelMappingJSON)
		}
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
	llmTargetAddCmd.Flags().StringSliceVar(&addSupportedModels, "supported-models", []string{}, "Supported models (comma-separated, e.g., claude-3-*,gpt-4-*)")
	llmTargetAddCmd.Flags().StringVar(&addAutoModel, "auto-model", "", "Model to use for auto mode requests")
	llmTargetAddCmd.Flags().StringVar(&addModelMapping, "model-mapping", "", `Model name mapping as JSON, e.g. '{"*":"llama3.2"}' or '{"claude-3-5-sonnet-20241022":"mistral"}'`)

	_ = llmTargetAddCmd.MarkFlagRequired("url")
	_ = llmTargetAddCmd.MarkFlagRequired("api-key-id")
}

// ---------------------------------------------------------------------------
// llm target update
// ---------------------------------------------------------------------------

var (
	// Update command flags
	updateProvider        string
	updateAPIKeyID        string
	updateName            string
	updateWeight          int
	updateHealthCheckPath string
	updateSupportedModels []string
	updateAutoModel       string
	updateModelMapping    string // JSON string, e.g. '{"*":"llama3.2"}'; use '{}' to clear
)

// llmTargetUpdateCmd 更新 LLM target
var llmTargetUpdateCmd = &cobra.Command{
	Use:   "update <url>",
	Short: "Update an existing LLM target",
	Long:  "Update an existing LLM target in the database (only database-sourced targets can be updated)",
	Example: `  sproxy admin llm target update http://ollama.local:11434 \
    --provider ollama \
    --name "Updated Ollama" \
    --weight 2`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targetURL := args[0]

		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		repo := db.NewLLMTargetRepo(gormDB, logger)

		// 查询 target（UUID 或 URL；URL 多条时报错提示使用 UUID）
		target, err := resolveUniqueTarget(repo, targetURL)
		if err != nil {
			return err
		}

		// 检查是否可编辑
		if !target.IsEditable {
			return fmt.Errorf("cannot update config-sourced target: %s\nConfig-sourced targets must be modified in sproxy.yaml", targetURL)
		}

		// 记录变更前的值（用于审计日志）
		changes := []string{}

		// 更新字段（仅更新提供的 flag）
		if cmd.Flags().Changed("provider") {
			if target.Provider != updateProvider {
				changes = append(changes, fmt.Sprintf("provider: %s→%s", target.Provider, updateProvider))
				target.Provider = updateProvider
			}
		}

		if cmd.Flags().Changed("api-key-id") {
			// 验证 API Key 存在性
			var apiKey db.APIKey
			if err := gormDB.Where("id = ?", updateAPIKeyID).First(&apiKey).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return fmt.Errorf("API Key not found: %s", updateAPIKeyID)
				}
				return fmt.Errorf("query api key: %w", err)
			}

			oldKeyID := ""
			if target.APIKeyID != nil {
				oldKeyID = *target.APIKeyID
			}
			if oldKeyID != updateAPIKeyID {
				changes = append(changes, fmt.Sprintf("api_key_id: %s→%s", oldKeyID, updateAPIKeyID))
				target.APIKeyID = &updateAPIKeyID
			}
		}

		if cmd.Flags().Changed("name") {
			if target.Name != updateName {
				changes = append(changes, fmt.Sprintf("name: %s→%s", target.Name, updateName))
				target.Name = updateName
			}
		}

		if cmd.Flags().Changed("weight") {
			if target.Weight != updateWeight {
				changes = append(changes, fmt.Sprintf("weight: %d→%d", target.Weight, updateWeight))
				target.Weight = updateWeight
			}
		}

		if cmd.Flags().Changed("health-check-path") {
			if target.HealthCheckPath != updateHealthCheckPath {
				changes = append(changes, fmt.Sprintf("health_check_path: %s→%s", target.HealthCheckPath, updateHealthCheckPath))
				target.HealthCheckPath = updateHealthCheckPath
			}
		}

		if cmd.Flags().Changed("supported-models") {
			newSupportedModelsJSON := "[]"
			if len(updateSupportedModels) > 0 {
				if b, err := json.Marshal(updateSupportedModels); err == nil {
					newSupportedModelsJSON = string(b)
				}
			}
			if newSupportedModelsJSON != target.SupportedModelsJSON {
				changes = append(changes, fmt.Sprintf("supported_models: %s→%s", target.SupportedModelsJSON, newSupportedModelsJSON))
				target.SupportedModelsJSON = newSupportedModelsJSON
			}
		}

		if cmd.Flags().Changed("auto-model") {
			if target.AutoModel != updateAutoModel {
				changes = append(changes, fmt.Sprintf("auto_model: %s→%s", target.AutoModel, updateAutoModel))
				target.AutoModel = updateAutoModel
			}
		}

		if cmd.Flags().Changed("model-mapping") {
			var mm map[string]string
			if updateModelMapping != "" && updateModelMapping != "{}" {
				if err := json.Unmarshal([]byte(updateModelMapping), &mm); err != nil {
					return fmt.Errorf("invalid --model-mapping JSON: %w\nExample: --model-mapping '{\"*\":\"llama3.2\"}'", err)
				}
			}
			newModelMappingJSON := "{}"
			if len(mm) > 0 {
				if b, err := json.Marshal(mm); err == nil {
					newModelMappingJSON = string(b)
				}
			}
			if newModelMappingJSON != target.ModelMappingJSON {
				changes = append(changes, fmt.Sprintf("model_mapping: %s→%s", target.ModelMappingJSON, newModelMappingJSON))
				target.ModelMappingJSON = newModelMappingJSON
			}
		}

		if len(changes) == 0 {
			fmt.Printf("No changes detected for target: %s\n", targetURL)
			return nil
		}

		// 更新时间戳
		target.UpdatedAt = time.Now()

		// 执行更新
		if err := repo.Update(target); err != nil {
			return fmt.Errorf("update target: %w", err)
		}

		// 记录审计日志
		changesSummary := ""
		for i, change := range changes {
			if i > 0 {
				changesSummary += ", "
			}
			changesSummary += change
		}
		auditCLI(gormDB, logger, "llm_target.update", targetURL, changesSummary)

		// 输出成功信息
		fmt.Printf("✓ LLM target updated successfully\n")
		fmt.Printf("  URL:      %s\n", target.URL)
		fmt.Printf("  Provider: %s\n", target.Provider)
		fmt.Printf("  Name:     %s\n", target.Name)
		fmt.Printf("  Weight:   %d\n", target.Weight)
		if len(changes) > 0 {
			fmt.Printf("\nChanges:\n")
			for _, change := range changes {
				fmt.Printf("  - %s\n", change)
			}
		}

		return nil
	},
}

func init() {
	llmTargetUpdateCmd.Flags().StringVar(&updateProvider, "provider", "", "Provider: anthropic, openai, ollama")
	llmTargetUpdateCmd.Flags().StringVar(&updateAPIKeyID, "api-key-id", "", "API Key ID")
	llmTargetUpdateCmd.Flags().StringVar(&updateName, "name", "", "Display name")
	llmTargetUpdateCmd.Flags().IntVar(&updateWeight, "weight", 0, "Load balancing weight")
	llmTargetUpdateCmd.Flags().StringVar(&updateHealthCheckPath, "health-check-path", "", "Health check path")
	llmTargetUpdateCmd.Flags().StringSliceVar(&updateSupportedModels, "supported-models", []string{}, "Supported models (comma-separated, e.g., claude-3-*,gpt-4-*)")
	llmTargetUpdateCmd.Flags().StringVar(&updateAutoModel, "auto-model", "", "Model to use for auto mode requests")
	llmTargetUpdateCmd.Flags().StringVar(&updateModelMapping, "model-mapping", "", `Model name mapping as JSON, e.g. '{"*":"llama3.2"}'; use '{}' to clear`)
}

// ---------------------------------------------------------------------------
// llm target delete
// ---------------------------------------------------------------------------

// llmTargetDeleteCmd 删除 LLM target
var llmTargetDeleteCmd = &cobra.Command{
	Use:     "delete <url>",
	Short:   "Delete an LLM target",
	Long:    "Delete an LLM target from the database (only database-sourced targets can be deleted)",
	Example: `  sproxy admin llm target delete http://ollama.local:11434`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targetURL := args[0]

		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		repo := db.NewLLMTargetRepo(gormDB, logger)

		// 查询 target（UUID 或 URL；URL 多条时报错提示使用 UUID）
		target, err := resolveUniqueTarget(repo, targetURL)
		if err != nil {
			return err
		}

		// 检查是否可编辑
		if !target.IsEditable {
			return fmt.Errorf("cannot delete config-sourced target: %s\nConfig-sourced targets must be removed from sproxy.yaml", targetURL)
		}

		// 删除 target
		if err := repo.Delete(target.ID); err != nil {
			return fmt.Errorf("delete target: %w", err)
		}

		// 记录审计日志
		auditCLI(gormDB, logger, "llm_target.delete", targetURL, fmt.Sprintf("id=%s name=%s", target.ID, target.Name))

		// 输出成功信息
		fmt.Printf("✓ LLM target deleted successfully\n")
		fmt.Printf("  URL:      %s\n", target.URL)
		fmt.Printf("  Name:     %s\n", target.Name)
		fmt.Printf("  Provider: %s\n", target.Provider)

		return nil
	},
}

// ---------------------------------------------------------------------------
// llm target enable
// ---------------------------------------------------------------------------

// llmTargetEnableCmd 启用 LLM target
var llmTargetEnableCmd = &cobra.Command{
	Use:     "enable <url>",
	Short:   "Enable an LLM target",
	Long:    "Enable an LLM target (set is_active=true)",
	Example: `  sproxy admin llm target enable http://ollama.local:11434`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targetURL := args[0]

		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		repo := db.NewLLMTargetRepo(gormDB, logger)

		// 查询 target（UUID 或 URL；URL 多条时报错提示使用 UUID）
		target, err := resolveUniqueTarget(repo, targetURL)
		if err != nil {
			return err
		}

		// 检查当前状态
		if target.IsActive {
			fmt.Printf("Target is already enabled: %s\n", targetURL)
			return nil
		}

		// 更新 is_active 字段
		target.IsActive = true
		target.UpdatedAt = time.Now()

		// 使用 Updates 方法更新（支持 boolean 字段）
		if err := gormDB.Model(&db.LLMTarget{}).Where("id = ?", target.ID).
			Updates(map[string]interface{}{
				"is_active":  true,
				"updated_at": target.UpdatedAt,
			}).Error; err != nil {
			logger.Error("failed to enable llm target",
				zap.String("id", target.ID),
				zap.String("url", target.URL),
				zap.Error(err))
			return fmt.Errorf("enable target: %w", err)
		}

		// 记录审计日志
		auditCLI(gormDB, logger, "llm_target.enable", targetURL, fmt.Sprintf("id=%s name=%s", target.ID, target.Name))

		// 输出成功信息
		fmt.Printf("✓ LLM target enabled successfully\n")
		fmt.Printf("  URL:      %s\n", target.URL)
		fmt.Printf("  Name:     %s\n", target.Name)
		fmt.Printf("  Provider: %s\n", target.Provider)
		fmt.Printf("  Status:   active\n")

		return nil
	},
}

// ---------------------------------------------------------------------------
// llm target disable
// ---------------------------------------------------------------------------

// llmTargetDisableCmd 禁用 LLM target
var llmTargetDisableCmd = &cobra.Command{
	Use:     "disable <url>",
	Short:   "Disable an LLM target",
	Long:    "Disable an LLM target (set is_active=false)",
	Example: `  sproxy admin llm target disable http://ollama.local:11434`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		targetURL := args[0]

		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		repo := db.NewLLMTargetRepo(gormDB, logger)

		// 查询 target（UUID 或 URL；URL 多条时报错提示使用 UUID）
		target, err := resolveUniqueTarget(repo, targetURL)
		if err != nil {
			return err
		}

		// 检查当前状态
		if !target.IsActive {
			fmt.Printf("Target is already disabled: %s\n", targetURL)
			return nil
		}

		// 更新 is_active 字段
		target.IsActive = false
		target.UpdatedAt = time.Now()

		// 使用 Updates 方法更新（支持 boolean 字段）
		if err := gormDB.Model(&db.LLMTarget{}).Where("id = ?", target.ID).
			Updates(map[string]interface{}{
				"is_active":  false,
				"updated_at": target.UpdatedAt,
			}).Error; err != nil {
			logger.Error("failed to disable llm target",
				zap.String("id", target.ID),
				zap.String("url", target.URL),
				zap.Error(err))
			return fmt.Errorf("disable target: %w", err)
		}

		// 记录审计日志
		auditCLI(gormDB, logger, "llm_target.disable", targetURL, fmt.Sprintf("id=%s name=%s", target.ID, target.Name))

		// 输出成功信息
		fmt.Printf("✓ LLM target disabled successfully\n")
		fmt.Printf("  URL:      %s\n", target.URL)
		fmt.Printf("  Name:     %s\n", target.Name)
		fmt.Printf("  Provider: %s\n", target.Provider)
		fmt.Printf("  Status:   inactive\n")

		return nil
	},
}
