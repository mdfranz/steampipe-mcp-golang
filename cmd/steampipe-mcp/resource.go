package main

import (
	"context"
	"encoding/json"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type StatusResource struct {
	cfg          *Config
	pool         *pgxpool.Pool
	startedAt    time.Time
	lastQueryAt  atomic.Pointer[time.Time]
	lastDuration atomic.Int64 // in milliseconds
	lastRowCount atomic.Int64
	lastTrunc    atomic.Bool
}

// NewStatusResource constructs a thread-safe telemetry structure for the steampipe://status resource.
func NewStatusResource(cfg *Config, pool *pgxpool.Pool, startedAt time.Time) *StatusResource {
	return &StatusResource{
		cfg:       cfg,
		pool:      pool,
		startedAt: startedAt,
	}
}

// RecordQuery updates the telemetry of the last executed query.
func (sr *StatusResource) RecordQuery(duration time.Duration, rowsReturned int, truncated bool) {
	now := time.Now()
	sr.lastQueryAt.Store(&now)
	sr.lastDuration.Store(duration.Milliseconds())
	sr.lastRowCount.Store(int64(rowsReturned))
	sr.lastTrunc.Store(truncated)
}

// Register registers the 'steampipe://status' resource on the MCP server.
func (sr *StatusResource) Register(server *mcp.Server) {
	res := &mcp.Resource{
		URI:      "steampipe://status",
		Name:     "Steampipe Server Status",
		MIMEType: "application/json",
	}

	server.AddResource(res, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		statusData := sr.getStatus(ctx)
		jsonData, err := json.MarshalIndent(statusData, "", "  ")
		if err != nil {
			return nil, err
		}

		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{
				{
					URI:      "steampipe://status",
					MIMEType: "application/json",
					Text:     string(jsonData),
				},
			},
		}, nil
	})
}

type databaseStatus struct {
	ConnectionString   string `json:"connection_string"`
	Reachable          bool   `json:"reachable"`
	StatementTimeoutMs int    `json:"statement_timeout_ms"`
	Pool               struct {
		Acquired int32 `json:"acquired"`
		Idle     int32 `json:"idle"`
		Max      int32 `json:"max"`
	} `json:"pool"`
}

type steampipeStatus struct {
	PluginCount int `json:"plugin_count"`
	TableCount  int `json:"table_count"`
}

type lastQueryStatus struct {
	At           string `json:"at"`
	DurationMs   int64  `json:"duration_ms"`
	RowsReturned int64  `json:"rows_returned"`
	Truncated    bool   `json:"truncated"`
}

type limitsStatus struct {
	RowLimit          int   `json:"row_limit"`
	PayloadLimitBytes int64 `json:"payload_limit_bytes"`
}

type statusPayload struct {
	ServerVersion string           `json:"server_version"`
	GoVersion     string           `json:"go_version"`
	StartedAt     string           `json:"started_at"`
	UptimeSeconds int64            `json:"uptime_seconds"`
	Database      databaseStatus   `json:"database"`
	Steampipe     steampipeStatus  `json:"steampipe"`
	LastQuery     *lastQueryStatus `json:"last_query,omitempty"`
	Limits        limitsStatus     `json:"limits"`
}

func (sr *StatusResource) getStatus(ctx context.Context) *statusPayload {
	payload := &statusPayload{
		ServerVersion: "0.1.0",
		GoVersion:     runtime.Version(),
		StartedAt:     sr.startedAt.Format(time.RFC3339),
		UptimeSeconds: int64(time.Since(sr.startedAt).Seconds()),
	}

	// 1. Database Info & Reachability
	dbStat := databaseStatus{
		ConnectionString:   SanitizeDatabaseURL(sr.cfg.DatabaseURL),
		StatementTimeoutMs: sr.cfg.StatementTimeoutMs,
	}

	// Double-check active reachability
	if err := sr.pool.Ping(ctx); err == nil {
		dbStat.Reachable = true
	} else {
		dbStat.Reachable = false
	}

	poolStats := sr.pool.Stat()
	dbStat.Pool.Acquired = poolStats.AcquiredConns()
	dbStat.Pool.Idle = poolStats.IdleConns()
	dbStat.Pool.Max = poolStats.MaxConns()
	payload.Database = dbStat

	// 2. Steampipe metadata (best effort)
	if dbStat.Reachable {
		var pluginCount int
		err := sr.pool.QueryRow(ctx, "SELECT count(distinct plugin) FROM steampipe_connection").Scan(&pluginCount)
		if err == nil {
			payload.Steampipe.PluginCount = pluginCount
		}

		var tableCount int
		err = sr.pool.QueryRow(ctx, "SELECT count(*) FROM steampipe_table").Scan(&tableCount)
		if err == nil {
			payload.Steampipe.TableCount = tableCount
		}
	}

	// 3. Last Query Telemetry
	if lqTime := sr.lastQueryAt.Load(); lqTime != nil {
		payload.LastQuery = &lastQueryStatus{
			At:           lqTime.Format(time.RFC3339),
			DurationMs:   sr.lastDuration.Load(),
			RowsReturned: sr.lastRowCount.Load(),
			Truncated:    sr.lastTrunc.Load(),
		}
	}

	// 4. Configuration Limits
	payload.Limits = limitsStatus{
		RowLimit:          sr.cfg.RowLimit,
		PayloadLimitBytes: sr.cfg.PayloadLimitBytes,
	}

	return payload
}
