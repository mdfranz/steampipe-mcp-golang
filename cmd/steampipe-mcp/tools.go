package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type tableListArgs struct {
	Plugin string `json:"plugin,omitempty"`
}

type tableSearchArgs struct {
	Query  string `json:"query"`
	Plugin string `json:"plugin,omitempty"`
}

type tableShowArgs struct {
	Table string `json:"table"`
}

type queryArgs struct {
	SQL string `json:"sql"`
}

var missingRelationRe = regexp.MustCompile(`(?i)relation\s+"([^"]+)"\s+does not exist`)

// RegisterTools registers all Steampipe database discovery and query execution tools.
func RegisterTools(server *mcp.Server, pool *pgxpool.Pool, sr *StatusResource) {
	// 1. steampipe_plugin_list
	server.AddTool(&mcp.Tool{
		Name:        "steampipe_plugin_list",
		Description: "Discover connected Steampipe plugins and active database connections.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := `
			SELECT c.name, c.plugin, p.version AS plugin_version
			FROM steampipe_internal.steampipe_connection c
			LEFT JOIN steampipe_internal.steampipe_plugin p ON c.plugin_instance = p.plugin_instance
		`
		res, err := ExecuteReadOnlyQuery(ctx, pool, query, sr.cfg.RowLimit)
		if err != nil {
			// Fallback to simpler query on steampipe_connection
			fallbackQuery := "SELECT name, plugin FROM steampipe_connection"
			fallbackRes, err2 := ExecuteReadOnlyQuery(ctx, pool, fallbackQuery, sr.cfg.RowLimit)
			if err2 != nil {
				return errorResult(formatDBError(err)), nil
			}
			// Parse version from plugin string (e.g. plugin@version)
			for _, row := range fallbackRes.Rows {
				pluginStr, _ := row["plugin"].(string)
				version := ""
				if idx := strings.Index(pluginStr, "@"); idx != -1 {
					version = pluginStr[idx+1:]
				}
				row["plugin_version"] = version
			}
			res = fallbackRes
		} else {
			// Fill in any rows where plugin_version is null from the plugin name suffix
			for _, row := range res.Rows {
				if row["plugin_version"] == nil {
					pluginStr, _ := row["plugin"].(string)
					version := ""
					if idx := strings.Index(pluginStr, "@"); idx != -1 {
						version = pluginStr[idx+1:]
					}
					row["plugin_version"] = version
				}
			}
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

		query := `
			SELECT
				n.nspname AS connection_name,
				c.relname AS name,
				pd.description AS description
			FROM
				pg_catalog.pg_class c
			JOIN
				pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN
				pg_catalog.pg_description pd ON pd.objoid = c.oid AND pd.objsubid = 0
			WHERE
				c.relkind = 'f'
				AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'steampipe_internal', 'steampipe_command')
			ORDER BY
				connection_name,
				name
		`
		var queryArgs []any

		if args.Plugin != "" {
			query = `
				SELECT
					n.nspname AS connection_name,
					c.relname AS name,
					pd.description AS description
				FROM
					pg_catalog.pg_class c
				JOIN
					pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				LEFT JOIN
					pg_catalog.pg_description pd ON pd.objoid = c.oid AND pd.objsubid = 0
				WHERE
					c.relkind = 'f'
					AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'steampipe_internal', 'steampipe_command')
					AND (n.nspname = $1 OR c.relname LIKE $2)
				ORDER BY
					connection_name,
					name
			`
			queryArgs = append(queryArgs, args.Plugin, args.Plugin+"_%")
		}

		res, err := ExecuteReadOnlyQuery(ctx, pool, query, sr.cfg.RowLimit, queryArgs...)
		if err != nil {
			return errorResult(formatDBError(err)), nil
		}

		markdown := formatTableListMarkdown(res.Rows)
		return successResult(markdown), nil
	})

	// 2b. steampipe_table_search
	server.AddTool(&mcp.Tool{
		Name:        "steampipe_table_search",
		Description: "Search available Steampipe tables by keyword in table name or description (useful when you don't know the exact table name, e.g. 'load_balancer').",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {
					"type": "string",
					"description": "Substring to search for (case-insensitive), e.g. 'load_balancer' or 's3'"
				},
				"plugin": {
					"type": "string",
					"description": "Optional plugin prefix or connection name to filter tables (e.g., 'aws' or 'github')"
				}
			},
			"required": ["query"],
			"additionalProperties": false
		}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args tableSearchArgs
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errorResult(fmt.Sprintf("Invalid arguments: %v", err)), nil
		}

		if strings.TrimSpace(args.Query) == "" {
			return errorResult("Argument 'query' is required"), nil
		}

		query := `
			SELECT
				n.nspname AS connection_name,
				c.relname AS name,
				pd.description AS description
			FROM
				pg_catalog.pg_class c
			JOIN
				pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN
				pg_catalog.pg_description pd ON pd.objoid = c.oid AND pd.objsubid = 0
			WHERE
				c.relkind = 'f'
				AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'steampipe_internal', 'steampipe_command')
				AND (
					c.relname ILIKE $1
					OR COALESCE(pd.description, '') ILIKE $1
				)
			ORDER BY
				connection_name,
				name
		`
		var queryArgs []any
		queryArgs = append(queryArgs, "%"+args.Query+"%")

		if args.Plugin != "" {
			query = `
				SELECT
					n.nspname AS connection_name,
					c.relname AS name,
					pd.description AS description
				FROM
					pg_catalog.pg_class c
				JOIN
					pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				LEFT JOIN
					pg_catalog.pg_description pd ON pd.objoid = c.oid AND pd.objsubid = 0
				WHERE
					c.relkind = 'f'
					AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'steampipe_internal', 'steampipe_command')
					AND (
						c.relname ILIKE $1
						OR COALESCE(pd.description, '') ILIKE $1
					)
					AND (n.nspname = $2 OR c.relname LIKE $3)
				ORDER BY
					connection_name,
					name
			`
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

		// Retrieve table schema details from steampipe_plugin_column
		res, err := ExecuteReadOnlyQuery(ctx, pool,
			"SELECT name, type, description FROM steampipe_internal.steampipe_plugin_column WHERE table_name = $1 ORDER BY name",
			sr.cfg.RowLimit, args.Table)
		if err != nil {
			// Fallback if steampipe_plugin_column query fails
			res = &QueryResult{}
		}

		// Fallback to standard information_schema if no rows are returned by steampipe_plugin_column
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
			formatted := formatDBError(err)
			if rel := missingRelationName(err); rel != "" {
				// Best-effort: suggest tables that look similar (helps with common guesswork like aws_ec2_load_balancer).
				suggestions := suggestTablesForMissingRelation(ctx, pool, rel, 25)
				if suggestions != "" {
					formatted += suggestions
				} else {
					formatted += "\n\nTip: use `steampipe_table_search` with a keyword (e.g. `load_balancer`) to find the exact table name, then `steampipe_table_show` before retrying."
				}
			}
			slog.Error("query_failed", "duration_ms", duration.Milliseconds(), "sql", args.SQL, "error", err, "message", formatted)
			return errorResult(formatted), nil
		}

		// Enforce payload truncation caps before returning to host
		finalRes, err := EnforcePayloadLimit(res, sr.cfg.PayloadLimitBytes)
		if err != nil {
			slog.Error("payload_limit_failed", "error", err)
			return errorResult(fmt.Sprintf("Payload size guard failed: %v", err)), nil
		}

		sr.RecordQuery(duration, len(finalRes.Rows), finalRes.Truncated)
		slog.Info("query_executed",
			"duration_ms", duration.Milliseconds(),
			"rows", len(finalRes.Rows),
			"truncated", finalRes.Truncated,
			"sql", args.SQL,
		)

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

func missingRelationName(err error) string {
	if err == nil {
		return ""
	}
	m := missingRelationRe.FindStringSubmatch(err.Error())
	if len(m) != 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func suggestTablesForMissingRelation(ctx context.Context, pool *pgxpool.Pool, missing string, maxRows int) string {
	candidates := relationSearchCandidates(missing)
	if len(candidates) == 0 {
		return ""
	}

	const q = `
		SELECT
			n.nspname AS connection_name,
			c.relname AS name,
			pd.description AS description
		FROM
			pg_catalog.pg_class c
		JOIN
			pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN
			pg_catalog.pg_description pd ON pd.objoid = c.oid AND pd.objsubid = 0
		WHERE
			c.relkind = 'f'
			AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'steampipe_internal', 'steampipe_command')
			AND c.relname ILIKE $1
		ORDER BY
			name,
			connection_name
	`

	for _, c := range candidates {
		res, err := ExecuteReadOnlyQuery(ctx, pool, q, maxRows, "%"+c+"%")
		if err != nil || len(res.Rows) == 0 {
			continue
		}
		return formatTableSuggestions(res.Rows, missing)
	}

	return ""
}

func relationSearchCandidates(missing string) []string {
	missing = strings.TrimSpace(missing)
	if missing == "" {
		return nil
	}

	seen := map[string]bool{}
	out := make([]string, 0, 6)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	add(missing)

	parts := strings.Split(missing, "_")
	for k := 3; k >= 1; k-- {
		if len(parts) >= k {
			add(strings.Join(parts[len(parts)-k:], "_"))
		}
	}

	// Common helpful keyword for AWS ELB resources.
	if strings.Contains(missing, "load_balancer") {
		add("load_balancer")
	}

	// Put more-specific candidates first (longer strings tend to be more specific).
	// Keep missing itself first, since it's the most semantically relevant.
	if len(out) <= 1 {
		return out
	}
	rest := out[1:]
	// simple selection sort by length desc
	for i := 0; i < len(rest); i++ {
		maxIdx := i
		for j := i + 1; j < len(rest); j++ {
			if len(rest[j]) > len(rest[maxIdx]) {
				maxIdx = j
			}
		}
		rest[i], rest[maxIdx] = rest[maxIdx], rest[i]
	}

	return append([]string{out[0]}, rest...)
}

func formatTableSuggestions(rows []map[string]any, missing string) string {
	if len(rows) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\nPossible matching tables:\n")
	limit := 10
	if len(rows) < limit {
		limit = len(rows)
	}
	for i := 0; i < limit; i++ {
		name := rows[i]["name"]
		conn := rows[i]["connection_name"]
		sb.WriteString(fmt.Sprintf("- %v (connection: %v)\n", name, conn))
	}
	sb.WriteString(fmt.Sprintf("\nMissing relation was: %q\n", missing))
	sb.WriteString("Tip: run `steampipe_table_show` on one of the above to confirm columns before retrying.\n")
	return sb.String()
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

	if strings.Contains(msg, "column") && strings.Contains(msg, "does not exist") {
		return fmt.Sprintf("Missing column: %s. Use `steampipe_table_show` to verify the exact schema before retrying. Do not guess column names.", msg)
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
