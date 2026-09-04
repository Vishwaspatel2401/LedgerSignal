# MCP Server — Phase 6 / 8a add-on

`intelligence/mcp_server.py` exposes LedgerSignal's risk data directly to any MCP client (Claude Code, Claude Desktop, Cursor) as callable tools — the README's "MCP Server over LedgerSignal" add-on (Section 8a), mirroring Plaid's own MCP support announcement.

## What this is not

This is a separate process from `main.py`'s FastAPI app. An MCP server speaks its own protocol over stdio, not HTTP — a client starts it itself as a subprocess (per its own config), rather than connecting to something already running in the background. It also doesn't call Claude itself — it's the opposite direction: it lets an MCP client (which may itself be Claude) query LedgerSignal's Postgres data through a small, fixed set of safe, read-only tools instead of raw SQL.

## The three tools

| Tool | What it does |
|---|---|
| `get_transaction_risk_summary(plaid_transaction_id)` | Full risk assessment for one transaction — level, score, rule-engine reasons, and (if Phase 6 enrichment is configured) Claude's summary and income classification. |
| `query_spending_by_category(category, start_date?, end_date?)` | Total spend and transaction count in one category, optionally within a date range. |
| `get_flagged_transactions(risk_level?, limit?)` | Most recent transactions at a given risk level (`LOW`/`MEDIUM`/`HIGH`), newest first. |

All three are thin wrappers around `intelligence/queries.py` — the same functions the NL query interface uses, so both surfaces stay consistent by construction rather than by convention.

## Manual test (no client needed)

```bash
intelligence/venv/bin/python -c "
from intelligence.db import SessionLocal
from intelligence import queries
with SessionLocal() as s:
    print(queries.get_flagged_transactions(s, 'HIGH', 5))
"
```

## Registering it with Claude Code

Add to a `.mcp.json` at the repo root (create it if it doesn't exist):

```json
{
  "mcpServers": {
    "ledgersignal": {
      "command": "/Users/vishwaspatel/LinkVault/intelligence/venv/bin/python",
      "args": ["-m", "intelligence.mcp_server"],
      "cwd": "/Users/vishwaspatel/LinkVault"
    }
  }
}
```

`cwd` matters — the module is `intelligence.mcp_server`, and Python only resolves that if it's run from the repo root (same reason `main.py`'s own docstring says to run `uvicorn` from the root, not from inside `intelligence/`). Restart Claude Code after adding this for it to pick up the new server.

## Registering it with Claude Desktop

Same shape, in Claude Desktop's `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "ledgersignal": {
      "command": "/Users/vishwaspatel/LinkVault/intelligence/venv/bin/python",
      "args": ["-m", "intelligence.mcp_server"],
      "cwd": "/Users/vishwaspatel/LinkVault"
    }
  }
}
```

## Why the absolute paths

MCP clients launch this as a subprocess without inheriting your shell's environment (no active venv, no `cd` history) — so both the interpreter path and `cwd` have to be spelled out explicitly, unlike running it yourself from a terminal where you're already in the right place with the venv active.
