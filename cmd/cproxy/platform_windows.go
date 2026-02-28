//go:build windows

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	winsvcName        = "CProxy"
	winsvcDisplayName = "PairProxy CProxy"
	winsvcDesc        = "PairProxy client-side proxy — intercepts Claude Code requests and forwards to s-proxy"
)

var installServiceConfigFlag string

var installServiceCmd = &cobra.Command{
	Use:   "install-service",
	Short: "Install cproxy as a Windows background service (requires Administrator)",
	Long: `Install cproxy as a Windows service that starts automatically on boot.

IMPORTANT — Windows services run as LocalSystem by default, which cannot read
your user-profile token file (%APPDATA%\pairproxy\token.json).

Before running this command:

  1. Create the system-wide config and token directory:
       mkdir C:\ProgramData\pairproxy

  2. Create C:\ProgramData\pairproxy\cproxy.yaml and add:
       auth:
         token_dir: C:\ProgramData\pairproxy

  3. Log in once using that config to write the token there:
       cproxy login --server http://proxy.company.com:9000 ^
                    (then edit the saved token.json to token_dir above)
     Or just copy your token.json into C:\ProgramData\pairproxy\.

  4. Run this command as Administrator:
       cproxy install-service --config C:\ProgramData\pairproxy\cproxy.yaml

After installation:
  Start:  sc start CProxy
  Stop:   sc stop  CProxy
  Status: sc query CProxy
  Logs:   Windows Event Viewer > Windows Logs > Application`,
	RunE: runInstallService,
}

var uninstallServiceCmd = &cobra.Command{
	Use:   "uninstall-service",
	Short: "Remove the cproxy Windows service (requires Administrator)",
	Long:  `Stop and permanently remove the CProxy Windows service.`,
	RunE:  runUninstallService,
}

func init() {
	installServiceCmd.Flags().StringVar(&installServiceConfigFlag, "config", "",
		"path to cproxy.yaml accessible by the LocalSystem account")
	rootCmd.AddCommand(installServiceCmd, uninstallServiceCmd)
}

func runInstallService(cmd *cobra.Command, args []string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (try running as Administrator): %w", err)
	}
	defer m.Disconnect()

	// Fail fast if service already exists.
	if s, openErr := m.OpenService(winsvcName); openErr == nil {
		s.Close()
		return fmt.Errorf("service %q already installed; run 'cproxy uninstall-service' first", winsvcName)
	}

	// Build the command line the SCM will invoke: cproxy start [--config <path>]
	startArgs := []string{"start"}
	if installServiceConfigFlag != "" {
		startArgs = append(startArgs, "--config", installServiceConfigFlag)
	}

	s, err := m.CreateService(winsvcName, exePath, mgr.Config{
		DisplayName: winsvcDisplayName,
		Description: winsvcDesc,
		StartType:   mgr.StartAutomatic,
	}, startArgs...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	// Configure automatic restart on failure (up to 3 attempts with increasing delay).
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400); err != nil {
		fmt.Printf("Warning: could not set recovery actions: %v\n", err)
	}

	// Register event log source so the service can write to Event Viewer.
	_ = eventlog.InstallAsEventCreate(winsvcName, eventlog.Error|eventlog.Warning|eventlog.Info)

	fmt.Printf("✓ Service %q installed successfully.\n", winsvcName)
	fmt.Printf("  Binary: %s\n", exePath)
	if installServiceConfigFlag != "" {
		fmt.Printf("  Config: %s\n", installServiceConfigFlag)
	}
	fmt.Println()
	fmt.Printf("  Start:  sc start %s\n", winsvcName)
	fmt.Printf("  Stop:   sc stop  %s\n", winsvcName)
	fmt.Printf("  Status: sc query %s\n", winsvcName)
	return nil
}

func runUninstallService(cmd *cobra.Command, args []string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (try running as Administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(winsvcName)
	if err != nil {
		return fmt.Errorf("service %q not found: %w", winsvcName, err)
	}
	defer s.Close()

	// Stop the service first if it is running.
	if status, qErr := s.Query(); qErr == nil && status.State == svc.Running {
		if _, stopErr := s.Control(svc.Stop); stopErr != nil {
			fmt.Printf("Warning: could not stop service: %v\n", stopErr)
		} else {
			// Wait up to 10 s for the service to reach Stopped state.
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if st, e := s.Query(); e != nil || st.State == svc.Stopped {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
		}
	}

	if err := s.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	_ = eventlog.Remove(winsvcName)

	fmt.Printf("✓ Service %q removed successfully.\n", winsvcName)
	return nil
}

// isWindowsService reports whether the process was launched by the Windows SCM.
func isWindowsService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

// runAsWindowsService hands the HTTP server off to the Windows Service Control Manager.
// It blocks until the SCM requests Stop or Shutdown.
func runAsWindowsService(server *http.Server, logger *zap.Logger) error {
	return svc.Run(winsvcName, &cproxyService{server: server, logger: logger})
}

// cproxyService implements svc.Handler so the SCM can start and stop cproxy.
type cproxyService struct {
	server *http.Server
	logger *zap.Logger
}

func (s *cproxyService) Execute(_ []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	elog, _ := eventlog.Open(winsvcName)
	if elog != nil {
		defer elog.Close()
		_ = elog.Info(1, "cproxy service starting on "+s.server.Addr)
	}

	// Start the HTTP server in a goroutine.
	serverErr := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Signal that we are running and accept Stop / Shutdown requests.
	status <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}
	if elog != nil {
		_ = elog.Info(1, "cproxy service running")
	}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := s.server.Shutdown(ctx); err != nil {
					s.logger.Error("graceful shutdown error", zap.Error(err))
					if elog != nil {
						_ = elog.Error(1, fmt.Sprintf("shutdown error: %v", err))
					}
				}
				if elog != nil {
					_ = elog.Info(1, "cproxy service stopped")
				}
				return false, 0
			default:
				// Acknowledge any other request and remain running.
				status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			}

		case err := <-serverErr:
			s.logger.Error("HTTP server fatal error", zap.Error(err))
			if elog != nil {
				_ = elog.Error(1, fmt.Sprintf("server fatal error: %v", err))
			}
			return true, 1
		}
	}
}

// daemonize is not supported on Windows; the Windows service subsystem is used instead.
func daemonize(_ string) error {
	return fmt.Errorf("--daemon is not supported on Windows; use 'cproxy install-service' to run cproxy as a background service")
}
