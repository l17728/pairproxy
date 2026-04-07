package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/db"
)

// targetsetCmd 代表 targetset 命令
var targetsetCmd = &cobra.Command{
	Use:   "targetset",
	Short: "Manage group target sets",
	Long:  "Manage group target sets for load balancing and failover",
}

// targetsetListCmd 列出所有 target sets
var targetsetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all target sets",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		repo := db.NewGroupTargetSetRepo(gormDB, logger)
		sets, err := repo.ListAll()
		if err != nil {
			return fmt.Errorf("list target sets: %w", err)
		}

		if len(sets) == 0 {
			fmt.Println("No target sets found")
			return nil
		}

		fmt.Printf("%-36s  %-20s  %-20s  %-15s  %-8s\n", "ID", "Name", "Group", "Strategy", "Members")
		fmt.Println("------------------------------------------------------------------------")
		for _, set := range sets {
			groupName := "default"
			if set.GroupID != nil {
				groupName = *set.GroupID
			}
			members, err := repo.ListMembers(set.ID)
			if err != nil {
				logger.Warn("failed to list members", zap.Error(err))
				members = []db.GroupTargetSetMember{}
			}
			fmt.Printf("%-36s  %-20s  %-20s  %-15s  %-8d\n",
				set.ID, set.Name, groupName, set.Strategy, len(members))
		}
		return nil
	},
}

// targetsetCreateCmd 创建新的 target set
var targetsetCreateCmd = &cobra.Command{
	Use:   "create <id>",
	Short: "Create a new target set",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		id := args[0]
		name, _ := cmd.Flags().GetString("name")
		group, _ := cmd.Flags().GetString("group")
		strategy, _ := cmd.Flags().GetString("strategy")
		retryPolicy, _ := cmd.Flags().GetString("retry-policy")
		isDefault, _ := cmd.Flags().GetBool("default")

		if name == "" {
			return errors.New("--name is required")
		}

		// Validate ID format: alphanumeric, dash, underscore only
		if !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(id) {
			return fmt.Errorf("invalid ID format: must contain only alphanumeric, dash, underscore")
		}

		repo := db.NewGroupTargetSetRepo(gormDB, logger)

		var groupIDPtr *string
		if group != "" {
			groupIDPtr = &group
		}

		logger.Info("creating target set", zap.String("id", id), zap.String("name", name))

		set := &db.GroupTargetSet{
			ID:          id,
			Name:        name,
			GroupID:     groupIDPtr,
			Strategy:    strategy,
			RetryPolicy: retryPolicy,
			IsDefault:   isDefault,
		}

		if err := repo.Create(set); err != nil {
			logger.Error("failed to create target set", zap.Error(err))
			return fmt.Errorf("create target set: %w", err)
		}

		detail := map[string]interface{}{
			"id":           id,
			"name":         name,
			"group_id":     groupIDPtr,
			"strategy":     strategy,
			"retry_policy": retryPolicy,
			"is_default":   isDefault,
		}
		detailBytes, _ := json.Marshal(detail)
		auditCLI(gormDB, logger, "targetset.create", id, string(detailBytes))

		logger.Info("target set created successfully", zap.String("id", id))

		fmt.Printf("✓ Target set created successfully\n")
		fmt.Printf("  ID:           %s\n", id)
		fmt.Printf("  Name:         %s\n", name)
		if groupIDPtr != nil {
			fmt.Printf("  Group:        %s\n", *groupIDPtr)
		} else {
			fmt.Printf("  Group:        default (global)\n")
		}
		fmt.Printf("  Strategy:     %s\n", strategy)
		return nil
	},
}

// targetsetDeleteCmd 删除 target set
var targetsetDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a target set",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		name := args[0]
		repo := db.NewGroupTargetSetRepo(gormDB, logger)

		set, err := repo.GetByName(name)
		if err != nil {
			return fmt.Errorf("get target set: %w", err)
		}
		if set == nil {
			return fmt.Errorf("target set not found: %s", name)
		}

		if err := repo.Delete(set.ID); err != nil {
			return fmt.Errorf("delete target set: %w", err)
		}

		detail := map[string]interface{}{"id": set.ID, "name": name}
		detailBytes, _ := json.Marshal(detail)
		auditCLI(gormDB, logger, "targetset.delete", name, string(detailBytes))

		fmt.Printf("✓ Target set deleted: %s\n", name)
		return nil
	},
}

// targetsetAddTargetCmd 添加 target 到 set
var targetsetAddTargetCmd = &cobra.Command{
	Use:   "add-target <set_name>",
	Short: "Add a target to a set",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		setName := args[0]
		url, _ := cmd.Flags().GetString("url")
		weight, _ := cmd.Flags().GetInt("weight")
		priority, _ := cmd.Flags().GetInt("priority")

		if url == "" {
			return errors.New("--url is required")
		}

		repo := db.NewGroupTargetSetRepo(gormDB, logger)
		set, err := repo.GetByName(setName)
		if err != nil {
			return fmt.Errorf("get target set: %w", err)
		}
		if set == nil {
			return fmt.Errorf("target set not found: %s", setName)
		}

		member := &db.GroupTargetSetMember{
			ID:           uuid.NewString(),
			TargetSetID:  set.ID,
			TargetURL:    url,
			Weight:       weight,
			Priority:     priority,
			IsActive:     true,
			HealthStatus: "unknown",
			CreatedAt:    time.Now(),
		}

		if err := repo.AddMember(set.ID, member); err != nil {
			return fmt.Errorf("add member: %w", err)
		}

		detail := map[string]interface{}{
			"set_name": setName,
			"url":      url,
			"weight":   weight,
			"priority": priority,
		}
		detailBytes, _ := json.Marshal(detail)
		auditCLI(gormDB, logger, "targetset.add_member", setName, string(detailBytes))

		fmt.Printf("✓ Target added to set\n")
		fmt.Printf("  Set:      %s\n", setName)
		fmt.Printf("  URL:      %s\n", url)
		fmt.Printf("  Weight:   %d\n", weight)
		fmt.Printf("  Priority: %d\n", priority)
		return nil
	},
}

// targetsetRemoveTargetCmd 从 set 移除 target
var targetsetRemoveTargetCmd = &cobra.Command{
	Use:   "remove-target <set_name>",
	Short: "Remove a target from a set",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		setName := args[0]
		url, _ := cmd.Flags().GetString("url")

		if url == "" {
			return errors.New("--url is required")
		}

		repo := db.NewGroupTargetSetRepo(gormDB, logger)
		set, err := repo.GetByName(setName)
		if err != nil {
			return fmt.Errorf("get target set: %w", err)
		}
		if set == nil {
			return fmt.Errorf("target set not found: %s", setName)
		}

		if err := repo.RemoveMember(set.ID, url); err != nil {
			return fmt.Errorf("remove member: %w", err)
		}

		detail := map[string]interface{}{
			"set_name": setName,
			"url":      url,
		}
		detailBytes, _ := json.Marshal(detail)
		auditCLI(gormDB, logger, "targetset.remove_member", setName, string(detailBytes))

		fmt.Printf("✓ Target removed from set\n")
		fmt.Printf("  Set: %s\n", setName)
		fmt.Printf("  URL: %s\n", url)
		return nil
	},
}

// targetsetSetWeightCmd 更新 target 权重
var targetsetSetWeightCmd = &cobra.Command{
	Use:   "set-weight <set_name>",
	Short: "Update target weight",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		setName := args[0]
		url, _ := cmd.Flags().GetString("url")
		weight, _ := cmd.Flags().GetInt("weight")
		priority, _ := cmd.Flags().GetInt("priority")

		if url == "" {
			return errors.New("--url is required")
		}

		repo := db.NewGroupTargetSetRepo(gormDB, logger)
		set, err := repo.GetByName(setName)
		if err != nil {
			return fmt.Errorf("get target set: %w", err)
		}
		if set == nil {
			return fmt.Errorf("target set not found: %s", setName)
		}

		if err := repo.UpdateMember(set.ID, url, weight, priority); err != nil {
			return fmt.Errorf("update member: %w", err)
		}

		detail := map[string]interface{}{
			"set_name": setName,
			"url":      url,
			"weight":   weight,
			"priority": priority,
		}
		detailBytes, _ := json.Marshal(detail)
		auditCLI(gormDB, logger, "targetset.update_member", setName, string(detailBytes))

		fmt.Printf("✓ Target weight updated\n")
		fmt.Printf("  Set:      %s\n", setName)
		fmt.Printf("  URL:      %s\n", url)
		fmt.Printf("  Weight:   %d\n", weight)
		fmt.Printf("  Priority: %d\n", priority)
		return nil
	},
}

// targetsetShowCmd 查看 target set 详情
var targetsetShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show target set details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, _, logger, gormDB, err := openAdminDB()
		if err != nil {
			return err
		}
		defer closeGormDB(logger, gormDB)

		name := args[0]
		repo := db.NewGroupTargetSetRepo(gormDB, logger)

		set, err := repo.GetByName(name)
		if err != nil {
			return fmt.Errorf("get target set: %w", err)
		}
		if set == nil {
			return fmt.Errorf("target set not found: %s", name)
		}

		members, err := repo.ListMembers(set.ID)
		if err != nil {
			return fmt.Errorf("list members: %w", err)
		}

		groupName := "default (global)"
		if set.GroupID != nil {
			groupName = *set.GroupID
		}

		fmt.Printf("Target Set: %s\n", name)
		fmt.Printf("  ID:           %s\n", set.ID)
		fmt.Printf("  Name:         %s\n", set.Name)
		fmt.Printf("  Group:        %s\n", groupName)
		fmt.Printf("  Strategy:     %s\n", set.Strategy)
		fmt.Printf("  Retry Policy: %s\n", set.RetryPolicy)
		fmt.Printf("  Is Default:   %v\n", set.IsDefault)
		fmt.Printf("  Created:      %s\n", set.CreatedAt.Format("2006-01-02 15:04:05"))

		if len(members) == 0 {
			fmt.Println("\nMembers: (none)")
			return nil
		}

		fmt.Printf("\nMembers (%d):\n", len(members))
		fmt.Printf("  %-40s  %-8s  %-8s  %-10s\n", "URL", "Weight", "Priority", "Status")
		fmt.Println("  " + "------------------------------------------------------------------------")
		for _, m := range members {
			status := "active"
			if !m.IsActive {
				status = "inactive"
			}
			fmt.Printf("  %-40s  %-8d  %-8d  %-10s\n", m.TargetURL, m.Weight, m.Priority, status)
		}
		return nil
	},
}

// alertCmd 代表 alert 命令
var alertCmd = &cobra.Command{
	Use:   "alert",
	Short: "Manage target alerts",
	Long:  "Manage target alerts and health status",
}

// alertListCmd 列出活跃告警
var alertListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active alerts",
	RunE: func(cmd *cobra.Command, args []string) error {
		target, _ := cmd.Flags().GetString("target")
		severity, _ := cmd.Flags().GetString("severity")

		fmt.Printf("Listing alerts (target: %s, severity: %s)\n", target, severity)
		return nil
	},
}

// alertHistoryCmd 查看告警历史
var alertHistoryCmd = &cobra.Command{
	Use:   "history",
	Short: "View alert history",
	RunE: func(cmd *cobra.Command, args []string) error {
		days, _ := cmd.Flags().GetInt("days")
		target, _ := cmd.Flags().GetString("target")

		fmt.Printf("Showing alert history (days: %d, target: %s)\n", days, target)
		return nil
	},
}

// alertResolveCmd 手动解决告警
var alertResolveCmd = &cobra.Command{
	Use:   "resolve <alert_id>",
	Short: "Manually resolve an alert",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		alertID := args[0]
		fmt.Printf("Resolving alert: %s\n", alertID)
		return nil
	},
}

// alertStatsCmd 查看告警统计
var alertStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "View alert statistics",
	RunE: func(cmd *cobra.Command, args []string) error {
		days, _ := cmd.Flags().GetInt("days")
		target, _ := cmd.Flags().GetString("target")

		fmt.Printf("Showing alert stats (days: %d, target: %s)\n", days, target)
		return nil
	},
}

func init() {
	// targetset 子命令
	targetsetCmd.AddCommand(
		targetsetListCmd,
		targetsetCreateCmd,
		targetsetDeleteCmd,
		targetsetAddTargetCmd,
		targetsetRemoveTargetCmd,
		targetsetSetWeightCmd,
		targetsetShowCmd,
	)

	// targetset 标志
	targetsetCreateCmd.Flags().StringP("name", "n", "", "Target set name")
	targetsetCreateCmd.Flags().StringP("group", "g", "", "Group ID (optional, empty = default/global)")
	targetsetCreateCmd.Flags().StringP("strategy", "s", "weighted_random", "Selection strategy (weighted_random, round_robin, priority)")
	targetsetCreateCmd.Flags().StringP("retry-policy", "r", "try_next", "Retry policy (try_next, fail_fast)")
	targetsetCreateCmd.Flags().BoolP("default", "d", false, "Mark as default target set")
	_ = targetsetCreateCmd.MarkFlagRequired("name")

	targetsetAddTargetCmd.Flags().StringP("url", "u", "", "Target URL")
	targetsetAddTargetCmd.Flags().IntP("weight", "w", 1, "Target weight (default: 1)")
	targetsetAddTargetCmd.Flags().IntP("priority", "p", 0, "Target priority (default: 0)")
	_ = targetsetAddTargetCmd.MarkFlagRequired("url")

	targetsetRemoveTargetCmd.Flags().StringP("url", "u", "", "Target URL")
	_ = targetsetRemoveTargetCmd.MarkFlagRequired("url")

	targetsetSetWeightCmd.Flags().StringP("url", "u", "", "Target URL")
	targetsetSetWeightCmd.Flags().IntP("weight", "w", 1, "Target weight")
	targetsetSetWeightCmd.Flags().IntP("priority", "p", 0, "Target priority")
	_ = targetsetSetWeightCmd.MarkFlagRequired("url")

	// alert 子命令
	alertCmd.AddCommand(
		alertListCmd,
		alertHistoryCmd,
		alertResolveCmd,
		alertStatsCmd,
	)

	// alert 标志
	alertListCmd.Flags().StringP("target", "t", "", "Target URL filter")
	alertListCmd.Flags().StringP("severity", "s", "", "Severity filter")

	alertHistoryCmd.Flags().IntP("days", "d", 7, "Number of days")
	alertHistoryCmd.Flags().StringP("target", "t", "", "Target URL filter")

	alertStatsCmd.Flags().IntP("days", "d", 30, "Number of days")
	alertStatsCmd.Flags().StringP("target", "t", "", "Target URL filter")
}

// GetTargetSetCmd 返回 targetset 命令
func GetTargetSetCmd() *cobra.Command {
	return targetsetCmd
}

// GetAlertCmd 返回 alert 命令
func GetAlertCmd() *cobra.Command {
	return alertCmd
}
