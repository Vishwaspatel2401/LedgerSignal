// Package kafka wraps every direct interaction with the Kafka/Redpanda
// producer client. Same pattern as plaidclient and storage: nothing outside
// this package imports segmentio/kafka-go directly.
package kafka

import (
	"context"
	"encoding/json"

	kafkago "github.com/segmentio/kafka-go"

	"ledgersignal/ingestion/internal/events"
)

// Producer publishes NormalizedTransactionEvents to one Kafka topic.
type Producer struct {
	writer *kafkago.Writer
}

// NewProducer builds a Producer connected to the given broker address(es)
// (e.g. "localhost:19092" — Redpanda's external listener), publishing to the
// given topic. The Hash balancer means messages with the same Key always land
// on the same partition — that's what "partitioned by account_id" actually
// requires: without an explicit balancer, kafka-go defaults to round-robin,
// which would scatter one account's events across every partition instead of
// keeping them ordered together.
func NewProducer(brokerAddr, topic string) *Producer {
	return &Producer{
		writer: &kafkago.Writer{
			Addr:     kafkago.TCP(brokerAddr),
			Topic:    topic,
			Balancer: &kafkago.Hash{},
		},
	}
}

// Publish sends one event to the topic, using AccountID as the partition key.
func (p *Producer) Publish(ctx context.Context, event events.NormalizedTransactionEvent) error {
	value, err := json.Marshal(event)
	if err != nil {
		return err
	}

	return p.writer.WriteMessages(ctx, kafkago.Message{
		// Key determines the partition (via the Hash balancer above) — every
		// event for this account_id will always be routed to the same
		// partition, giving per-account ordering.
		Key:   []byte(event.AccountID),
		Value: value,
	})
}

// Close releases the writer's underlying connections. Should be called once,
// on program shutdown — mirrors how the DB pool is closed in main().
func (p *Producer) Close() error {
	return p.writer.Close()
}
