package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"ledgersignal/ingestion/internal/events"
)

const (
	testBrokerAddr = "localhost:19092"
	testTopic      = "kafka-producer-test"
)

// requireBroker skips the test (rather than failing it) if Redpanda isn't
// reachable — this is a real integration test against a live broker, not a
// hermetic unit test, so it needs the environment this project's docker-compose
// sets up. Skipping cleanly means it doesn't break CI/environments without
// Docker running, while still running for real whenever Redpanda is up.
func requireBroker(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", testBrokerAddr, 2*time.Second)
	if err != nil {
		t.Skipf("skipping: Redpanda not reachable at %s (%v) — start it with `docker compose up -d redpanda`", testBrokerAddr, err)
	}
	conn.Close()
}

// TestPublish_RoundTrips publishes a real event to the real (Dockerized)
// Redpanda broker, then reads it back directly from the topic to confirm
// what actually landed matches what was sent — proving the producer's
// marshaling and the Hash-balancer partitioning both work against a real
// broker, not just against mocked-out code.
func TestPublish_RoundTrips(t *testing.T) {
	requireBroker(t)

	producer := NewProducer(testBrokerAddr, testTopic)
	t.Cleanup(func() { producer.Close() })

	// A unique ID per test run, so this test can tell its own message apart
	// from anything left over in the topic by earlier runs.
	uniqueID := fmt.Sprintf("test-txn-%d", time.Now().UnixNano())
	event := events.NormalizedTransactionEvent{
		AccountID:          "test-account-1",
		PlaidTransactionID: uniqueID,
		RawPayload:         json.RawMessage(`{"note":"integration test fixture"}`),
		NormalizedAmount:   12.34,
		Timestamp:          time.Now().UTC().Truncate(time.Second),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := producer.Publish(ctx, event); err != nil {
		t.Fatalf("Publish returned an error: %v", err)
	}

	found := findMessage(t, uniqueID, 3, 8*time.Second)
	if found == nil {
		t.Fatalf("published event with plaid_transaction_id=%s was not found in the topic", uniqueID)
	}

	if found.AccountID != event.AccountID {
		t.Errorf("expected account_id %q, got %q", event.AccountID, found.AccountID)
	}
	if found.NormalizedAmount != event.NormalizedAmount {
		t.Errorf("expected normalized_amount %v, got %v", event.NormalizedAmount, found.NormalizedAmount)
	}
}

// TestPublish_SameAccountAlwaysSamePartition publishes several events for the
// same account_id and confirms they all land on the same partition — the
// actual behavior "partitioned by account_id" depends on, not just a label.
func TestPublish_SameAccountAlwaysSamePartition(t *testing.T) {
	requireBroker(t)

	producer := NewProducer(testBrokerAddr, testTopic)
	t.Cleanup(func() { producer.Close() })

	accountID := fmt.Sprintf("partition-test-account-%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for i := 0; i < 5; i++ {
		event := events.NormalizedTransactionEvent{
			AccountID:          accountID,
			PlaidTransactionID: fmt.Sprintf("%s-txn-%d", accountID, i),
			RawPayload:         json.RawMessage(`{}`),
			NormalizedAmount:   1.00,
			Timestamp:          time.Now().UTC(),
		}
		if err := producer.Publish(ctx, event); err != nil {
			t.Fatalf("Publish %d returned an error: %v", i, err)
		}
	}

	partitions := collectPartitionsForKey(t, accountID, 3, 8*time.Second)
	if len(partitions) == 0 {
		t.Fatal("found no messages for this account_id — nothing to check")
	}
	first := partitions[0]
	for i, p := range partitions {
		if p != first {
			t.Errorf("message %d landed on partition %d, expected %d (same as the first message) — same account_id should always map to the same partition", i, p, first)
		}
	}
}

// foundEvent pairs a decoded event with which partition it was read from.
type foundEvent struct {
	events.NormalizedTransactionEvent
	partition int
}

// findMessage scans every partition of testTopic for a message whose
// plaid_transaction_id matches wantTransactionID, within the given timeout.
func findMessage(t *testing.T, wantTransactionID string, numPartitions int, timeout time.Duration) *events.NormalizedTransactionEvent {
	t.Helper()
	results := scanTopic(t, numPartitions, timeout, 1, func(e events.NormalizedTransactionEvent) bool {
		return e.PlaidTransactionID == wantTransactionID
	})
	if len(results) == 0 {
		return nil
	}
	return &results[0].NormalizedTransactionEvent
}

// collectPartitionsForKey scans every partition for messages belonging to
// accountID, returning the partition number each one was found on.
func collectPartitionsForKey(t *testing.T, accountID string, numPartitions int, timeout time.Duration) []int {
	t.Helper()
	matches := scanTopic(t, numPartitions, timeout, 5, func(e events.NormalizedTransactionEvent) bool {
		return e.AccountID == accountID
	})
	partitions := make([]int, len(matches))
	for i, m := range matches {
		partitions[i] = m.partition
	}
	return partitions
}

// scanTopic reads from every partition of testTopic concurrently, from the
// earliest available offset, collecting every message that satisfies `match`,
// up to a wall-clock `timeout` as a fallback. As soon as `stopAfter` matches
// are found (0 means "no early exit"), it cancels every reader immediately —
// this is what keeps these tests fast in the normal case, while `timeout`
// guarantees they still terminate if something never shows up.
func scanTopic(t *testing.T, numPartitions int, timeout time.Duration, stopAfter int, match func(events.NormalizedTransactionEvent) bool) []foundEvent {
	t.Helper()

	type result struct {
		event     events.NormalizedTransactionEvent
		partition int
	}
	resultsCh := make(chan result, 100)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for p := 0; p < numPartitions; p++ {
		go func(partition int) {
			reader := kafkago.NewReader(kafkago.ReaderConfig{
				Brokers:   []string{testBrokerAddr},
				Topic:     testTopic,
				Partition: partition,
				MinBytes:  1,
				MaxBytes:  10e6,
			})
			defer reader.Close()
			reader.SetOffset(kafkago.FirstOffset)

			for {
				msg, err := reader.ReadMessage(ctx)
				if err != nil {
					return // context deadline hit (or cancelled), or reader closed — stop this goroutine
				}
				var e events.NormalizedTransactionEvent
				if err := json.Unmarshal(msg.Value, &e); err != nil {
					continue
				}
				if match(e) {
					resultsCh <- result{event: e, partition: partition}
				}
			}
		}(p)
	}

	var found []foundEvent
	for {
		select {
		case r := <-resultsCh:
			found = append(found, foundEvent{NormalizedTransactionEvent: r.event, partition: r.partition})
			if stopAfter > 0 && len(found) >= stopAfter {
				cancel() // stop every reader goroutine now that we have enough
				return found
			}
		case <-ctx.Done():
			return found
		}
	}
}
