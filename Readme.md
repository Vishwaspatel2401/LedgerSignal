# LinkVault (LedgerSignal)

### A Polyglot Transaction Intelligence Platform for the Open-Banking Era

---

## 1. Overview

**LedgerSignal** is a full-stack financial intelligence platform built on top of Plaid's Sandbox API, using a polyglot architecture that mirrors how real fintech infra teams split their stacks: **Go** handles high-throughput ingestion and normalization, **Python** handles ML/LLM-driven risk scoring and enrichment, and **Kafka** decouples the two as an event backbone.

Rather than re-implementing account connectivity — which is becoming commoditized under open banking regulation (CFPB Section 1033) — LedgerSignal focuses on the layer _above_ the connection: normalizing messy transaction data, generating risk signals, and surfacing actionable insight through an LLM-powered query interface.

**Goal:** Demonstrate the ability to build both (a) the higher-value intelligence layer that data aggregators are increasingly competing on, and (b) a production-realistic polyglot architecture — matching language choice to workload characteristics rather than using one language for everything.

---

## 2. Problem Statement

Open banking regulation (like the CFPB's Section 1033 rule) is turning account-to-app connectivity into a regulated utility — any fintech will soon be able to tap bank APIs directly, without paying an aggregator like Plaid a per-connection fee. This means the aggregation layer itself is losing its moat, and the real competitive value is shifting to what happens _after_ the connection: identity verification, risk scoring, and turning raw transaction data into decisions. This mirrors Plaid's own strategic shift toward products like Signal (fraud/risk ML) and Identity, rather than pure connectivity.

LedgerSignal is scoped around that shift, and around a second, complementary problem: **how to architect a system where a high-throughput, latency-sensitive ingestion path (Go) and an ML/LLM-heavy intelligence path (Python) can scale, fail, and deploy independently of each other.** It assumes account connectivity as a solved/commoditized problem (via Plaid Sandbox) and builds the intelligence layer on top: reconciling inconsistent data, scoring transactions for risk, and making that data queryable and explainable.

---

## 3. Core Features

|Feature|Description|Owning Service|
|---|---|---|
|**Data Normalization Pipeline**|Ingests inconsistent transaction formats from multiple mock institutions (via Plaid Sandbox) and reconciles them into one clean schema|Go|
|**Real-Time Webhook Ingestion**|High-concurrency receiver for Plaid webhooks (new transaction, balance update, item error)|Go|
|**Event Publishing**|Normalized transactions published to Kafka topics for downstream consumption|Go|
|**Risk Signal Engine**|Scores transactions for risk using rule-based checks + lightweight ML — a smaller analog to Plaid's Signal product|Python|
|**LLM-Powered Transaction Enrichment**|Classifies messy raw transaction descriptions into clean, human-readable categories|Python|
|**Explainable Risk Summaries**|LLM generates short, human-readable explanations for flagged/risky transactions instead of an opaque score|Python|
|**Natural-Language Query Interface**|Chat-style interface that translates plain-English questions ("How much did I spend on subscriptions last month?") into structured queries over transaction data|Python|
|**Secure Account Linking**|Connects to Plaid Sandbox to simulate real bank linking; only encrypted access tokens are stored, never raw credentials|Go|
|**Transaction Dashboard**|React dashboard visualizing account balances, categorized spending, and flagged risk signals|Frontend|
|**Audit Logging**|Every token access, data pull, and risk decision is logged for compliance traceability|Go + Python|

---

## 4. Tech Stack

### Ingestion Service (Go)

- **Chi** or **Gin** (or native `net/http`) — routing for the webhook receiver and internal API
- **`plaid-go`** — official Plaid SDK for account linking and sandbox event handling
- **Goroutines + Channels** — concurrent processing of incoming webhook events
- **`pgx`** — high-performance PostgreSQL driver (raw SQL, no heavy ORM overhead) — `ent` or `GORM` as an alternative if schema migrations need more structure
- **`segmentio/kafka-go`** — Kafka producer, publishing normalized transaction events

### Intelligence Service (Python)

- **FastAPI** — lightweight API layer for the risk-scoring/enrichment service and NL query endpoint
- **`confluent-kafka-python`** or **`aiokafka`** — Kafka consumer, reading normalized events off the topic
- **scikit-learn** _(stretch goal)_ — lightweight classification model for risk scoring
- **Claude Haiku (via `anthropic` Python SDK)** — transaction categorization, risk-summary generation, and NL-to-query translation via tool use
- **SQLAlchemy** — ORM layer for this service's DB access

### Event Backbone & Caching

- **Apache Kafka** (via **Redpanda** locally) — durable, ordered event streaming between the Go ingestion service and the Python intelligence service; partitioned by `account_id` for per-account ordering guarantees
    - Local dev: **Redpanda** via Docker Compose — Kafka-API-compatible, boots in milliseconds, minimal RAM, zero code changes needed when moving to production
    - Deployed: self-hosted on an **Oracle Cloud "Always Free" Ampere (ARM) VM**, as a final deployment step after the pipeline is validated locally
- **Redis** — pure caching layer (hot reads: account balances, recent transactions for the dashboard); cache-aside pattern with a 300s TTL, updated/invalidated by the Python service only after risk scoring + enrichment complete — not used as the event backbone

### Data & Storage

- **PostgreSQL** — primary transactional database (accounts, users, normalized transactions), shared by both services (or split per-service if data ownership boundaries are pushed further)
- **Snowflake** _(optional/stretch)_ — analytical warehouse for historical transaction analysis
- **Apache Airflow** _(optional/stretch)_ — scheduled ETL jobs for batch reconciliation

### Third-Party Integration

- **Plaid Sandbox API** — simulated bank linking, mock transactions, and webhook events

### Frontend

- **React** — dashboard UI
- **REST API integration** — connecting frontend to both backend services
- **Chart/visualization library** (e.g., Recharts) — spending trends, flagged transaction views

### DevOps / Infra

- **Docker** — containerized services (Go service, Python service, Kafka, Postgres, Redis) for local dev
- **GitHub Actions (CI/CD)** — automated testing and deployment pipeline for both services
- **Oracle Cloud Free Tier** — deployment target for Kafka (and optionally the full stack)

---

## 5. High-Level Architecture

```
                              Plaid Sandbox API
                                    │
                                    ▼
                    ┌───────────────────────────────┐
                    │      GO: Ingestion Service      │
                    │  ─────────────────────────────  │
                    │  Webhook Receiver (Chi/Gin)      │
                    │        │                         │
                    │        ▼                         │
                    │  Goroutine Worker Pool            │
                    │        │                         │
                    │        ▼                         │
                    │  Normalization Layer              │
                    │        │                         │
                    │        ▼                         │
                    │  PostgreSQL (raw + normalized)    │
                    └────────┬──────────────────────────┘
                              │  publish normalized txn event
                              ▼
                    ┌───────────────────┐
                    │   Apache Kafka     │   (partitioned by account_id)
                    │  (local Docker /   │
                    │   Oracle Cloud VM) │
                    └────────┬───────────┘
                              │  consume
                              ▼
                    ┌───────────────────────────────┐
                    │     PYTHON: Intelligence        │
                    │           Service                │
                    │  ─────────────────────────────  │
                    │  Kafka Consumer                   │
                    │        │                         │
                    │        ▼                         │
                    │  Risk Signal Engine (rules + ML)  │
                    │        │                         │
                    │        ▼                         │
                    │  LLM Enrichment (Claude Haiku)    │
                    │  - categorization                 │
                    │  - explainable risk summaries     │
                    │        │                         │
                    │        ▼                         │
                    │  PostgreSQL (enriched data)       │
                    │        │                         │
                    │  FastAPI: NL Query Interface      │
                    │  (LLM tool-use → SQL filter)      │
                    └────────┬──────────────────────────┘
                              │
                              ▼
                    ┌───────────────────┐      ┌─────────────┐
                    │   REST API Layer   │◄────►│  Redis Cache │
                    └────────┬───────────┘      └─────────────┘
                              ▼
                      React Dashboard
```

**Why this shape:** the Go service and Python service never call each other directly — they're fully decoupled via Kafka. If the Python intelligence service is slow, down, or being redeployed, the Go ingestion path keeps accepting and normalizing webhooks without interruption; events simply queue up in Kafka until the consumer catches up. This is the "failure isolation" pattern applied structurally, not just conceptually.

---

## 6. Security Model

- **No raw credentials stored** — Plaid Link handles bank auth; LedgerSignal only ever stores encrypted `access_token`s
- **Token encryption at rest** — tokens encrypted before persisting to Postgres
- **Scoped access** — API endpoints validate token ownership before returning data
- **Audit trail** — every token use, data access event, and risk decision is logged with timestamp + purpose, across both services
- **Kafka access control** — topic-level ACLs so only the Go producer and Python consumer(s) can read/write the transaction topic, especially once Kafka is deployed on a publicly reachable Oracle VM

---

## 7. Suggested Build Phases

|Phase|Focus|Est. Time|
|---|---|---|
|0|**Go ramp-up** — language fundamentals, small throwaway CLI/REST exercises before touching this architecture|1–2 weeks|
|1|Plaid Sandbox integration + basic account linking flow (Go)|3–4 days|
|2|Data normalization pipeline + Postgres schema (Go)|3–4 days|
|3|Goroutine worker pool + webhook handling + retry logic (Go)|3–4 days|
|4|Kafka producer wired up (local Docker Kafka); validate events flowing end-to-end|2–3 days|
|5|Python intelligence service: Kafka consumer + risk engine (rule-based first)|3–4 days|
|6|LLM layer: transaction enrichment, risk summaries, NL query interface (Python)|3–4 days|
|7|React dashboard (incl. chat-style query UI)|3–4 days|
|8|Security hardening + audit logging + write-up|2 days|
|9|**Deployment**: move Kafka (and services) from local Docker to Oracle Cloud free-tier VM|3–5 days|

**Total estimate:** ~5–6 weeks for a solid v1, accounting for the Go learning curve and the added service-split/Kafka complexity. Treat Phase 0 and Phase 9 as flexible — don't let either block getting the core pipeline (Phases 1–7) working end-to-end first.

---

## 8. Stretch Goals

- ML-based risk scoring instead of pure rule-based
- Snowflake + Airflow for historical analytics layer
- Multi-institution simulation (mock 3–4 "banks" with different data shapes) to stress-test normalization
- Identity-verification signal (mocked KYC checks) alongside transaction risk — closer to Plaid's Identity product
- Expand NL query interface to support multi-turn conversation ("and what about the month before?")
- Kafka consumer groups with multiple independent downstream consumers (e.g., risk engine + separate analytics logger both reading the same topic)

---

## 9. Key Design Decisions (Why, Not Just What)

This section exists to make sure every non-obvious architectural choice has a clear, defensible rationale — the kind of thing an interviewer will ask "why did you do it this way?" about.

|Decision|Rationale|
|---|---|
|**Go for ingestion, Python for intelligence**|Matches language strengths to workload: Go's concurrency model suits high-throughput, latency-sensitive I/O (webhooks); Python's ecosystem suits ML/LLM work. Mirrors real fintech infra patterns (e.g., Stripe, Monzo) rather than using one language everywhere.|
|**Kafka over Redis as the event backbone**|Redis was already used across other projects on the resume — Kafka is the more differentiated, industry-standard choice for durable, ordered, replayable event streaming between decoupled services, and better demonstrates partitioning/consumer-group concepts.|
|**Redis kept, but only for caching**|Redis is still the right tool for low-latency hot-read caching (balances, recent transactions) — this isn't removed for variety's sake, it's a correct separation of concerns from the event-streaming problem.|
|**Kafka, not direct service-to-service calls**|Decouples the Go and Python services so the intelligence layer (slower, LLM-dependent) can never block the ingestion path (fast, latency-sensitive). This is "failure isolation" applied structurally.|
|**Local Docker first, Oracle Cloud deployment last**|De-risks the project: Kafka ops (brokers, ARM compatibility, topic config) is a real learning curve on its own. Sequencing it last means a slow or stalled deployment phase doesn't block having a fully working, demoable pipeline.|
|**Oracle Cloud free tier (ARM) over a managed Kafka service**|Self-hosting teaches real broker/ops fundamentals rather than just a client API, and it's a genuinely free, always-on deployment target — traded off against ARM compatibility checks and owning uptime/security for a publicly reachable service.|

---

## 9a. Execution Plan: Kafka/Redpanda + Redis Implementation Details

This is the concrete, staged implementation of the event backbone and caching layer described above — written to prevent infrastructure setup from stalling feature progress.

### Stage 1 — Define the Event Contract (Go)

Create a strictly typed schema for `NormalizedTransactionEvent` inside the Go codebase (JSON schema or Protocol Buffers). Every event published to Kafka must contain:

- `account_id`
- `plaid_transaction_id`
- `raw_payload`
- `normalized_amount`
- `timestamp`

All Kafka messages are **partitioned by `account_id`**, enforcing strict sequential ordering per user account — this is what makes the partitioning strategy in Section 10 concrete rather than conceptual.

### Stage 2 — Use Redpanda for Local Development

Instead of running full Apache Kafka with Zookeeper or KRaft locally, run **Redpanda** via Docker Compose. Redpanda is a C++-based, Kafka-API-compatible streaming platform that boots in milliseconds, uses minimal RAM, and requires **zero code changes** when switching to standard Kafka (or a Kafka-protocol-compatible cloud service) in production. This directly de-risks the Kafka-ops learning curve flagged earlier in the plan.

### Stage 3 — Build Idempotent Python Consumers for Replayability

The Python intelligence service writes to PostgreSQL using idempotent **UPSERT** operations:

```sql
INSERT INTO transactions (...)
VALUES (...)
ON CONFLICT (plaid_transaction_id) DO UPDATE SET ...
```

This guarantees that resetting the Kafka consumer group offset back to the beginning — to re-run historical data through a new LLM prompt or an updated risk algorithm — updates the database cleanly, with no primary key violations. This is the concrete implementation of "at-least-once delivery + idempotency" from the Learning section.

### Stage 4 — Apply Cache-Aside Invalidation for Redis

Dashboard stats (`user_balance:{account_id}`, `recent_transactions:{account_id}`) are stored in Redis with a **300-second TTL**. The Python intelligence service updates (or publishes an invalidation event for) the relevant Redis key **only after** it completes risk scoring and LLM enrichment — ensuring the dashboard never serves data that looks "done" but hasn't actually been scored/enriched yet.

### Interview Framing for This Architecture

> _"By separating our event backbone from our read cache, we can trigger full historical re-enrichments of transaction data by resetting Kafka consumer group offsets — without making a single external Plaid API call, blocking the Go ingestion layer, or thrashing PostgreSQL with read locks. Redis is kept independently as a fast read cache for dashboard queries, refreshed only once enrichment is confirmed complete."_

**Note:** avoid stating specific benchmark numbers (e.g., exact P99 latency) in interviews unless you've actually measured them under load — design targets are fine to mention as goals, but should be framed as such rather than as measured facts.

---

This project is scoped to double as hands-on practice for the concepts Plaid (and most fintech infra teams) actually probe in interviews — not just a build exercise.

### High-Level Design (HLD)

- **Service Boundary Design** — Go ingestion service and Python intelligence service as independently deployable units with a clearly defined contract (Kafka topic schema)
- **Event-Driven Architecture** — reacting to Plaid webhooks (pub/sub pattern) instead of polling, and propagating normalized events via Kafka rather than direct calls
- **Producer-Consumer Pattern** — Go Kafka producer → Kafka topic (partitioned by account ID) → Python Kafka consumer
- **Data Pipeline / ETL Design** — raw data → normalization (Go) → event → enrichment/scoring (Python) → serving layer, and where each stage validates/rejects bad data
- **Caching Strategy** — Redis as cache for hot reads (balances, recent transactions), with a plan for cache invalidation on new events
- **Read/Write Path Separation** — ingestion path (webhooks → normalization → Kafka) vs. query path (dashboard/API/NL interface), each with different scaling needs and even different languages

### Low-Level Design (LLD)

- **Schema Design & Data Modeling** — a normalized transaction schema (and Kafka message schema) that absorbs inconsistent bank formats without constant migrations
- **Idempotency & Duplicate Detection** — unique constraints on `plaid_transaction_id` so retried/duplicate webhook deliveries — and retried Kafka consumption — don't create duplicate records (a real Plaid interview question)
- **Retry Logic & Backoff** — exponential backoff, max retry count, and a dead-letter topic/queue for permanently failed events
- **State Machines** — explicit states for account linking (pending → linked → error → re-auth) and event processing (queued → processing → success/failed → retried), rather than ad hoc boolean flags
- **Token/Credential Storage Design** — encryption-at-rest scheme, key rotation strategy, per-user token scoping
- **Rate Limiting** — token bucket or sliding window, both to protect your own API and to respect Plaid's rate limits

### Distributed Systems

- **Eventual Consistency** — reasoning about acceptable staleness between Kafka consumer lag, Redis cache, and Postgres source of truth
- **At-Least-Once Delivery & Idempotency** — the core distributed-systems problem underlying both webhook delivery and Kafka consumption
- **Concurrency Control** — preventing race conditions when multiple goroutines or Kafka consumer instances touch the same account (row-level locks, optimistic concurrency/version numbers, or partitioning by account ID so the same account always lands on the same partition/consumer)
- **Partitioning Strategy** — real, not just conceptual, this time: Kafka topic partitioned by account ID, giving per-account ordering guarantees and a concrete answer to "how would this scale?"
- **Failure Isolation** — the Python intelligence service failing or lagging never blocks the Go ingestion path, since they only communicate via Kafka
- **CAP-Adjacent Tradeoffs** — consistency-vs-availability decisions, e.g. what happens to event publishing if Kafka itself is temporarily unreachable (buffer and retry vs. reject the webhook)

**Why it matters:** Plaid's interview loop includes a System Design round on financial data ingestion/reliability, a Low-Level Design round on production-ready components, and (for senior candidates) a domain-depth round on fintech-specific challenges. This project's architecture maps closely onto that loop, so building it well doubles as direct interview prep.

---

## 11. Resume/Portfolio Framing

> _"Built LedgerSignal, a polyglot transaction intelligence platform on Plaid's Sandbox API — a Go service handles high-concurrency webhook ingestion and normalization via a goroutine worker pool, publishing events to Kafka; a Python service consumes those events to run rule-based risk scoring and Claude-powered enrichment (categorization, explainable risk summaries, and a natural-language query interface). Deployed Kafka on a self-hosted Oracle Cloud VM after validating the pipeline locally, with the two services fully decoupled for independent scaling and failure isolation."_

**Honesty note for interviews:** if asked about Go experience specifically, the accurate answer is "this was my first production-shaped Go project, built specifically to get hands-on with it" — not implied years of prior Go work. Interviewers respect that framing far more than an inflated claim that unravels under follow-up questions.