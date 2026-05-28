import asyncio
import logging
import os
import shutil
import sys
import time
import argparse
import warnings
from dataclasses import dataclass
from typing import Any

# Suppress Pydantic AI MCPServerStdio deprecation warnings
warnings.filterwarnings("ignore", category=DeprecationWarning)

from pydantic_ai import Agent, RunContext
from pydantic_ai.capabilities.hooks import Hooks
from pydantic_ai.mcp import MCPServerStdio
from pydantic_ai.models import ModelRequestContext, ModelResponse

# Setup logger
def setup_logging(log_file: str, debug_libs: tuple[str, ...]) -> None:
    logging.basicConfig(
        level=logging.DEBUG,
        format="%(asctime)s [%(levelname)s] %(name)s: %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
        handlers=[logging.FileHandler(log_file), logging.StreamHandler()],
    )
    # Console at INFO; file stays at DEBUG
    for h in logging.root.handlers:
        if type(h) is logging.StreamHandler:
            h.setLevel(logging.INFO)
    for name in debug_libs:
        logging.getLogger(name).setLevel(logging.DEBUG)

setup_logging("pydantic_ai_test.log", ("mcp", "httpx", "pydantic_ai"))
logger = logging.getLogger("pydantic_ai_test")

PROVIDERS = [
    (("gpt-", "o1-"), "openai:", ("OPENAI_API_KEY",)),
    (("gemini-",), "google-gla:", ("GOOGLE_API_KEY", "GEMINI_API_KEY")),
    (("claude-",), "anthropic:", ("ANTHROPIC_API_KEY",)),
]

def resolve_model(requested: str) -> str | None:
    """Add a provider prefix if missing and verify the API key. Returns None if a key is missing."""
    name = requested
    if ":" not in name:
        for prefixes, provider, _ in PROVIDERS:
            if name.startswith(prefixes):
                name = provider + name
                break

    for _, provider, env_keys in PROVIDERS:
        if name.startswith(provider):
            if not any(os.getenv(k) for k in env_keys):
                logger.error(f"Error: {' or '.join(env_keys)} not found in environment.")
                return None
            break

    return name

def _total_tokens(usage: Any) -> int:
    t = getattr(usage, "total_tokens", 0)
    return t() if callable(t) else (t or 0)

@dataclass
class TokenUsageTotals:
    requests: int = 0
    input_tokens: int = 0
    output_tokens: int = 0
    total_tokens: int = 0
    cache_read_tokens: int = 0

    def add(self, usage: Any) -> None:
        self.requests += getattr(usage, "requests", 0) or 0
        self.input_tokens += getattr(usage, "input_tokens", 0) or 0
        self.output_tokens += getattr(usage, "output_tokens", 0) or 0
        self.total_tokens += _total_tokens(usage)
        self.cache_read_tokens += getattr(usage, "cache_read_tokens", 0) or 0

    def summary(self) -> str:
        return (
            f"requests={self.requests}, input={self.input_tokens}, "
            f"output={self.output_tokens}, total={self.total_tokens}, "
            f"cache_read={self.cache_read_tokens}"
        )

# Initialize global tracking
usage_totals = TokenUsageTotals()

hooks = Hooks()

@hooks.on.after_model_request
async def track_usage(
    ctx: RunContext[None],
    *,
    request_context: ModelRequestContext,
    response: ModelResponse,
) -> ModelResponse:
    # Extract usage if present on response or response.model_response
    usage = getattr(response, "usage", None)
    if usage:
        usage_totals.add(usage)
    return response

SYSTEM_PROMPT = """You are working with Steampipe, a Postgres FDW that exposes cloud and SaaS APIs as SQL tables. Use these tools in this order:

1. `steampipe_plugin_list` — discover which plugins (aws, github, gcp, kubernetes, …) are connected. Skip this step only if the user already named a plugin.
2. `steampipe_table_list` — list tables for the relevant plugin(s). Filter by plugin prefix (e.g. tables starting with `aws_`) to keep the response small.
3. `steampipe_table_show` — inspect a specific table's columns, types, and descriptions BEFORE writing SQL. Steampipe tables often have hundreds of columns; guessing column names will fail.
4. `steampipe_query` — only now run the SQL. Always project specific columns (avoid `SELECT *`) and add a `LIMIT` for exploratory queries.

Worked example: "Find S3 buckets without versioning."
- Call `steampipe_table_show` with table=`aws_s3_bucket` to confirm `versioning_enabled` is the right column.
- Then `steampipe_query` with: `SELECT name, region, versioning_enabled FROM aws_s3_bucket WHERE versioning_enabled IS NOT TRUE LIMIT 50;`

Rules:
- If a query times out, narrow it (tighter WHERE, fewer columns, smaller LIMIT) — do not retry the same query.
- If a query returns `truncated: true`, the result was capped for transport safety. Add filters or LIMIT/OFFSET to page through.
- All queries run inside a READ ONLY transaction; INSERT/UPDATE/DELETE will fail.
- Never fabricate column names — call `steampipe_table_show` first when uncertain."""

TASKS = [
    (
        "Progressive discovery",
        "Use the Steampipe MCP tools to discover which plugins are connected, then list five tables from the most populated plugin. Explain whether `steampipe_table_list` reduces round trips compared to grepping `steampipe_table_show` outputs."
    ),
    (
        "Single-table query",
        "Inspect a representative table for the connected plugin via `steampipe_table_show`, then run `steampipe_query` to return five rows projecting only the columns you'll discuss. End with a note on why projecting specific columns matters for token usage."
    ),
    (
        "Truncation behavior",
        "Run a query you expect to return more than 1000 rows. Confirm the result envelope reports `truncated: true`, then refine the query with WHERE/LIMIT to fit within the cap. Report which trigger fired (`row_cap` vs `payload_cap`)."
    ),
    (
        "EC2 instances in us-east-2",
        "Use `steampipe_table_show` to inspect the aws_ec2_instance table schema, then write a query to list EC2 instances in us-east-2, projecting instance_id, instance_type, state, and launch_time. Add a LIMIT to ensure safe truncation."
    ),
    (
        "IAM access keys",
        "Use `steampipe_table_show` to inspect the aws_iam_access_key table schema, then write a query to list all IAM access keys, projecting access_key_id, user_name, status, and create_date. Include any notes on access_key_id truncation or redaction."
    )
]

async def run_tasks(model_name: str) -> None:
    # Look up the binary
    binary_path = shutil.which("steampipe-mcp-golang")
    if not binary_path:
        # Fallback to local build path
        local_path = os.path.abspath("./steampipe-mcp-golang")
        if os.path.exists(local_path):
            binary_path = local_path
        else:
            logger.error("Error: steampipe-mcp-golang binary not found in PATH or current directory.")
            sys.exit(1)

    logger.info(f"Using server binary: {binary_path}")

    # Set up standard environment overrides
    # Force single-instance lock file off, send logs to temp path
    env = os.environ.copy()
    env["STEAMPIPE_MCP_LOCKFILE"] = "off"
    env["STEAMPIPE_MCP_LOGFILE"] = os.path.abspath("steampipe_mcp_test_run.log")

    server = MCPServerStdio(
        command=binary_path,
        args=[],
        env=env,
    )

    agent = Agent(
        model_name,
        system_prompt=SYSTEM_PROMPT,
        mcp_servers=[server],
        capabilities=[hooks],
    )

    logger.info(f"Running LLM integration verification with model: {model_name}")

    for name, prompt in TASKS:
        logger.info(f"=== Starting Task: {name} ===")
        logger.info(f"Prompt: {prompt}")
        start_time = time.time()
        try:
            async with agent.run_mcp_servers():
                result = await agent.run(prompt)
                duration = time.time() - start_time
                logger.info(f"=== Task {name} Completed in {duration:.2f}s ===")
                print(f"\n--- TASK RESULT: {name} ---")
                print(result.output)
                print("-" * 40)
        except Exception as e:
            logger.exception(f"Task {name} failed: {e}")
            sys.exit(1)

    # Print final totals
    logger.info("=== Run Statistics ===")
    logger.info(usage_totals.summary())
    print("\n=== TOTAL TOKEN USAGE SUMMARY ===")
    print(usage_totals.summary())

def main() -> None:
    parser = argparse.ArgumentParser(description="Run Pydantic AI MCP tests for Steampipe")
    parser.add_argument(
        "model",
        nargs="?",
        default="gemini-3.5-flash",
        help="Model name to run with (e.g., gemini-3.5-flash, gpt-4o-mini, etc.)"
    )
    args = parser.parse_args()

    resolved = resolve_model(args.model)
    if not resolved:
        logger.error(f"Failed to resolve model or missing API key for {args.model}")
        sys.exit(1)

    asyncio.run(run_tasks(resolved))

if __name__ == "__main__":
    main()
