# Steampipe Model Context Protocol (MCP) Server

A high-performance, production-grade Go implementation of the [Model Context Protocol (MCP)](https://modelcontextprotocol.io) server for [Steampipe](https://steampipe.io). 

This server empowers LLMs and AI Agents (e.g., Cursor, Claude Desktop, Copilot) to interact with Steampipe's PostgreSQL Foreign Data Wrapper (FDW) natively. Agents can securely discover database schemas, inspect columns, and execute read-only queries across hundreds of cloud APIs (AWS, GCP, GitHub, Kubernetes, Slack, etc.) with strict safety controls.

---

## 🚀 Key Features

* **Progressive Schema Discovery**: Empowers the LLM to inspect schema elements sequentially (`steampipe_plugin_list` ➔ `steampipe_table_list` ➔ `steampipe_table_show` ➔ `steampipe_query`) to avoid token overflow.
* **Dual-Layer Truncation Guards**:
  * **Row Limit Safeguard**: Caps results at $1,000$ rows (configurable) during iteration to protect database and transport buffers.
  * **Payload Limit Safeguard**: Employs a serialization-time binary search trimmer to drop trailing rows if the aggregate JSON payload exceeds $1\text{ MiB}$ (configurable), reporting truncation in-band.
* **Single-Instance Safety**: Implements exclusive PID-lock safety via `unix.Flock` to prevent duplicate or conflicting processes from competing for stdio, with a fallback override.
* **LLM Self-Correction Loop**: Database exceptions (syntax typos, missing columns) are wrapped gracefully in `CallToolResult` with `IsError: true` instead of raising JSON-RPC failures, letting the LLM inspect errors and self-correct queries.
* **Thread-Safe Telemetry**: Provides resource tracking via `steampipe://status` and pre-registered prompts via the `best_practices` instruction.
* **Secure by Design**: Sanity-checks and redacts database credentials/passwords in logs and telemetry, and runs all queries in explicit `BEGIN TRANSACTION READ ONLY` boundaries.

---

## 🛠 Directory Structure

```text
steampipe-mcp-golang/
├── cmd/steampipe-mcp/
│   ├── main.go        # Logging setup, PID lock, connection pool initialization, server run loop
│   ├── config.go      # Configuration loading, validation, and connection string sanitization
│   ├── db.go          # pgxpool connection management, statement timeouts, and transaction helpers
│   ├── tools.go       # Tool schemas and callback handlers
│   ├── resource.go    # Thread-safe telemetry for 'steampipe://status'
│   └── prompt.go      # Predefined system instructions ('best_practices')
├── tools/
│   └── pydantic_ai_test_mcp.py  # End-to-end Pydantic AI MCP test harness
├── pyproject.toml     # python tool configuration (requires python >= 3.13 and uv)
├── Makefile           # build, run, test, fmt, vet, install utilities
└── README.md          # This documentation file
```

---

## ⚙️ Configuration Environment Variables

Configure the server by exporting these environment variables:

| Variable | Default Value | Description |
| :--- | :--- | :--- |
| `STEAMPIPE_MCP_WORKSPACE_DATABASE` | `postgresql://steampipe@localhost:9193/steampipe` | Main connection string to the Steampipe Postgres service. |
| `STEAMPIPE_MCP_WORKSPACE_DATABASE_PASSWORD` | `""` | Optional password for connection string (falls back to `PGPASSWORD` if unset). |
| `STEAMPIPE_MCP_LOGFILE` | `""` | Optional file path to output service logs. If empty, logs write to `Stderr`. |
| `STEAMPIPE_MCP_DEBUG` | `""` | Set to `1` or `debug` to enable verbose structured `slog` debug outputs. |
| `STEAMPIPE_MCP_LOCKFILE` | `"steampipe-mcp.lock"` | File path of the single-instance lock. Set to `off` to disable (e.g. for testing). |
| `STEAMPIPE_MCP_ROW_LIMIT` | `"1000"` | Maximum number of rows to return before truncating outputs. |
| `STEAMPIPE_MCP_STATEMENT_TIMEOUT_MS` | `"120000"` | Postgres `statement_timeout` applied to pooled connections in milliseconds ($120\text{s}$). |
| `STEAMPIPE_MCP_PAYLOAD_LIMIT_BYTES` | `"1048576"` | Hard cap on the serialized JSON payload size before truncation ($1\text{ MiB}$). |

---

## ⚡️ Quick Start

### 1. Prerequisites
* **Go**: `Go 1.21` or later.
* **Steampipe**: Must be installed and running.
  ```bash
  steampipe service start
  ```

### 2. Build the Server
Compile the server using the provided `Makefile`:
```bash
# Formats, vets, and builds the binary
make all
```

This produces a single, statically linked executable called `steampipe-mcp-golang`.

To install it locally (copied to `~/.local/bin`):
```bash
make install
```

---

## 🧪 Testing

### Go Unit Tests
Unit tests run on every commit and verify parsing safety, credential redactions, lock boundaries, and early-exit database stream cleaning:
```bash
make test
```

### Python LLM Integration Tests
We provide an automated, end-to-end integration test suite powered by [Pydantic AI](https://github.com/pydantic/pydantic-ai) to verify actual LLM interactions.

#### 1. Setup Environment
Ensure [uv](https://github.com/astral-sh/uv) is installed and Python $\ge$ 3.13 is available. Initialize the environment:
```bash
# Syncs packages and creates the .venv
uv sync
```

#### 2. Run Tests
Verify your Google Gemini credentials are set, and run the test harness (defaults to `gemini-3.5-flash`):
```bash
export GOOGLE_API_KEY="your-gemini-key"
uv run python tools/pydantic_ai_test_mcp.py
```

This will run five progressive discovery, single-table query, and row/payload truncation verification tasks directly against your running Steampipe server and output token consumption stats.

---

## 🔌 Integrating with AI Clients

### Gemini

Put in `~/.gemini/settings.json`
```
{
  "mcpServers": {
    "steampipe": {
      "command": "steampipe-mcp-golang",
      "env": {
        "STEAMPIPE_MCP_WORKSPACE_DATABASE": "postgresql://steampipe@localhost:9193/steampipe",
        "STEAMPIPE_MCP_DEBUG": "1"

      }
    }
  }
}
```

# Steampipe Configuration

## Start Service

```
% steampipe service start
Steampipe service is running:

Database:

 Host(s):            127.0.0.1, ::1, fdfe:5e6c:2e69:98de:c31d:5fee:95d6:6b63, 2607:fb91:4b0d:d213:907f:45f1:2533:78c6, 2607:fb91:4b0d:d213:cb0:2321:4fe:55af, fdfe:5e6c:2e69:98de:104f:7319:8806:538a, 192.168.12.128
 Port:               9193
 Database:           steampipe
 User:               steampipe
 Password:           ********* [use --show-password to reveal]
 Connection string:  postgres://steampipe@127.0.0.1:9193/steampipe
```


## Confirm Config
`~/.steampipe/config/aws.spc` MUST have regions set to `[*]` 

```
connection "aws" {
  plugin = "aws"
  regions = ["*"] # All regions
  max_error_retry_attempts = 9
  min_error_retry_delay = 25
}
```

# Related / Referenced Projects
- https://github.com/turbot/steampipe-mcp
- https://github.com/mdfranz/osqueryi-mcp
