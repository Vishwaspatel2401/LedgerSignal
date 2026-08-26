"""Pydantic models — the Python side of the Go <-> Python event contract.

This must stay in sync with internal/events/events.go's NormalizedTransactionEvent
by hand, field for field. There's no shared schema-generation step between the
two languages (that's what a schema registry would solve, and this project
deliberately isn't running one — see docs/kafka.md).
"""
from datetime import datetime
from typing import Any

from pydantic import BaseModel


class NormalizedTransactionEvent(BaseModel):
    account_id: str
    plaid_transaction_id: str
    raw_payload: dict[str, Any]
    normalized_amount: float
    timestamp: datetime
