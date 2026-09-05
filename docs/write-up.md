# LedgerSignal — Project Write-Up

A polyglot transaction-intelligence platform built on Plaid's Sandbox API: Go handles ingestion, Python handles risk scoring and Claude-powered enrichment, Kafka decouples the two, Postgres is the shared source of truth. This is the honest story of building it — what worked, what broke, and what's still open — not a highlight reel.

## What this is, and why

Every fintech company doing anything with bank data runs some version of the same pipeline: pull transactions from an aggregator (Plaid, in this case), normalize wildly inconsistent bank formats into one schema, score them for risk, and surface something a human or another system can act on. LedgerSignal is that pipeline, built specifically to get real, hands-on experience with the pieces that pipeline actually requires — a systems language for the ingestion path, a decoupling event backbone, and an LLM layer doing the part that's genuinely hard to hand-write: explaining *why* something looks risky in plain English.

It's also, honestly, a first production-shaped Go project — not a rewrite of prior Go experience. That's a deliberate framing choice, not a hedge: claiming otherwise wouldn't survive a real follow-up question in an interview.

## Architecture

Two independently deployable services, connected by one event backbone and one shared database:

- **Go ingestion service** — links accounts via Plaid, normalizes transaction data, handles Plaid's webhooks with a small goroutine worker pool, and publishes a normalized event per transaction to Kafka.
- **Python intelligence service** — consumes those events, runs a rule-based risk engine (large-amount, velocity, and new-category checks against an account's own history), and layers Claude-powered enrichment on top: plain-English risk summaries and income classification.
- **Kafka (Redpanda locally)** — the only channel between the two services. Go never calls Python directly.
- **Postgres** — shared, but with clear ownership: Go owns `items` and `transactions`; Python owns `risk_signals` and (as of the security-hardening pass) `audit_log`.

The one architectural decision worth explaining rather than just stating: **why Kafka, instead of Go calling Python directly.** Without it, the fast, latency-sensitive ingestion path would be waiting on the slow, LLM-dependent enrichment path on every single transaction — one Claude API hiccup and webhook handling itself starts timing out. With it, Go's job ends the moment an event is published; Python reads it whenever it's ready, and a slow or even fully offline intelligence service never blocks a real bank webhook from being acknowledged.

## Real problems, not smoothed over

Two are worth calling out specifically, because they're the kind of thing that only shows up by actually running the thing, not by reading the plan:

**A copied Redpanda config silently failed.** The first `docker-compose.yml` for Redpanda, based on a common example, included Pandaproxy/Schema Registry flags this image version didn't accept — the container just exited. The fix wasn't finding the "correct" flag syntax; it was noticing LedgerSignal never needed Pandaproxy or a Schema Registry at all (no Avro, no REST-based produce/consume) and deleting the flags outright. The lesson that stuck: when a copied config fails, check the actual logs before trying to patch the syntax — sometimes the fix is "delete what you don't need," not "fix how you're asking for it."

**A version pin broke on a newer interpreter.** `pydantic==2.10.4`'s Rust extension didn't have a prebuilt wheel for this machine's Python 3.14, and its pinned `pyo3` dependency explicitly didn't support it — pip fell back to compiling from source and failed. Unpinning and letting pip resolve the latest compatible version fixed it immediately. That's a real, still-open tradeoff (see below), not a solved problem — pinning matters for reproducibility, and this project currently trades that away for "works on this machine."

## Security, treated as a real feature

The webhook endpoint went through three real states, not one:

1. **Nothing.** `HandleWebhook` trusted every request. Fine for local Sandbox development; a real vulnerability the moment it's exposed publicly (which it was, briefly, via `ngrok`, to test real webhook delivery).
2. **Rate limiting.** A token bucket (burst of 10, refilling at 2/sec) scoped to just that route via middleware — protects the worker pool's queue from a burst before anything else runs.
3. **Actual signature verification.** Plaid's documented algorithm, implemented properly: the `Plaid-Verification` header's JWT is checked against Plaid's own published key (fetched and cached by key ID), rejected if older than five minutes, and its `request_body_sha256` claim is checked against the real request body — so a valid signature on a tampered body still fails.

The signature verification code came with a test that matters more than it might look: rather than only proving garbage gets rejected (which a broken implementation would also do), it signs a JWT with a throwaway key in the exact shape Plaid uses and proves a **correctly signed, untampered** request is actually accepted. Both directions, proven, not assumed.

On top of both: a persistent, queryable `audit_log` table, because before this the only record of an attempted forgery was whatever was still in stdout when the process happened to be running. A live burst test — 11 requests against a fresh 10-token bucket — produced exactly the expected split: 10 rejected for an invalid signature, 1 rejected by the rate limiter, both landing in the audit table with full detail.

## Measuring accuracy instead of trusting vibes

Plaid's own Effects 2026 announcements backed their income-classification feature with a measured 48% accuracy improvement, not a vibe. LedgerSignal's evaluation suite exists to make the same kind of claim honestly checkable — and building it caught a real bug in the process.

The first run of the eval suite scored **0% across every single example.** Not a labeling problem — every real Claude call was silently failing. The cause: Claude reliably wraps its JSON reply in a `` ```json ... ``` `` markdown fence, even when explicitly told to respond with only JSON. `enrichment.py`'s existing `try`/`except` caught the parse failure gracefully (nothing crashed, `income_classification` was just always `None`) — which meant the bug had been silently doing nothing since the day it shipped, until an eval suite with a real API key surfaced it. After stripping the fence: **90% overall accuracy, 100% on the real Sandbox transactions that exist in this project's own database.**

That real-data number is deliberately reported separately from the full-set number. Real Sandbox data only contains two distinct inflow patterns (an airline refund, an interest payment) — nowhere near enough to test a five-way classification, and with zero examples of "salary" or "gig income" at all. The other eight examples are hand-crafted and clearly labeled as synthetic, specifically to cover the categories real data couldn't. The one miss in the full set was a deliberately ambiguous synthetic label ("MISC CREDIT ADJUSTMENT," labeled "other," classified as "refund") — a reasonable model call on a genuinely ambiguous made-up example, not a real error.

## What's honestly still missing

In the spirit of the same honesty above:

- **No frontend exists.** The planned React dashboard (chat-style query UI, transaction/risk views) hasn't been started.
- **Still running entirely on local Docker.** The Oracle Cloud deployment described in this project's own resume framing hasn't happened yet — worth being direct about that distinction between the plan and the current state.
- **The event schema is mirrored by hand** across `internal/events/events.go` (Go) and `intelligence/schemas.py` (Python) — no schema registry, no codegen. A field change in one language won't warn you the other is now out of sync.
- **`requirements.txt` is deliberately unpinned**, a direct consequence of the `pydantic-core` build failure above — fine on one dev machine, not reproducible across environments until it's re-pinned inside a fixed container image.
- **The async Kafka consumer's synchronous DB work** runs via `asyncio.to_thread` with no pool sizing — correct at today's message volume, a real bottleneck the moment that volume grows.

## What I'd do differently

Build the evaluation suite earlier, not after the fact. It didn't just measure the income-classification feature — it caught a real bug (the markdown-fence parsing failure) that had been silently doing nothing for as long as the enrichment feature existed, with no crash and no alert to say so. A feature that fails silently and a feature that doesn't exist yet produce the exact same observable behavior from the outside; the only way to tell them apart is to actually check the output against a real, expected answer. That's true of anything built on top of an LLM call, not just this one.
