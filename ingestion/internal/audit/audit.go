// Package audit implements a small, persistent, queryable log of
// security-relevant events. Before this, the only record of "did someone
// try to forge a webhook" or "when was this account linked" was whatever
// happened to still be in log.Printf's stdout output — ephemeral, not
// queryable, gone the moment the process restarts. This writes the same
// kind of event to Postgres instead, where it can actually be searched,
// counted, and kept.
//
// Deliberately narrow: this package only knows how to write one row to
// audit_log. It doesn't know what a webhook or a worker pool is — callers
// decide what's worth logging and pass in plain strings/maps, same "one
// concern per package" pattern as crypto/storage/worker/ratelimit.
package audit

import (
	"context"
	"encoding/json"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Logger writes audit events to Postgres.
type Logger struct {
	db *pgxpool.Pool
}

// NewLogger builds a Logger against an existing connection pool — the same
// pool everything else already uses, not a separate connection.
func NewLogger(db *pgxpool.Pool) *Logger {
	return &Logger{db: db}
}

// Log records one event. itemID may be empty if the event isn't tied to a
// specific linked account (e.g. a rejected webhook with no valid payload to
// read an item_id from at all).
//
// Errors are logged, not returned — a failure to WRITE an audit record
// should never be the reason a real request fails. Audit logging is meant
// to observe the system, not become a new way for it to break.
func (l *Logger) Log(ctx context.Context, eventType, itemID string, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}

	detailJSON, err := json.Marshal(detail)
	if err != nil {
		log.Printf("audit: failed to marshal detail for event_type=%s: %v", eventType, err)
		return
	}

	// item_id is nullable in the table; an empty string is stored as NULL
	// rather than as a literal empty string, so queries like
	// "WHERE item_id IS NULL" behave the way anyone reading the schema
	// would expect.
	var itemIDArg any
	if itemID != "" {
		itemIDArg = itemID
	}

	_, err = l.db.Exec(ctx,
		`INSERT INTO audit_log (event_type, item_id, detail) VALUES ($1, $2, $3)`,
		eventType, itemIDArg, detailJSON,
	)
	if err != nil {
		log.Printf("audit: failed to write event_type=%s: %v", eventType, err)
	}
}
