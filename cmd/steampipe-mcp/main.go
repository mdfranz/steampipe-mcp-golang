package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/unix"
)

func main() {
	startTime := time.Now()

	// 1. Load config
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	// 2. Set up logging
	logCloser, err := setupLogger(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Logger setup failed: %v\n", err)
		os.Exit(1)
	}
	if logCloser != nil {
		defer logCloser.Close()
	}

	slog.Info("Starting Steampipe Go MCP Server",
		"version", "0.1.0",
		"database", SanitizeDatabaseURL(cfg.DatabaseURL),
	)

	// 3. Single-instance lock
	lockCleanup, err := acquireLock(cfg.LockFile)
	if err != nil {
		slog.Error("Failed to acquire process lock", "error", err)
		os.Exit(1)
	}
	defer lockCleanup()

	// 4. Setup graceful shutdown context
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 5. Connect to Steampipe
	pool, err := NewConnectionPool(ctx, cfg)
	if err != nil {
		slog.Error("Database connection pool failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		slog.Info("Closing database connection pool")
		pool.Close()
	}()

	// 6. Instantiate Server telemetry tracker
	sr := NewStatusResource(cfg, pool, startTime)

	// 7. Instantiate MCP Server
	impl := &mcp.Implementation{
		Name:    "steampipe-mcp-golang",
		Version: "0.1.0",
	}
	serverOpts := &mcp.ServerOptions{
		Instructions: "Recommended flow: steampipe_plugin_list -> steampipe_table_list -> steampipe_table_show -> steampipe_query. Never run SELECT *; always include LIMIT for explorative queries.",
		Logger:       slog.Default(),
	}
	server := mcp.NewServer(impl, serverOpts)

	// 8. Register Prompt, Status Resource, and Tools
	RegisterPrompt(server)
	sr.Register(server)
	RegisterTools(server, pool, sr)

	// 9. Run Server Loop on Stdio
	slog.Info("JSON-RPC server running on stdio transport loop")
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Run(ctx, &mcp.StdioTransport{})
	}()

	select {
	case <-ctx.Done():
		slog.Info("Graceful shutdown initiated by signal")
	case runErr := <-errChan:
		if runErr != nil {
			slog.Error("Server execution failed", "error", runErr)
			os.Exit(1)
		}
	}

	slog.Info("Shutdown complete")
}

func setupLogger(cfg *Config) (io.Closer, error) {
	level := slog.LevelInfo
	if cfg.Debug {
		level = slog.LevelDebug
	}

	logPath := cfg.LogFile
	if logPath == "" {
		logPath = "steampipe-mcp.log"
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	return f, nil
}

func acquireLock(lockFile string) (func(), error) {
	cleanup := func() {} // always safe to defer
	if strings.EqualFold(lockFile, "off") {
		return cleanup, nil
	}

	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return cleanup, fmt.Errorf("unable to open lock file: %w", err)
	}

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		existing, _ := os.ReadFile(lockFile)
		_ = f.Close()
		return cleanup, fmt.Errorf("another instance is running (pid file: %q): %w",
			strings.TrimSpace(string(existing)), err)
	}

	if err := f.Truncate(0); err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return cleanup, fmt.Errorf("unable to truncate lock file: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		return cleanup, fmt.Errorf("unable to write PID: %w", err)
	}

	return func() {
		slog.Debug("Releasing PID lock file", "path", lockFile)
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
		_ = os.Remove(lockFile)
	}, nil
}
