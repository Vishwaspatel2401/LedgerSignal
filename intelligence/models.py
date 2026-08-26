"""SQLAlchemy ORM models — per the project's own tech stack decision (README
Section 4: "SQLAlchemy — ORM layer for this service's DB access"), the Python
side deliberately uses an ORM here, unlike Go's raw-SQL approach — a real,
per-service choice matching the polyglot architecture's own rationale
(matching language/tool strengths to workload, not using one style everywhere).
"""
import uuid

from sqlalchemy import Boolean, Column, Date, DateTime, Numeric, String, func
from sqlalchemy.dialects.postgresql import JSONB, UUID

from .db import Base


class Transaction(Base):
    """Read-mostly mapping onto the table Go's `storage` package owns writes
    to. The risk engine queries this for historical context (an account's
    past amounts/categories) — it never writes here; that stays Go's job."""

    __tablename__ = "transactions"

    id = Column(UUID(as_uuid=True), primary_key=True)
    item_id = Column(String, nullable=False)
    account_id = Column(String, nullable=False)
    plaid_transaction_id = Column(String, unique=True, nullable=False)
    raw_payload = Column(JSONB, nullable=False)
    amount = Column(Numeric(12, 2), nullable=False)
    iso_currency_code = Column(String)
    merchant_name = Column(String)
    category = Column(String)
    transaction_date = Column(Date, nullable=False)
    pending = Column(Boolean, nullable=False, default=False)
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now())


class RiskSignal(Base):
    """This service's own table — the actual output of the risk engine."""

    __tablename__ = "risk_signals"

    id = Column(UUID(as_uuid=True), primary_key=True, default=uuid.uuid4)
    plaid_transaction_id = Column(String, unique=True, nullable=False)
    risk_score = Column(Numeric(5, 2), nullable=False)
    risk_level = Column(String, nullable=False)
    reasons = Column(JSONB, nullable=False, default=list)
    created_at = Column(DateTime(timezone=True), server_default=func.now())
    updated_at = Column(DateTime(timezone=True), server_default=func.now())
