//go:build windows

package main

import "os"

// notifySIGHUP 在 Windows 上 SIGHUP 不可用，此函数为空操作。
// On Windows, SIGHUP is not available; hot-reload via signal is a no-op.
// Use 'sproxy start' with a new process to reload configuration on Windows.
func notifySIGHUP(ch chan<- os.Signal) {
	// no-op on Windows
}
