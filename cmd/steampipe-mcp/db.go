package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type QueryResult struct {
	Rows             []map[string]any `json:"rows"`
	RowCount         int              `json:"row_count"`
	Truncated        bool             `json:"truncated"`
	TruncationReason string           `json:"truncation_reason,omitempty"`
	Hint             string           `json:"hint,omitempty"`
}

// NewConnectionPool instantiates and pings the pgx connection pool, applying statement timeouts.
func NewConnectionPool(ctx context.Context, cfg *Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database URL: %w", err)
	}

	// Override password if specified separately
	if cfg.DatabasePassword != "" {
		poolCfg.ConnConfig.Password = cfg.DatabasePassword
	}

	// Set session level timeout limit on connection establishment
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		// Use set_config to prevent SQL injection in statement_timeout
		_, err := conn.Exec(ctx,
			"SELECT set_config('statement_timeout', $1, false)",
			strconv.Itoa(cfg.StatementTimeoutMs))
		if err != nil {
			return fmt.Errorf("failed to configure statement timeout: %w", err)
		}
		return nil
	}

	// Optimize pool for Steampipe's intermittent connections
	poolCfg.MaxConnIdleTime = 5 * time.Minute
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Explicitly ping the database to verify reachability
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to reach database: %w", err)
	}

	return pool, nil
}

// ExecuteReadOnlyQuery runs a query within a READ ONLY transaction, applying row-level truncation limits.
func ExecuteReadOnlyQuery(ctx context.Context, pool *pgxpool.Pool, query string, rowLimit int, args ...any) (*QueryResult, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("failed to start read-only transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []map[string]any
	colDescriptions := rows.FieldDescriptions()

	truncated := false
	truncationReason := ""
	rowCount := 0

	for rows.Next() {
		if rowLimit > 0 && rowCount >= rowLimit {
			truncated = true
			truncationReason = "row_cap"
			break
		}

		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("failed to parse row values: %w", err)
		}

		rowMap := make(map[string]any)
		for i, col := range colDescriptions {
			rowMap[col.Name] = values[i]
		}
		results = append(results, rowMap)
		rowCount++
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit read-only transaction: %w", err)
	}

	res := &QueryResult{
		Rows:             results,
		RowCount:         rowCount,
		Truncated:        truncated,
		TruncationReason: truncationReason,
	}

	if truncated {
		res.Hint = fmt.Sprintf("Returned the first %d rows. Add a WHERE clause, project specific columns, or include LIMIT/OFFSET in your SQL to page through more.", rowLimit)
	}

	return res, nil
}

// EnforcePayloadLimit handles serialization-time truncation if the JSON representation exceeds payload limits.
func EnforcePayloadLimit(res *QueryResult, maxBytes int64) (*QueryResult, error) {
	if maxBytes <= 0 || len(res.Rows) == 0 {
		return res, nil
	}

	data, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize results: %w", err)
	}

	if int64(len(data)) <= maxBytes {
		return res, nil
	}

	// Payload exceeds maxBytes. Binary search or step backwards to drop trailing rows until it fits.
	truncatedRows := make([]map[string]any, len(res.Rows))
	copy(truncatedRows, res.Rows)

	low := 0
	high := len(truncatedRows) - 1
	bestSize := 0
	var finalRows []map[string]any

	for low <= high {
		mid := (low + high) / 2
		testRes := &QueryResult{
			Rows:             truncatedRows[:mid+1],
			RowCount:         mid + 1,
			Truncated:        true,
			TruncationReason: "payload_cap",
			Hint:             "Returned fewer rows to comply with transport payload limits. Add a WHERE clause, project specific columns, or include LIMIT/OFFSET in your SQL to page through more.",
		}

		testData, err := json.Marshal(testRes)
		if err != nil {
			return nil, err
		}

		if int64(len(testData)) <= maxBytes {
			bestSize = mid + 1
			finalRows = testRes.Rows
			low = mid + 1 // try to fit more rows
		} else {
			high = mid - 1 // try fewer rows
		}
	}

	if bestSize == 0 {
		// If even 1 row exceeds maxBytes, return an empty array with truncation info
		return &QueryResult{
			Rows:             []map[string]any{},
			RowCount:         0,
			Truncated:        true,
			TruncationReason: "payload_cap",
			Hint:             "No rows could be returned because even a single row exceeds transport payload limits. Select fewer columns or narrow your query.",
		}, nil
	}

	return &QueryResult{
		Rows:             finalRows,
		RowCount:         bestSize,
		Truncated:        true,
		TruncationReason: "payload_cap",
		Hint:             "Returned fewer rows to comply with transport payload limits. Add a WHERE clause, project specific columns, or include LIMIT/OFFSET in your SQL to page through more.",
	}, nil
}
