// Package version 提供构建时注入的版本信息。
// 变量由 Makefile 通过 -ldflags 在编译时注入，开发时为默认值。
package version

import (
	"fmt"
	"runtime"
)

// Version 是 git tag（如 v1.0.0），构建时由 Makefile 注入。
var Version = "dev"

// Commit 是 git 短 hash，构建时由 Makefile 注入。
var Commit = "unknown"

// BuiltAt 是 UTC 构建时间（RFC3339），构建时由 Makefile 注入。
var BuiltAt = "unknown"

// Short 返回单行版本字符串，用于 cobra rootCmd.Version（cobra 会自动添加 "<binary> version " 前缀）。
//
//	v1.2.0 (abc1234) built 2025-01-01T00:00:00Z
func Short() string {
	return fmt.Sprintf("%s (%s) built %s", Version, Commit, BuiltAt)
}

// Full 返回多行详细版本信息，用于 version 子命令。
func Full(binary string) string {
	return fmt.Sprintf(
		"%s\n  version:  %s\n  commit:   %s\n  built:    %s\n  go:       %s\n  os/arch:  %s/%s",
		binary,
		Version,
		Commit,
		BuiltAt,
		runtime.Version(),
		runtime.GOOS,
		runtime.GOARCH,
	)
}
