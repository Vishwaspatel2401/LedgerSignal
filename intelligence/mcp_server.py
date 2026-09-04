"""Phase 6 / 8a — "MCP Server over LedgerSignal" (README Section 8a).

Exposes the read-only functions in queries.py as MCP tools, so any MCP
client (Claude Desktop, Claude Code, Cursor) can ask about risk signals and
spending directly — the same idea behind Plaid's own MCP support
announcement, applied to this project.

This is a separate, standalone process from main.py's FastAPI app — an MCP
server speaks its own protocol over stdio, not HTTP, and a client starts it
itself as a subprocess rather than connecting to something already running.

Run directly for a quick manual check:
    intelligence/venv/bin/python -m intelligence.mcp_server

To register it with an MCP client, point the client's config at this exact
command (see docs/mcp.md for the ready-to-paste config block).
"""
from datetime import date

from mcp.server.mcpserver import MCPServer

from . import queries
from .db import SessionLocal

mcp = MCPServer("ledgersignal")


@mcp.tool()
def get_transaction_risk_summary(plaid_transaction_id: str) -> dict:
    """Get the risk assessment for one transaction: its risk level, score,
    the rule engine's reasons, and (if enrichment is configured) Claude's
    plain-English summary and income classification.

    Args:
        plaid_transaction_id: the transaction's Plaid transaction ID.
    """
    with SessionLocal() as session:
        result = queries.get_transaction_risk_summary(session, plaid_transaction_id)
        return result or {"error": f"no risk signal found for {plaid_transaction_id}"}


@mcp.tool()
def query_spending_by_category(
    category: str, start_date: str | None = None, end_date: str | None = None
) -> dict:
    """Get total amount spent and transaction count in one spending
    category, optionally restricted to a date range.

    Args:
        category: the normalized category name (e.g. "FOOD_AND_DRINK").
        start_date: optional start date, as YYYY-MM-DD.
        end_date: optional end date, as YYYY-MM-DD.
    """
    with SessionLocal() as session:
        parsed_start = date.fromisoformat(start_date) if start_date else None
        parsed_end = date.fromisoformat(end_date) if end_date else None
        return queries.query_spending_by_category(session, category, parsed_start, parsed_end)


@mcp.tool()
def get_flagged_transactions(risk_level: str = "HIGH", limit: int = 20) -> list[dict]:
    """List the most recent transactions at a given risk level, newest first.

    Args:
        risk_level: exactly "LOW", "MEDIUM", or "HIGH".
        limit: maximum number of transactions to return.
    """
    with SessionLocal() as session:
        return queries.get_flagged_transactions(session, risk_level, limit)


if __name__ == "__main__":
    mcp.run()
