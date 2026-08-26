# Kafka (Redpanda) — Event Publishing

Phase 4: after a transaction is normalized and stored in Postgres, it's also published as an event — this is the actual handoff point between the Go ingestion service and the future Python intelligence service.

## What's running

**Redpanda**, not real Apache Kafka — a Kafka-protocol-compatible broker, added as a service in `docker-compose.yml`. Two listeners are configured:
- `internal://redpanda:9092` — for other containers on the Docker network (not used yet; nothing else is containerized)
- `external://localhost:19092` — for anything on the host machine, which is what the Go service (running directly on the host, not in Docker) actually connects through

See [bugs.md](bugs.md) for a real bug hit configuring this (Redpanda rejected the Pandaproxy/Schema Registry flags from a copied example — fixed by removing both, since neither is needed here).

## The event schema — `NormalizedTransactionEvent`

Defined in `internal/events/events.go`:

```go
type NormalizedTransactionEvent struct {
    AccountID          string          `json:"account_id"`
    PlaidTransactionID string          `json:"plaid_transaction_id"`
    RawPayload         json.RawMessage `json:"raw_payload"`
    NormalizedAmount   float64         `json:"normalized_amount"`
    Timestamp          time.Time       `json:"timestamp"`
}
```

This is the actual contract between Go and Python — field names use explicit `json` tags so the wire format is `snake_case`, not Go's default `CamelCase`. `Timestamp` is the transaction's own date (when the financial event happened), not "when this event was published."

## The producer — `internal/kafka/producer.go`

Wraps `segmentio/kafka-go`. One deliberate detail: the `Writer`'s `Balancer` is explicitly set to `&kafka.Hash{}`. Without this, kafka-go defaults to round-robin distribution, which would scatter one account's events across every partition at random. `Hash` means the same `Key` (we use `account_id`) always maps to the same partition — that's what "partitioned by account_id" in the architecture actually requires, not just a diagram label.

## Wiring

`SyncItemTransactions` (in `internal/api/handlers.go`) publishes one event per transaction, right after that transaction is successfully saved to Postgres. If publishing fails, the function returns an error exactly like a DB or Plaid failure would — which means the worker pool's existing retry-with-backoff logic (Phase 3) automatically covers Kafka publish failures too, with no new retry code needed.

## The topic

Created explicitly (not relying on broker auto-create, which Redpanda doesn't do by default):
```bash
docker exec linkvault-redpanda rpk topic create normalized-transactions --partitions 3 --replicas 1
```
3 partitions specifically so partitioning is actually observable, not trivially true with only one.

`KAFKA_BROKERS` and `KAFKA_TOPIC` are plain config (not secrets), read from `.env` the same way as everything else.

## Verified end-to-end

Ran `/dev/sync-transactions` for 49 transactions, then checked Redpanda directly:
```
PARTITION  HIGH-WATERMARK
0          19
1          24
2          6
```
19 + 24 + 6 = 49 — every event published successfully, genuinely spread across partitions (not clustering on one), and repeated messages for the same `account_id` were confirmed to consistently land on the same partition.

## Current status

Go → Kafka publishing is done and proven. **Nothing consumes these events yet** — that's Phase 5, the Python intelligence service. Right now, events accumulate in the topic with no reader.
