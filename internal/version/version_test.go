package version

import (
	"strings"
	"testing"
)

func TestShort(t *testing.T) {
	// Save original values
	origVersion := Version
	origCommit := Commit
	origBuiltAt := BuiltAt
	defer func() {
		Version = origVersion
		Commit = origCommit
		BuiltAt = origBuiltAt
	}()

	tests := []struct {
		name     string
		version  string
		commit   string
		builtAt  string
		expected string
	}{
		{
			name:     "standard version",
			version:  "v1.2.0",
			commit:   "abc1234",
			builtAt:  "2025-01-01T00:00:00Z",
			expected: "v1.2.0 (abc1234) built 2025-01-01T00:00:00Z",
		},
		{
			name:     "dev version",
			version:  "dev",
			commit:   "unknown",
			builtAt:  "unknown",
			expected: "dev (unknown) built unknown",
		},
		{
			name:     "empty values",
			version:  "",
			commit:   "",
			builtAt:  "",
			expected: " () built ",
		},
		{
			name:     "long commit hash",
			version:  "v2.0.0",
			commit:   "abcdef1234567890abcdef1234567890abcdef12",
			builtAt:  "2025-12-31T23:59:59Z",
			expected: "v2.0.0 (abcdef1234567890abcdef1234567890abcdef12) built 2025-12-31T23:59:59Z",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version = tt.version
			Commit = tt.commit
			BuiltAt = tt.builtAt

			got := Short()
			if got != tt.expected {
				t.Errorf("Short() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFull(t *testing.T) {
	// Save original values
	origVersion := Version
	origCommit := Commit
	origBuiltAt := BuiltAt
	defer func() {
		Version = origVersion
		Commit = origCommit
		BuiltAt = origBuiltAt
	}()

	// Set test values
	Version = "v1.0.0"
	Commit = "testcommit"
	BuiltAt = "2025-06-01T12:00:00Z"

	binary := "sproxy"
	got := Full(binary)

	// Check that output contains expected fields
	if !strings.Contains(got, binary) {
		t.Errorf("Full() output missing binary name %q", binary)
	}
	if !strings.Contains(got, Version) {
		t.Errorf("Full() output missing version %q", Version)
	}
	if !strings.Contains(got, Commit) {
		t.Errorf("Full() output missing commit %q", Commit)
	}
	if !strings.Contains(got, BuiltAt) {
		t.Errorf("Full() output missing built time %q", BuiltAt)
	}
	if !strings.Contains(got, "go version") && !strings.Contains(got, "go1.") {
		// Go version should be included in runtime info
		t.Error("Full() output missing go version info")
	}
	if !strings.Contains(got, "os/arch") {
		t.Error("Full() output missing os/arch info")
	}
}

func TestFull_MultilineFormat(t *testing.T) {
	origVersion, origCommit, origBuiltAt := Version, Commit, BuiltAt
	defer func() { Version, Commit, BuiltAt = origVersion, origCommit, origBuiltAt }()
	Version = "v1.0.0"
	Commit = "abc123"
	BuiltAt = "2025-01-01T00:00:00Z"

	got := Full("cproxy")
	lines := strings.Split(got, "\n")

	// Should have 6 lines: binary name + 5 info lines
	if len(lines) != 6 {
		t.Errorf("Full() returned %d lines, expected 6", len(lines))
	}

	// First line should be just the binary name
	if lines[0] != "cproxy" {
		t.Errorf("First line = %q, want 'cproxy'", lines[0])
	}
}

func TestDefaultValues(t *testing.T) {
	// Test that default values are set correctly
	if Version != "dev" {
		t.Errorf("Default Version = %q, want 'dev'", Version)
	}
	if Commit != "unknown" {
		t.Errorf("Default Commit = %q, want 'unknown'", Commit)
	}
	if BuiltAt != "unknown" {
		t.Errorf("Default BuiltAt = %q, want 'unknown'", BuiltAt)
	}
}
