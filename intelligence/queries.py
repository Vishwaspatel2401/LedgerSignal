"""Phase 6 — the read-only query functions behind both the MCP server
(mcp_server.py) and the NL query interface (coming next). Written once here
so neither has to duplicate the actual SQLAlchemy queries — they just parse
their own input format (MCP tool args, or a natural-language question) and
call these.

Every function takes an already-open `session` rather than opening its own —
same reasoning as risk_engine.score_transaction: keeps these testable without
a real database, and lets callers control the session's lifetime.
"""
from datetime import date

from sqlalchemy import func
from sqlalchemy.orm import Session

from .models import RiskSignal, Transaction


def get_transaction_risk_summary(session: Session, plaid_transaction_id: str) -> dict | None:
    """The full risk assessment for one transaction, or None if it hasn't
    been scored yet (or the ID doesn't exist)."""
    signal = (
        session.query(RiskSignal)
        .filter(RiskSignal.plaid_transaction_id == plaid_transaction_id)
        .one_or_none()
    )
    if signal is None:
        return None

    return {
        "plaid_transaction_id": signal.plaid_transaction_id,
        "risk_level": signal.risk_level,
        "risk_score": float(signal.risk_score),
        "reasons": signal.reasons,
        "risk_summary": signal.risk_summary,
        "income_classification": signal.income_classification,
    }


def query_spending_by_category(
    session: Session,
    category: str,
    start_date: date | None = None,
    end_date: date | None = None,
) -> dict:
    """Total spend and transaction count in one category, optionally
    restricted to a date range. Matches on Transaction.category exactly —
    the same normalized category storage.go writes on the Go side."""
    q = session.query(
        func.sum(Transaction.amount), func.count(Transaction.id)
    ).filter(Transaction.category == category)

    if start_date is not None:
        q = q.filter(Transaction.transaction_date >= start_date)
    if end_date is not None:
        q = q.filter(Transaction.transaction_date <= end_date)

    total, count = q.one()
    return {
        "category": category,
        "total_amount": float(total or 0),
        "transaction_count": count or 0,
    }


def get_flagged_transactions(
    session: Session, risk_level: str = "HIGH", limit: int = 20
) -> list[dict]:
    """The most recent transactions at exactly the given risk level (LOW,
    MEDIUM, or HIGH), newest first, joined with their transaction details."""
    rows = (
        session.query(RiskSignal, Transaction)
        .join(Transaction, RiskSignal.plaid_transaction_id == Transaction.plaid_transaction_id)
        .filter(RiskSignal.risk_level == risk_level)
        .order_by(RiskSignal.created_at.desc())
        .limit(limit)
        .all()
    )

    return [
        {
            "plaid_transaction_id": rs.plaid_transaction_id,
            "merchant_name": txn.merchant_name,
            "amount": float(txn.amount),
            "category": txn.category,
            "transaction_date": str(txn.transaction_date),
            "risk_level": rs.risk_level,
            "risk_score": float(rs.risk_score),
            "risk_summary": rs.risk_summary,
        }
        for rs, txn in rows
    ]
