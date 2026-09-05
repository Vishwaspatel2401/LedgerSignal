"""Phase 6 — the natural-language query interface (README's last Phase 6
piece, alongside enrichment and the MCP server).

This deliberately doesn't let Claude write its own SQL against Postgres —
that's a real prompt-injection/accuracy risk (a hallucinated query could read
or touch anything). Instead, Claude picks from the exact same three safe,
parameterized functions in queries.py that the MCP server exposes: it can
only ever do what those functions already allow, nothing more. Same
guardrail, two different front doors (HTTP here, MCP there).

The flow is the standard "tool use" loop: ask Claude the question with a list
of tools it may call; if it asks to call one, run the real Python function
and hand the result back; repeat until it answers in plain English instead
of asking for another tool call.
"""
import logging
from datetime import date

import anthropic
from sqlalchemy.orm import Session

from . import config, queries

logger = logging.getLogger(__name__)

MODEL = "claude-haiku-4-5-20251001"
MAX_TOOL_ROUNDS = 5  # a safety cap — never loop forever if Claude keeps asking for tools

SYSTEM_PROMPT = """You answer questions about LedgerSignal's transaction and risk data.
You can only know what the tools tell you — never guess a transaction ID, category name,
or dollar amount. If a tool returns no data, say so plainly instead of inventing an answer.
Keep answers short and in plain English, not JSON."""

TOOLS = [
    {
        "name": "get_transaction_risk_summary",
        "description": "Get the risk assessment for one transaction: level, score, "
        "rule-engine reasons, and (if available) Claude's summary and income classification.",
        "input_schema": {
            "type": "object",
            "properties": {
                "plaid_transaction_id": {"type": "string"},
            },
            "required": ["plaid_transaction_id"],
        },
    },
    {
        "name": "query_spending_by_category",
        "description": "Get total amount spent and transaction count in one spending "
        "category, optionally restricted to a date range.",
        "input_schema": {
            "type": "object",
            "properties": {
                "category": {"type": "string", "description": "e.g. FOOD_AND_DRINK, TRAVEL, LOAN_PAYMENTS"},
                "start_date": {"type": "string", "description": "YYYY-MM-DD, optional"},
                "end_date": {"type": "string", "description": "YYYY-MM-DD, optional"},
            },
            "required": ["category"],
        },
    },
    {
        "name": "get_flagged_transactions",
        "description": "List the most recent transactions at a given risk level, newest first.",
        "input_schema": {
            "type": "object",
            "properties": {
                "risk_level": {"type": "string", "enum": ["LOW", "MEDIUM", "HIGH"]},
                "limit": {"type": "integer"},
            },
            "required": [],
        },
    },
]


def _run_tool(session: Session, name: str, tool_input: dict) -> dict | list | None:
    """Dispatches one tool call to the matching queries.py function. This is
    the one place a tool name (a string Claude produced) turns into an
    actual function call — kept narrow and explicit rather than something
    generic like getattr(queries, name), so an unexpected tool name fails
    loudly instead of silently calling something unintended."""
    if name == "get_transaction_risk_summary":
        return queries.get_transaction_risk_summary(session, tool_input["plaid_transaction_id"])

    if name == "query_spending_by_category":
        start = date.fromisoformat(tool_input["start_date"]) if tool_input.get("start_date") else None
        end = date.fromisoformat(tool_input["end_date"]) if tool_input.get("end_date") else None
        return queries.query_spending_by_category(session, tool_input["category"], start, end)

    if name == "get_flagged_transactions":
        return queries.get_flagged_transactions(
            session,
            tool_input.get("risk_level", "HIGH"),
            tool_input.get("limit", 20),
        )

    raise ValueError(f"unknown tool: {name}")


def answer_question(session: Session, question: str) -> str:
    """Runs the ask-Claude / maybe-call-a-tool / ask-again loop until Claude
    replies with plain text instead of another tool request, or the round
    cap is hit. Returns a plain-English answer either way."""
    if not config.ANTHROPIC_API_KEY:
        return "The NL query interface needs ANTHROPIC_API_KEY set in intelligence/.env."

    client = anthropic.Anthropic(api_key=config.ANTHROPIC_API_KEY)
    messages = [{"role": "user", "content": question}]

    for _ in range(MAX_TOOL_ROUNDS):
        response = client.messages.create(
            model=MODEL,
            max_tokens=1024,
            system=SYSTEM_PROMPT,
            tools=TOOLS,
            messages=messages,
        )

        # Claude's reply becomes the next "assistant" turn in the conversation,
        # whether it's a final answer or a tool request — the API needs the
        # full back-and-forth history to stay coherent across rounds.
        messages.append({"role": "assistant", "content": response.content})

        if response.stop_reason != "tool_use":
            # response.content is a list of blocks; a plain-text reply is one
            # text block. Join defensively in case there's more than one.
            return "".join(block.text for block in response.content if block.type == "text")

        # One turn can request multiple tool calls at once — run all of them
        # and report every result back before asking again.
        tool_results = []
        for block in response.content:
            if block.type != "tool_use":
                continue
            try:
                result = _run_tool(session, block.name, block.input)
            except Exception:
                logger.exception("tool call failed: %s(%s)", block.name, block.input)
                result = {"error": f"{block.name} failed"}

            tool_results.append(
                {
                    "type": "tool_result",
                    "tool_use_id": block.id,
                    "content": str(result),
                }
            )

        messages.append({"role": "user", "content": tool_results})

    return "I wasn't able to settle on an answer within the allowed number of tool calls."
