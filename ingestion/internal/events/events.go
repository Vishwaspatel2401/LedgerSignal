// Package events defines the event schema published to Kafka — the actual
// "contract" between the Go ingestion service and the future Python
// intelligence service. Unlike our internal structs, this one gets serialized
// to JSON and read by a completely different program, so field names and
// types need to stay stable and unambiguous, not just convenient for Go.
package events

import (
	"encoding/json"
	"time"
)

// NormalizedTransactionEvent is the message published to Kafka for every
// transaction, per the architecture plan (README Section 9a). Field names
// use explicit `json` tags so the wire format is snake_case, matching the
// rest of the project's JSON — Go's default (no tags) would serialize
// struct fields in CamelCase instead, which Python would then have to
// special-case around.
type NormalizedTransactionEvent struct {
	AccountID          string          `json:"account_id"`
	PlaidTransactionID string          `json:"plaid_transaction_id"`
	RawPayload         json.RawMessage `json:"raw_payload"`
	NormalizedAmount   float64         `json:"normalized_amount"`
	Timestamp          time.Time       `json:"timestamp"`
}
