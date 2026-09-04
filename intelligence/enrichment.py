"""Phase 6 — Claude-powered enrichment, layered on top of (not replacing)
risk_engine.py's rule-based score. Two things happen here, per the README's
Phase 6 / 8a plan:

1. Risk summary — turns the rule engine's plain-English `reasons` list into
   one natural sentence, the "Explainable Risk Summaries" feature.
2. Income classification — when a transaction is money coming IN (Plaid's
   convention: a negative `amount`), asks whether it looks like salary, gig
   income, a transfer, a refund, or something else. Mirrors Plaid's own
   Effects 2026 income-classification announcement (README Section 8a).

Both are best-effort. If ANTHROPIC_API_KEY isn't set, or the call fails, or
Claude's reply isn't parseable JSON, this returns an empty EnrichmentResult
rather than raising — enrichment is additive polish, not something that
should ever take down the Phase 5 scoring path underneath it.
"""
import json
import logging
from dataclasses import dataclass

import anthropic

from . import config
from .models import Transaction
from .risk_engine import RiskAssessment

logger = logging.getLogger(__name__)

MODEL = "claude-haiku-4-5-20251001"

# Lazily built so a missing key doesn't crash import-time — only fails (loudly,
# inside enrich_transaction) the moment a call is actually attempted.
_client: anthropic.Anthropic | None = None


def _get_client() -> anthropic.Anthropic | None:
    global _client
    if not config.ANTHROPIC_API_KEY:
        return None
    if _client is None:
        _client = anthropic.Anthropic(api_key=config.ANTHROPIC_API_KEY)
    return _client


@dataclass
class EnrichmentResult:
    summary: str | None = None
    income_classification: str | None = None


INCOME_CATEGORIES = ["salary", "gig_income", "transfer", "refund", "other"]


def enrich_transaction(txn: Transaction, assessment: RiskAssessment) -> EnrichmentResult:
    """Best-effort enrichment for one transaction. Never raises — a failure
    here should be logged and skipped, not take down the caller."""
    client = _get_client()
    if client is None:
        return EnrichmentResult()

    # Plaid's convention: amount is negative when money moves INTO the
    # account. Only inflows are candidates for income classification —
    # deciding that here in Python, rather than leaving it to the model,
    # keeps the domain rule explicit and testable instead of implicit in a
    # prompt.
    is_inflow = float(txn.amount) < 0

    schema_note = (
        f'"income_classification": one of {INCOME_CATEGORIES} (this is money coming IN, classify it)'
        if is_inflow
        else '"income_classification": null (this is money going OUT, not applicable)'
    )

    prompt = f"""A transaction was flagged by a rule-based risk engine. Write a one-sentence,
plain-English summary of why, using the reasons given — don't invent new reasons.

Transaction:
- merchant: {txn.merchant_name or "unknown"}
- category: {txn.category or "unknown"}
- amount: {txn.amount} ({"inflow" if is_inflow else "outflow"})
- date: {txn.transaction_date}

Risk engine output:
- level: {assessment.level}
- score: {assessment.score}
- reasons: {assessment.reasons or ["none — this transaction did not trigger any rule"]}

Reply with ONLY a JSON object, no other text:
{{"summary": "one plain-English sentence", {schema_note}}}"""

    try:
        response = client.messages.create(
            model=MODEL,
            max_tokens=300,
            messages=[{"role": "user", "content": prompt}],
        )
        raw_text = response.content[0].text.strip()
        parsed = json.loads(raw_text)
    except Exception:
        logger.exception(
            "enrichment failed for plaid_transaction_id=%s, continuing without it",
            txn.plaid_transaction_id,
        )
        return EnrichmentResult()

    income_classification = parsed.get("income_classification")
    if income_classification not in INCOME_CATEGORIES:
        income_classification = None  # guards against a malformed/hallucinated value

    return EnrichmentResult(
        summary=parsed.get("summary"),
        income_classification=income_classification,
    )
