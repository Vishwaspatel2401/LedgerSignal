"""The Kafka consumer loop — reads NormalizedTransactionEvent messages off the
topic Go publishes to, scores each one, and writes the result to risk_signals.
"""
import asyncio
import logging

from aiokafka import AIOKafkaConsumer
from sqlalchemy import func
from sqlalchemy.dialects.postgresql import insert

from . import config
from .db import SessionLocal
from .models import RiskSignal
from .risk_engine import score_transaction
from .schemas import NormalizedTransactionEvent

logger = logging.getLogger(__name__)


async def consume_forever() -> None:
    """Runs until cancelled — meant to be launched as a background asyncio
    task from the FastAPI app's lifespan (see main.py)."""
    consumer = AIOKafkaConsumer(
        config.KAFKA_TOPIC,
        bootstrap_servers=config.KAFKA_BROKERS,
        group_id=config.KAFKA_GROUP_ID,
        # "earliest" so a brand-new consumer group (or one whose offsets were
        # deliberately reset) replays the topic from the start, rather than
        # silently skipping everything published before it first connected —
        # this is exactly the "replay by resetting the consumer group" story
        # from the README's Section 9a.
        auto_offset_reset="earliest",
        enable_auto_commit=True,
    )
    await consumer.start()
    logger.info(
        "Kafka consumer started (topic=%s, group=%s)",
        config.KAFKA_TOPIC,
        config.KAFKA_GROUP_ID,
    )

    try:
        async for message in consumer:
            await _handle_message(message.value)
    finally:
        await consumer.stop()


async def _handle_message(raw_value: bytes) -> None:
    try:
        event = NormalizedTransactionEvent.model_validate_json(raw_value)
    except Exception:
        logger.exception("failed to parse Kafka message as NormalizedTransactionEvent, skipping")
        return

    try:
        # SQLAlchemy + psycopg2 here are synchronous (blocking) calls, but
        # this whole function runs inside aiokafka's asyncio event loop.
        # asyncio.to_thread runs the blocking DB work on a separate thread,
        # so one slow query doesn't stall every other message being consumed.
        await asyncio.to_thread(_process_event, event)
    except Exception:
        logger.exception(
            "failed to process transaction plaid_transaction_id=%s", event.plaid_transaction_id
        )


def _process_event(event: NormalizedTransactionEvent) -> None:
    with SessionLocal() as session:
        assessment = score_transaction(session, event)

        # Idempotent upsert — same discipline as Go's ON CONFLICT pattern.
        # Re-processing the same event (e.g. after a consumer restart before
        # the last offset commit) updates the row instead of erroring or
        # duplicating it.
        stmt = insert(RiskSignal).values(
            plaid_transaction_id=event.plaid_transaction_id,
            risk_score=assessment.score,
            risk_level=assessment.level,
            reasons=assessment.reasons,
        )
        stmt = stmt.on_conflict_do_update(
            index_elements=[RiskSignal.plaid_transaction_id],
            set_={
                "risk_score": assessment.score,
                "risk_level": assessment.level,
                "reasons": assessment.reasons,
                "updated_at": func.now(),
            },
        )
        session.execute(stmt)
        session.commit()

        logger.info(
            "scored plaid_transaction_id=%s risk_level=%s risk_score=%s",
            event.plaid_transaction_id,
            assessment.level,
            assessment.score,
        )
