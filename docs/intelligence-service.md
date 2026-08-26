# Intelligence Service (Python) — Risk Signal Engine

Phase 5: the Python service that consumes the events Go has been publishing since Phase 4, scores each transaction for risk, and writes the result back to Postgres.

## Where it lives

`intelligence/` at the repo root — sibling to `ingestion/`, its own package, its own `.env` (`intelligence/.env`, gitignored). Shares the root `venv/` and `requirements.txt` rather than a second virtualenv, since this repo only has one Python service so far.

**Deliberately its own `.env`, not shared with Go's**: Python only gets `DATABASE_URL`, `KAFKA_BROKERS`, `KAFKA_TOPIC`, `KAFKA_GROUP_ID` — never the Plaid credentials or `ENCRYPTION_KEY` Go uses. Same least-privilege reasoning behind splitting the two services into independently deployable units in the first place.

## Structure

```
intelligence/
  config.py      — env var loading
  schemas.py     — Pydantic mirror of Go's NormalizedTransactionEvent
  db.py          — SQLAlchemy engine/session
  models.py      — Transaction (read-only) + RiskSignal (owned) ORM models
  risk_engine.py — the actual scoring rules
  consumer.py    — the aiokafka consumer loop
  main.py        — FastAPI app; starts the consumer as a background task
```

## Why SQLAlchemy here, raw SQL in Go

Not an inconsistency — a deliberate per-service choice, matching the README's own tech stack decision ("SQLAlchemy — ORM layer for this service's DB access") and the project's broader "match tool to workload" philosophy. Go's `pgx` raw-SQL approach suits a service with a handful of well-known queries; an ORM suits a service that's about to do more varied, evolving querying (the risk engine's historical-context lookups, and later the NL query interface in Phase 6).

## The event contract, mirrored by hand

`intelligence/schemas.py`'s `NormalizedTransactionEvent` must match `internal/events/events.go`'s struct field-for-field. There's no shared schema-generation step between the two languages — no schema registry, no protobuf/Avro. Worth knowing as a real maintenance point: if the Go struct changes, this file has to change too, and nothing will warn you if it doesn't.

## Why the risk engine looks transactions back up in Postgres

`NormalizedTransactionEvent` only carries `account_id`, `plaid_transaction_id`, the raw payload, `normalized_amount`, and a timestamp — no `merchant_name` or `category`. Rather than re-deriving those from `raw_payload` (duplicating Go's normalization logic in a second language), `risk_engine.score_transaction` looks the transaction back up in the `transactions` table by `plaid_transaction_id`. This is safe because Go's `SaveTransaction` always commits before the event is published (see `SyncItemTransactions`) — Postgres, not the event, is the source of truth for normalized fields; the event is just the trigger telling Python "something's ready to look at."

## The rules (v1 — deliberately simple)

| Rule | Trigger | Score |
|---|---|---|
| Large amount | Transaction amount is ≥3x / ≥5x **this account's own** historical average (not a global threshold) | +20 / +40 |
| Velocity | ≥5 transactions for this account on the same calendar day | +25 |
| New category | First transaction ever in this category, for this account | +15 |

Risk level: `LOW` (<20), `MEDIUM` (20–49), `HIGH` (≥50). Every triggered rule's plain-English reason is stored in `risk_signals.reasons` (JSONB array) — this is the "explainable" half of the README's "Explainable Risk Summaries" feature; Claude turning these into a nicer sentence is Phase 6, not this phase.

## Async consumer, sync DB driver

`aiokafka` is asyncio-native; `psycopg2`/SQLAlchemy here are synchronous (blocking) by choice, for simplicity. Mixing them directly would block the whole consumer's event loop on every DB query. `consumer.py` runs the actual scoring + write (`_process_event`) via `asyncio.to_thread`, so blocking DB work happens on a separate thread while the async loop keeps pulling messages. Worth knowing as a real tradeoff: this works fine at low-to-moderate throughput; a high-throughput deployment would likely want either an async DB driver (`asyncpg`) or a proper thread/process pool sized deliberately, not the default thread-per-call behavior of `asyncio.to_thread`.

## Idempotency

Same discipline as the Go side: `risk_signals` upserts on `plaid_transaction_id` (`INSERT ... ON CONFLICT DO UPDATE`), so re-processing an event (consumer restart before an offset commit, or a deliberate replay) updates the row rather than erroring or duplicating.

## Consumer group + replay

`KAFKA_GROUP_ID=intelligence-service`, `auto_offset_reset="earliest"` — a fresh or reset consumer group replays the entire topic from the start rather than silently skipping everything published before it first connected. This is the concrete mechanism behind the README's "trigger full historical re-enrichments by resetting Kafka consumer group offsets" framing (Section 9a) — not yet exercised deliberately (that's a Phase 6+ operational story), but the mechanism is real and was exactly what happened the first time this consumer started, against the real backlog Go had already published.

## Verified end-to-end

Started the service fresh; it replayed the entire existing backlog (49 events), correctly flagging real outliers — e.g. a `$2078.50` "AUTOMATIC PAYMENT" (3.4x the account's average) and a `-$500.00` United Airlines refund (6.6x average) both landed `MEDIUM`, with human-readable reasons stored. Then triggered a **new** Go sync while the consumer was already running, and confirmed the new transactions' events were picked up and scored live — not just on startup replay.

```
risk_level | count
-----------+------
LOW        | 43
MEDIUM     | 6
```

## Current status

Kafka → risk scoring → Postgres is done and proven, both for backlog replay and live consumption. **Not yet built**: Claude-powered enrichment (categorization, human-written risk summaries) and the NL query interface — both Phase 6. The `/health` endpoint is the only HTTP surface so far; there's no API yet for querying risk signals directly (that's part of the eventual REST API layer / dashboard backend).
