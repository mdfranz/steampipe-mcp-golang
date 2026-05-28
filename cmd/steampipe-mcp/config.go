package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL        string
	DatabasePassword   string
	LogFile            string
	Debug              bool
	LockFile           string
	RowLimit           int
	StatementTimeoutMs int
	PayloadLimitBytes  int64
}

// Default values
const (
	DefaultDatabaseURL        = "postgresql://steampipe@localhost:9193/steampipe"
	DefaultLockFile           = "steampipe-mcp.lock"
	DefaultRowLimit           = 1000
	DefaultStatementTimeoutMs = 120000
	DefaultPayloadLimitBytes  = 1048576 // 1 MiB
)

// LoadConfig loads server configuration from environment variables and sets defaults.
func LoadConfig() (*Config, error) {
	dbURL := os.Getenv("STEAMPIPE_MCP_WORKSPACE_DATABASE")
	if dbURL == "" {
		dbURL = DefaultDatabaseURL
	}

	dbPassword := os.Getenv("STEAMPIPE_MCP_WORKSPACE_DATABASE_PASSWORD")
	if dbPassword == "" {
		dbPassword = os.Getenv("PGPASSWORD")
	}

	logFile := os.Getenv("STEAMPIPE_MCP_LOGFILE")

	debugVal := os.Getenv("STEAMPIPE_MCP_DEBUG")
	debug := debugVal == "1" || strings.ToLower(debugVal) == "debug" || os.Getenv("APP_DEBUG") != ""

	lockFile := os.Getenv("STEAMPIPE_MCP_LOCKFILE")
	if lockFile == "" {
		lockFile = DefaultLockFile
	}

	rowLimit := DefaultRowLimit
	if limitStr := os.Getenv("STEAMPIPE_MCP_ROW_LIMIT"); limitStr != "" {
		if val, err := strconv.Atoi(limitStr); err == nil && val > 0 {
			rowLimit = val
		}
	}

	timeoutMs := DefaultStatementTimeoutMs
	if timeoutStr := os.Getenv("STEAMPIPE_MCP_STATEMENT_TIMEOUT_MS"); timeoutStr != "" {
		if val, err := strconv.Atoi(timeoutStr); err == nil && val > 0 {
			timeoutMs = val
		}
	}

	payloadLimit := int64(DefaultPayloadLimitBytes)
	if payloadStr := os.Getenv("STEAMPIPE_MCP_PAYLOAD_LIMIT_BYTES"); payloadStr != "" {
		if val, err := strconv.ParseInt(payloadStr, 10, 64); err == nil && val > 0 {
			payloadLimit = val
		}
	}

	return &Config{
		DatabaseURL:        dbURL,
		DatabasePassword:   dbPassword,
		LogFile:            logFile,
		Debug:              debug,
		LockFile:           lockFile,
		RowLimit:           rowLimit,
		StatementTimeoutMs: timeoutMs,
		PayloadLimitBytes:  payloadLimit,
	}, nil
}

// SanitizeDatabaseURL returns a sanitized version of the database URL suitable for logging/status resources.
func SanitizeDatabaseURL(rawURL string) string {
	// If it's a URL with scheme, use url.Parse
	if strings.Contains(rawURL, "://") {
		u, err := url.Parse(rawURL)
		if err == nil {
			if u.User != nil {
				if _, has := u.User.Password(); has {
					u.User = url.UserPassword(u.User.Username(), "xxxxx")
				}
			}
			return u.String()
		}
	}

	// For non-scheme strings, do key-value or pattern-based masking.
	return maskPasswordInString(rawURL)
}

func maskPasswordInString(s string) string {
	// Case 1: user:password@host/db
	if strings.Contains(s, "@") {
		parts := strings.SplitN(s, "@", 2)
		left := parts[0]
		// Find the last colon in left to separate username from password
		if idx := strings.LastIndex(left, ":"); idx != -1 {
			user := left[:idx]
			return fmt.Sprintf("%s:xxxxx@%s", user, parts[1])
		}
	}

	// Case 2: key-value DSN, e.g., "host=localhost password=secret user=steampipe"
	// Replace password=val with password=xxxxx
	words := strings.Fields(s)
	hasChanges := false
	for i, w := range words {
		if strings.HasPrefix(w, "password=") {
			words[i] = "password=xxxxx"
			hasChanges = true
		} else if strings.HasPrefix(w, "pass=") {
			words[i] = "pass=xxxxx"
			hasChanges = true
		}
	}
	if hasChanges {
		return strings.Join(words, " ")
	}

	return s
}
