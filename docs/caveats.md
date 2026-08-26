# Caveats & Known Gaps

Real tradeoffs, deferred work, and things worth being deliberate about later — surfaced while building Phases 3–5, collected here so they don't just live in scrollback. Not bugs (see `bugs.md`); these are things that work correctly today but have a known limit or an open decision attached.

---

## Plaid webhook signature is not verified

**Where:** `internal/api/handlers.go`, `HandleWebhook` (Phase 3)

Plaid signs every real webhook with a `Plaid-Verification` JWT header — confirmed present on a real webhook delivery during Phase 3 testing (via `ngrok`'s request log). `HandleWebhook` currently ignores it completely. That means anyone who discovers the webhook URL could `POST` a fake `SYNC_UPDATES_AVAILABLE` payload with an arbitrary `item_id` and trigger a real sync — fine for local Sandbox development, not fine the moment this is ever exposed publicly.

**Deferred to:** Phase 8 (security hardening), by original plan — not forgotten, just not yet done.

---

## The event schema is mirrored by hand across two languages

**Where:** `internal/events/events.go` (Go) ↔ `intelligence/schemas.py` (Python)

There's no schema registry, no protobuf/Avro, no codegen step keeping these in sync — `NormalizedTransactionEvent`'s fields are typed out separately in both languages, by hand. If the Go struct ever gains, renames, or changes the type of a field, nothing will warn you that `schemas.py` is now out of sync — it'll either fail at parse time or, worse, silently drop/misread a field.

**Worth doing eventually:** if this event contract grows more fields or more event types, a shared schema definition (even just a hand-maintained JSON Schema both sides validate against in tests) would catch drift before it ships.

---

## Async Kafka consumer, synchronous DB driver

**Where:** `intelligence/consumer.py`

`aiokafka` is asyncio-native; `psycopg2`/SQLAlchemy here are synchronous by choice, for simplicity. Mixing them directly would block the consumer's entire event loop on every DB query, so `_process_event` runs via `asyncio.to_thread` — one new thread per message, by default, with no pool sizing or throughput tuning.

**Fine at:** the current low/dev-scale message volume. **Would need revisiting at:** meaningfully higher throughput — either an async driver (`asyncpg`) or a deliberately-sized worker pool, not the default one-thread-per-call behavior.

---

## `requirements.txt` is deliberately unpinned

**Where:** `requirements.txt`

Started pinned; unpinned after hitting a real build failure (pinned `pydantic-core` predated this machine's Python 3.14 — see `bugs.md`). Currently resolves to "whatever's latest and compatible," which is fine for a single dev machine but not reproducible across environments.

**Worth doing before deployment:** re-pin deliberately once this is containerized for Phase 9, against whatever fixed Python version the container image uses — a controlled environment removes the "pinned version predates this machine's interpreter" problem that caused the original unpinning.

---

## Kafka replay is real but not yet deliberately exercised

**Where:** `intelligence/consumer.py` (`KAFKA_GROUP_ID`, `auto_offset_reset="earliest"`)

The mechanism behind the README's "trigger full historical re-enrichment by resetting the consumer group offset" framing genuinely works — it's what happened, unprompted, the first time this consumer ever started against Go's existing backlog of 49 events. But that was incidental (a new consumer group's default behavior), not a deliberate "reset offsets and replay through an updated risk model" operation.

**Worth doing eventually:** actually exercise this on purpose once the risk engine's rules change — reset `intelligence-service`'s committed offset and confirm re-scoring updates `risk_signals` cleanly via the existing idempotent upsert, as a real demonstration of the architecture's own stated design goal.
