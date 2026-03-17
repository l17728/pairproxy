package preflight

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/l17728/pairproxy/internal/config"
)

// ---------------------------------------------------------------------------
// checkWritable — auto-creates parent directory
// ---------------------------------------------------------------------------

func TestCheckWritable_AutoCreatesParentDir(t *testing.T) {
	base := t.TempDir()
	// Use a deeply nested path that doesn't exist yet
	filePath := filepath.Join(base, "a", "b", "c", "test.db")
	if err := checkWritable(filePath); err != nil {
		t.Errorf("checkWritable should create parent dirs, got: %v", err)
	}
	// Parent dir should now exist
	if _, err := os.Stat(filepath.Dir(filePath)); err != nil {
		t.Errorf("parent dir should exist after checkWritable: %v", err)
	}
}

// ---------------------------------------------------------------------------
// checkWritable — read-only directory (Unix only)
// ---------------------------------------------------------------------------

func TestCheckWritable_ReadOnlyDir_ReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only directory test not reliable on Windows")
	}

	base := t.TempDir()
	roDir := filepath.Join(base, "readonly")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make it read-only
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(roDir, 0o755) //nolint:errcheck // cleanup

	filePath := filepath.Join(roDir, "test.db")
	if err := checkWritable(filePath); err == nil {
		t.Error("checkWritable on read-only dir should return error")
	}
}

// ---------------------------------------------------------------------------
// CheckSProxy — empty database path (skips write check)
// ---------------------------------------------------------------------------

func TestCheckSProxy_EmptyDBPath_SkipsWriteCheck(t *testing.T) {
	logger := zaptest.NewLogger(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket")
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cfg := &config.SProxyFullConfig{}
	cfg.Listen.Host = "127.0.0.1"
	cfg.Listen.Port = port
	cfg.Database.Path = "" // empty path → skips write check

	// Should pass (no write check needed when path is "")
	if err := CheckSProxy(logger, cfg); err != nil {
		t.Errorf("CheckSProxy with empty db path: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CheckSProxy — :memory: database path (skips write check)
// ---------------------------------------------------------------------------

func TestCheckSProxy_MemoryDB_Passes(t *testing.T) {
	logger := zaptest.NewLogger(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket")
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cfg := &config.SProxyFullConfig{}
	cfg.Listen.Host = "127.0.0.1"
	cfg.Listen.Port = port
	cfg.Database.Path = ":memory:"

	if err := CheckSProxy(logger, cfg); err != nil {
		t.Errorf("CheckSProxy with :memory:: unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CheckSProxy — both db path check AND port check fail (aggregated error)
// ---------------------------------------------------------------------------

func TestCheckSProxy_BothChecksFailAggregatesErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only directory test not reliable on Windows")
	}

	logger := zaptest.NewLogger(t)

	// Build a read-only directory for the DB path
	base := t.TempDir()
	roDir := filepath.Join(base, "readonly")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(roDir, 0o755) //nolint:errcheck

	// Occupy a port so the port check also fails
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket")
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := &config.SProxyFullConfig{}
	cfg.Listen.Host = "127.0.0.1"
	cfg.Listen.Port = port
	cfg.Database.Path = filepath.Join(roDir, "test.db")

	err = CheckSProxy(logger, cfg)
	if err == nil {
		t.Fatal("CheckSProxy should fail when both db and port checks fail")
	}
}

// ---------------------------------------------------------------------------
// CheckCProxy — aggregated error when port is in use
// ---------------------------------------------------------------------------

func TestCheckCProxy_PortInUse_ReturnsError(t *testing.T) {
	logger := zaptest.NewLogger(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket")
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	cfg := &config.CProxyConfig{}
	cfg.Listen.Host = "127.0.0.1"
	cfg.Listen.Port = port

	if err := CheckCProxy(logger, cfg); err == nil {
		t.Fatal("CheckCProxy should fail when port is in use")
	}
}

// ---------------------------------------------------------------------------
// checkPortAvailable — loopback address
// ---------------------------------------------------------------------------

func TestCheckPortAvailable_LoopbackFreePort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket")
	}
	addr := ln.Addr().String()
	ln.Close()

	if err := checkPortAvailable(addr); err != nil {
		t.Errorf("checkPortAvailable(%q) on free loopback port: unexpected error: %v", addr, err)
	}
}

// ---------------------------------------------------------------------------
// CheckSProxy — valid temp dir path
// ---------------------------------------------------------------------------

func TestCheckSProxy_ValidTempPath(t *testing.T) {
	logger := zaptest.NewLogger(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test socket")
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	cfg := &config.SProxyFullConfig{}
	cfg.Listen.Host = "127.0.0.1"
	cfg.Listen.Port = port
	cfg.Database.Path = filepath.Join(t.TempDir(), "sproxy.db")

	if err := CheckSProxy(logger, cfg); err != nil {
		t.Errorf("CheckSProxy: unexpected error: %v", err)
	}
}
