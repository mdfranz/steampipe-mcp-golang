package main

import (
	"os"
	"testing"
)

func TestLoadConfig_Defaults(t *testing.T) {
	// Clear any relevant environment variables first
	os.Unsetenv("STEAMPIPE_MCP_WORKSPACE_DATABASE")
	os.Unsetenv("STEAMPIPE_MCP_WORKSPACE_DATABASE_PASSWORD")
	os.Unsetenv("PGPASSWORD")
	os.Unsetenv("STEAMPIPE_MCP_LOGFILE")
	os.Unsetenv("STEAMPIPE_MCP_DEBUG")
	os.Unsetenv("STEAMPIPE_MCP_LOCKFILE")
	os.Unsetenv("STEAMPIPE_MCP_ROW_LIMIT")
	os.Unsetenv("STEAMPIPE_MCP_STATEMENT_TIMEOUT_MS")
	os.Unsetenv("STEAMPIPE_MCP_PAYLOAD_LIMIT_BYTES")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}

	if cfg.DatabaseURL != DefaultDatabaseURL {
		t.Errorf("expected DatabaseURL %q, got %q", DefaultDatabaseURL, cfg.DatabaseURL)
	}
	if cfg.DatabasePassword != "" {
		t.Errorf("expected empty password, got %q", cfg.DatabasePassword)
	}
	if cfg.LockFile != DefaultLockFile {
		t.Errorf("expected LockFile %q, got %q", DefaultLockFile, cfg.LockFile)
	}
	if cfg.RowLimit != DefaultRowLimit {
		t.Errorf("expected RowLimit %d, got %d", DefaultRowLimit, cfg.RowLimit)
	}
	if cfg.StatementTimeoutMs != DefaultStatementTimeoutMs {
		t.Errorf("expected StatementTimeoutMs %d, got %d", DefaultStatementTimeoutMs, cfg.StatementTimeoutMs)
	}
	if cfg.PayloadLimitBytes != DefaultPayloadLimitBytes {
		t.Errorf("expected PayloadLimitBytes %d, got %d", DefaultPayloadLimitBytes, cfg.PayloadLimitBytes)
	}
	if cfg.Debug {
		t.Error("expected Debug to be false by default")
	}
}

func TestLoadConfig_Overrides(t *testing.T) {
	os.Setenv("STEAMPIPE_MCP_WORKSPACE_DATABASE", "postgresql://user@remote:1234/testdb")
	os.Setenv("STEAMPIPE_MCP_WORKSPACE_DATABASE_PASSWORD", "secret_pass")
	os.Setenv("STEAMPIPE_MCP_LOGFILE", "/var/log/steampipe-mcp.log")
	os.Setenv("STEAMPIPE_MCP_DEBUG", "1")
	os.Setenv("STEAMPIPE_MCP_LOCKFILE", "custom.lock")
	os.Setenv("STEAMPIPE_MCP_ROW_LIMIT", "500")
	os.Setenv("STEAMPIPE_MCP_STATEMENT_TIMEOUT_MS", "5000")
	os.Setenv("STEAMPIPE_MCP_PAYLOAD_LIMIT_BYTES", "2048")

	defer func() {
		os.Unsetenv("STEAMPIPE_MCP_WORKSPACE_DATABASE")
		os.Unsetenv("STEAMPIPE_MCP_WORKSPACE_DATABASE_PASSWORD")
		os.Unsetenv("STEAMPIPE_MCP_LOGFILE")
		os.Unsetenv("STEAMPIPE_MCP_DEBUG")
		os.Unsetenv("STEAMPIPE_MCP_LOCKFILE")
		os.Unsetenv("STEAMPIPE_MCP_ROW_LIMIT")
		os.Unsetenv("STEAMPIPE_MCP_STATEMENT_TIMEOUT_MS")
		os.Unsetenv("STEAMPIPE_MCP_PAYLOAD_LIMIT_BYTES")
	}()

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}

	if cfg.DatabaseURL != "postgresql://user@remote:1234/testdb" {
		t.Errorf("got DatabaseURL %q", cfg.DatabaseURL)
	}
	if cfg.DatabasePassword != "secret_pass" {
		t.Errorf("got DatabasePassword %q", cfg.DatabasePassword)
	}
	if cfg.LogFile != "/var/log/steampipe-mcp.log" {
		t.Errorf("got LogFile %q", cfg.LogFile)
	}
	if !cfg.Debug {
		t.Error("expected Debug to be true")
	}
	if cfg.LockFile != "custom.lock" {
		t.Errorf("got LockFile %q", cfg.LockFile)
	}
	if cfg.RowLimit != 500 {
		t.Errorf("got RowLimit %d", cfg.RowLimit)
	}
	if cfg.StatementTimeoutMs != 5000 {
		t.Errorf("got StatementTimeoutMs %d", cfg.StatementTimeoutMs)
	}
	if cfg.PayloadLimitBytes != 2048 {
		t.Errorf("got PayloadLimitBytes %d", cfg.PayloadLimitBytes)
	}
}

func TestLoadConfig_PasswordFallback(t *testing.T) {
	os.Unsetenv("STEAMPIPE_MCP_WORKSPACE_DATABASE_PASSWORD")
	os.Setenv("PGPASSWORD", "pg_fallback_pass")
	defer os.Unsetenv("PGPASSWORD")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DatabasePassword != "pg_fallback_pass" {
		t.Errorf("expected password fallback to PGPASSWORD, got %q", cfg.DatabasePassword)
	}
}

func TestSanitizeDatabaseURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "postgresql://steampipe@localhost:9193/steampipe",
			expected: "postgresql://steampipe@localhost:9193/steampipe",
		},
		{
			input:    "postgresql://steampipe:supersecret@localhost:9193/steampipe",
			expected: "postgresql://steampipe:xxxxx@localhost:9193/steampipe",
		},
		{
			input:    "postgresql://user:pass:word@localhost:9193/steampipe", // complex
			expected: "postgresql://user:xxxxx@localhost:9193/steampipe",
		},
		{
			input:    "user:password@localhost:9193/db",
			expected: "user:xxxxx@localhost:9193/db",
		},
	}

	for _, tc := range tests {
		got := SanitizeDatabaseURL(tc.input)
		if got != tc.expected {
			t.Errorf("SanitizeDatabaseURL(%q) = %q, expected %q", tc.input, got, tc.expected)
		}
	}
}
