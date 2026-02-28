//go:build !windows

package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"go.uber.org/zap"

	"github.com/l17728/pairproxy/internal/auth"
)

// isWindowsService always returns false on non-Windows platforms.
func isWindowsService() bool { return false }

// runAsWindowsService is never called on non-Windows platforms.
func runAsWindowsService(_ *http.Server, _ *zap.Logger) error {
	panic("runAsWindowsService called on non-Windows platform")
}

// daemonize re-executes the current binary in a new session detached from the
// controlling terminal. The parent process exits after the child is spawned.
//
// If the environment variable _CPROXY_DAEMON=1 is already set, the function
// is a no-op (we are already the background child).
func daemonize(configPath string) error {
	if os.Getenv("_CPROXY_DAEMON") == "1" {
		// We are the daemon child — continue with normal server startup.
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	// Child process args: `cproxy start [--config <path>]` (no --daemon).
	childArgs := []string{"start"}
	if configPath != "" {
		childArgs = append(childArgs, "--config", configPath)
	}

	// Write stdout/stderr to the token directory log file.
	logDir := auth.DefaultTokenDir()
	logPath := filepath.Join(logDir, "cproxy.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// Fall back to the system temp directory.
		logPath = filepath.Join(os.TempDir(), "cproxy.log")
		logFile, err = os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("open log file %q: %w", logPath, err)
		}
	}

	cmd := exec.Command(exe, childArgs...)
	cmd.Env = append(os.Environ(), "_CPROXY_DAEMON=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Setsid creates a new session so the child is fully detached from the
	// terminal and will not receive SIGHUP when the parent's session ends.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start background process: %w", err)
	}
	logFile.Close()

	pid := cmd.Process.Pid
	pidPath := filepath.Join(logDir, "cproxy.pid")
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0644)

	fmt.Printf("✓ cproxy started in background (PID %d)\n", pid)
	fmt.Printf("  Logs: %s\n", logPath)
	fmt.Printf("  PID:  %s\n", pidPath)
	fmt.Printf("  Stop: kill $(cat %s)\n", pidPath)

	// The parent's work is done; exit cleanly.
	os.Exit(0)
	return nil // unreachable
}
