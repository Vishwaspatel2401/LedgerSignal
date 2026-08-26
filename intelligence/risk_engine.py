"""The Risk Signal Engine — rule-based transaction risk scoring (README's
"smaller analog to Plaid's Signal product").

Design note: NormalizedTransactionEvent only carries account_id,
plaid_transaction_id, the raw payload, amount, and a timestamp — it doesn't
carry merchant_name/category directly. Rather than re-deriving those from
raw_payload (duplicating Go's normalization logic in a second language), this
engine looks the transaction back up in the `transactions` table by
plaid_transaction_id, which is guaranteed to already exist there — Go's
SaveTransaction runs and commits before the event is ever published (see
SyncItemTransactions in internal/api/handlers.go). Postgres, not the event, is
the source of truth for normalized fields; the event is just the trigger.
"""
from dataclasses import dataclass, field
from decimal import Decimal

from sqlalchemy import func
from sqlalchemy.orm import Session

from .models import Transaction
from .schemas import NormalizedTransactionEvent

# Thresholds — deliberately simple, named constants rather than magic numbers
# scattered through the rules below, so tuning them later is a one-line change.
LARGE_AMOUNT_HIGH_RATIO = 5.0   # amount is >= 5x the account's average -> high-weight flag
LARGE_AMOUNT_MEDIUM_RATIO = 3.0  # amount is >= 3x the account's average -> medium-weight flag
VELOCITY_THRESHOLD = 5           # this many same-day transactions -> flag

SCORE_LARGE_AMOUNT_HIGH = 40
SCORE_LARGE_AMOUNT_MEDIUM = 20
SCORE_VELOCITY = 25
SCORE_NEW_CATEGORY = 15

RISK_LEVEL_HIGH_THRESHOLD = 50
RISK_LEVEL_MEDIUM_THRESHOLD = 20


@dataclass
class RiskAssessment:
    score: float
    level: str
    reasons: list[str] = field(default_factory=list)


def score_transaction(session: Session, event: NormalizedTransactionEvent) -> RiskAssessment:
    """Runs every rule against one transaction and returns the combined result."""
    score = 0.0
    reasons: list[str] = []

    txn = (
        session.query(Transaction)
        .filter(Transaction.plaid_transaction_id == event.plaid_transaction_id)
        .one_or_none()
    )
    if txn is None:
        # Shouldn't happen in normal operation (Go writes the row before
        # publishing), but fail loudly rather than scoring against nothing —
        # a silent zero-score here would be worse than an obvious error.
        raise LookupError(
            f"no transactions row found for plaid_transaction_id={event.plaid_transaction_id}"
        )

    amount = abs(float(event.normalized_amount))

    # Rule 1: is this amount unusually large for THIS account specifically,
    # not against some global threshold? A $2,000 transaction is normal for
    # one account and wildly out of pattern for another.
    avg_amount = (
        session.query(func.avg(func.abs(Transaction.amount)))
        .filter(
            Transaction.account_id == txn.account_id,
            Transaction.plaid_transaction_id != event.plaid_transaction_id,
        )
        .scalar()
    )
    if avg_amount and float(avg_amount) > 0:
        ratio = amount / float(avg_amount)
        if ratio >= LARGE_AMOUNT_HIGH_RATIO:
            score += SCORE_LARGE_AMOUNT_HIGH
            reasons.append(f"Amount is {ratio:.1f}x this account's average transaction size")
        elif ratio >= LARGE_AMOUNT_MEDIUM_RATIO:
            score += SCORE_LARGE_AMOUNT_MEDIUM
            reasons.append(f"Amount is {ratio:.1f}x this account's average transaction size")

    # Rule 2: transaction velocity — a burst of activity on the same day is a
    # common early signal for both fraud and account takeover.
    same_day_count = (
        session.query(func.count(Transaction.id))
        .filter(
            Transaction.account_id == txn.account_id,
            Transaction.transaction_date == txn.transaction_date,
        )
        .scalar()
        or 0
    )
    if same_day_count >= VELOCITY_THRESHOLD:
        score += SCORE_VELOCITY
        reasons.append(f"{same_day_count} transactions for this account on {txn.transaction_date}")

    # Rule 3: a category this account has genuinely never used before is
    # worth a small flag — not damning on its own, but meaningful combined
    # with the other two.
    if txn.category:
        prior_category_count = (
            session.query(func.count(Transaction.id))
            .filter(
                Transaction.account_id == txn.account_id,
                Transaction.category == txn.category,
                Transaction.plaid_transaction_id != event.plaid_transaction_id,
            )
            .scalar()
            or 0
        )
        if prior_category_count == 0:
            score += SCORE_NEW_CATEGORY
            reasons.append(f"First transaction in category '{txn.category}' for this account")

    if score >= RISK_LEVEL_HIGH_THRESHOLD:
        level = "HIGH"
    elif score >= RISK_LEVEL_MEDIUM_THRESHOLD:
        level = "MEDIUM"
    else:
        level = "LOW"

    return RiskAssessment(score=score, level=level, reasons=reasons)
