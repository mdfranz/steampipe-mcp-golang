package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tableListArgs struct {
	Plugin string `json:"plugin,omitempty"`
}

type tableShowArgs struct {
	Table string `json:"table"`
}

type queryArgs struct {
	SQL string `json:"sql"`
}

// RegisterTools registers all Steampipe database discovery and query execution tools.
func RegisterTools(server *mcp.Server, pool *pgxpool.Pool, sr *StatusResource) {
	// 1. steampipe_plugin_list
	server.AddTool(&mcp.Tool{
		Name:        "steampipe_plugin_list",
		Description: "Discover connected Steampipe plugins and active database connections.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := ExecuteReadOnlyQuery(ctx, pool, "SELECT name, plugin, plugin_version FROM steampipe_connection", sr.cfg.RowLimit)
		if err != nil {
			return errorResult(formatDBError(err)), nil
		}

		markdown := formatPluginListMarkdown(res.Rows)
		return successResult(markdown), nil
	})

	// 2. steampipe_table_list
	server.AddTool(&mcp.Tool{
		Name:        "steampipe_table_list",
		Description: "List tables available in Steampipe. Optionally filter by plugin or connection name to keep the response small.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"plugin": {
					"type": "string",
					"description": "Optional plugin prefix or connection name to filter tables (e.g., 'aws' or 'github')"
				}
			},
			"additionalProperties": false
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args tableListArgs
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errorResult(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		query := "SELECT name, connection_name, description FROM steampipe_table"
		var queryArgs []any

		if args.Plugin != "" {
			query = "SELECT name, connection_name, description FROM steampipe_table WHERE connection_name = $1 OR name LIKE $2"
			queryArgs = append(queryArgs, args.Plugin, args.Plugin+"_%")
		}

		res, err := ExecuteReadOnlyQuery(ctx, pool, query, sr.cfg.RowLimit, queryArgs...)
		if err != nil {
			return errorResult(formatDBError(err)), nil
		}

		markdown := formatTableListMarkdown(res.Rows)
		return successResult(markdown), nil
	})

	// 3. steampipe_table_show
	server.AddTool(&mcp.Tool{
		Name:        "steampipe_table_show",
		Description: "Inspect a specific table's columns, types, and descriptions BEFORE writing SQL. Guessing column names will fail.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"table": {
					"type": "string",
					"description": "The exact name of the table to show (e.g. 'aws_s3_bucket')"
				}
			},
			"required": ["table"],
			"additionalProperties": false
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args tableShowArgs
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errorResult(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}
		if strings.TrimSpace(args.Table) == "" {
			return errorResult("Argument 'table' is required"), nil
		}

		// Retrieve table schema details from steampipe_column
		res, err := ExecuteReadOnlyQuery(ctx, pool,
			"SELECT name, type, description FROM steampipe_column WHERE table_name = $1 ORDER BY name",
			sr.cfg.RowLimit, args.Table)
		if err != nil {
			return errorResult(formatDBError(err)), nil
		}

		// Fallback to standard information_schema if no rows are returned by steampipe_column
		if len(res.Rows) == 0 {
			fallbackRes, err := ExecuteReadOnlyQuery(ctx, pool,
				"SELECT column_name AS name, data_type AS type, '' AS description FROM information_schema.columns WHERE table_name = $1 ORDER BY ordinal_position",
				sr.cfg.RowLimit, args.Table)
			if err != nil || len(fallbackRes.Rows) == 0 {
				return errorResult(fmt.Sprintf("Table %q not found or has no columns.", args.Table)), nil
			}
			res = fallbackRes
		}

		ddlAndMarkdown := formatTableShow(args.Table, res.Rows)
		return successResult(ddlAndMarkdown), nil
	})

	// 4. steampipe_query
	server.AddTool(&mcp.Tool{
		Name:        "steampipe_query",
		Description: "Execute a read-only SELECT query against Steampipe virtual tables. Always project specific columns (avoid SELECT *) and include a LIMIT clause.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"sql": {
					"type": "string",
					"description": "The read-only SELECT query to run"
				}
			},
			"required": ["sql"],
			"additionalProperties": false
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args queryArgs
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errorResult(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}
		if strings.TrimSpace(args.SQL) == "" {
			return errorResult("Argument 'sql' is required"), nil
		}

		startTime := time.Now()
		res, err := ExecuteReadOnlyQuery(ctx, pool, args.SQL, sr.cfg.RowLimit)
		duration := time.Since(startTime)

		if err != nil {
			sr.RecordQuery(duration, 0, false)
			return errorResult(formatDBError(err)), nil
		}

		// Enforce payload truncation caps before returning to host
		finalRes, err := EnforcePayloadLimit(res, sr.cfg.PayloadLimitBytes)
		if err != nil {
			return errorResult(fmt.Sprintf("Payload size guard failed: %v", err)), nil
		}

		sr.RecordQuery(duration, len(finalRes.Rows), finalRes.Truncated)

		jsonBytes, err := json.MarshalIndent(finalRes, "", "  ")
		if err != nil {
			return errorResult(fmt.Sprintf("Failed to serialize results to JSON: %v", err)), nil
		}

		return successResult(string(jsonBytes)), nil
	})
}

func successResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: text,
			},
		},
	}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: msg,
			},
		},
	}
}

func formatDBError(err error) string {
	msg := err.Error()

	if strings.Contains(msg, "statement_timeout") || strings.Contains(msg, "canceling statement due to statement timeout") {
		return "Query exceeded statement_timeout. Try a tighter WHERE, LIMIT, or fewer columns; or raise STEAMPIPE_MCP_STATEMENT_TIMEOUT_MS."
	}

	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "dial tcp") {
		return "Cannot reach Steampipe. Verify the service is running and STEAMPIPE_MCP_WORKSPACE_DATABASE points at it."
	}

	if strings.Contains(msg, "syntax error") {
		return fmt.Sprintf("SQL syntax error: %s", msg)
	}

	if strings.Contains(msg, "relation") && strings.Contains(msg, "does not exist") {
		return fmt.Sprintf("Relation not found: %s", msg)
	}

	return fmt.Sprintf("Database query execution error: %s", msg)
}

func formatPluginListMarkdown(rows []map[string]any) string {
	if len(rows) == 0 {
		return "No connected plugins found."
	}

	var sb strings.Builder
	sb.WriteString("### Connected Steampipe Plugins & Connections\n\n")
	sb.WriteString("| Connection Name | Plugin | Version |\n")
	sb.WriteString("| :--- | :--- | :--- |\n")

	for _, row := range rows {
		name := row["name"]
		plugin := row["plugin"]
		version := row["plugin_version"]
		sb.WriteString(fmt.Sprintf("| %v | %v | %v |\n", name, plugin, version))
	}

	return sb.String()
}

func formatTableListMarkdown(rows []map[string]any) string {
	if len(rows) == 0 {
		return "No tables found matching criteria."
	}

	var sb strings.Builder
	sb.WriteString("### Available Steampipe Tables\n\n")
	sb.WriteString("| Table Name | Connection | Description |\n")
	sb.WriteString("| :--- | :--- | :--- |\n")

	for _, row := range rows {
		name := row["name"]
		conn := row["connection_name"]
		desc := row["description"]
		if desc == nil {
			desc = ""
		}
		descStr := strings.ReplaceAll(fmt.Sprintf("%v", desc), "\n", " ")
		sb.WriteString(fmt.Sprintf("| %v | %v | %s |\n", name, conn, descStr))
	}

	return sb.String()
}

func formatTableShow(table string, rows []map[string]any) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("### Table Schema: %s\n\n", table))

	// Generate SQL DDL representation
	sb.WriteString("```sql\n")
	sb.WriteString(fmt.Sprintf("CREATE TABLE %s (\n", table))
	for i, row := range rows {
		name := row["name"]
		typ := row["type"]
		desc := row["description"]

		comma := ","
		if i == len(rows)-1 {
			comma = ""
		}

		descComment := ""
		if desc != nil && fmt.Sprintf("%v", desc) != "" {
			descComment = fmt.Sprintf(" -- %s", strings.ReplaceAll(fmt.Sprintf("%v", desc), "\n", " "))
		}

		sb.WriteString(fmt.Sprintf("  %v %v%s%s\n", name, typ, comma, descComment))
	}
	sb.WriteString(");\n")
	sb.WriteString("```\n\n")

	// Generate standard Markdown table
	sb.WriteString("#### Columns\n\n")
	sb.WriteString("| Column Name | Type | Description |\n")
	sb.WriteString("| :--- | :--- | :--- |\n")
	for _, row := range rows {
		name := row["name"]
		typ := row["type"]
		desc := row["description"]
		if desc == nil {
			desc = ""
		}
		descStr := strings.ReplaceAll(fmt.Sprintf("%v", desc), "\n", " ")
		sb.WriteString(fmt.Sprintf("| **%v** | `%v` | %s |\n", name, typ, descStr))
	}

	return sb.String()
}
