package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const BestPracticesPromptText = `You are working with Steampipe, a Postgres FDW that exposes cloud and SaaS APIs as SQL tables. Use these tools in this order:

1. ` + "`steampipe_plugin_list`" + ` — discover which plugins (aws, github, gcp, kubernetes, …) are connected. Skip this step only if the user already named a plugin.
2. ` + "`steampipe_table_list`" + ` — list tables for the relevant plugin(s). Filter by plugin prefix (e.g. tables starting with ` + "`aws_`" + `) to keep the response small.
3. ` + "`steampipe_table_show`" + ` — inspect a specific table's columns, types, and descriptions BEFORE writing SQL. Steampipe tables often have hundreds of columns; guessing column names will fail.
4. ` + "`steampipe_query`" + ` — only now run the SQL. Always project specific columns (avoid ` + "`SELECT *`" + `) and add a ` + "`LIMIT`" + ` for exploratory queries.

Worked example: "Find S3 buckets without versioning."
- Call ` + "`steampipe_table_show`" + ` with table=` + "`aws_s3_bucket`" + ` to confirm ` + "`versioning_enabled`" + ` is the right column.
- Then ` + "`steampipe_query`" + ` with: ` + "`SELECT name, region, versioning_enabled FROM aws_s3_bucket WHERE versioning_enabled IS NOT TRUE LIMIT 50;`" + `

Rules:
- If a query times out, narrow it (tighter WHERE, fewer columns, smaller LIMIT) — do not retry the same query.
- If a query returns ` + "`truncated: true`" + `, the result was capped for transport safety. Add filters or LIMIT/OFFSET to page through.
- All queries run inside a READ ONLY transaction; INSERT/UPDATE/DELETE will fail.
- Never fabricate column names — call ` + "`steampipe_table_show`" + ` first when uncertain.`

// RegisterPrompt registers the "best_practices" system instructions prompt with the MCP server.
func RegisterPrompt(server *mcp.Server) {
	p := &mcp.Prompt{
		Name:        "best_practices",
		Description: "Guidelines and recommended discovery flow for generating highly accurate Steampipe SQL queries.",
	}

	server.AddPrompt(p, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Recommended Steampipe query and discovery flow instructions",
			Messages: []*mcp.PromptMessage{
				{
					Role: "user",
					Content: &mcp.TextContent{
						Text: BestPracticesPromptText,
					},
				},
			},
		}, nil
	})
}
