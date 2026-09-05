// Package api contains the HTTP layer — every route handler LedgerSignal's ingestion
// service exposes. This layer stays "thin" on purpose: it reads requests, calls into
// plaidclient/storage/crypto to do the real work, and writes responses. No SQL, no
// direct Plaid SDK calls live here.
package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/plaid/plaid-go/v46/plaid"

	"ledgersignal/ingestion/internal/crypto"
	"ledgersignal/ingestion/internal/events"
	kafkaproducer "ledgersignal/ingestion/internal/kafka"
	"ledgersignal/ingestion/internal/plaidclient"
	"ledgersignal/ingestion/internal/storage"
	"ledgersignal/ingestion/internal/webhookverify"
	"ledgersignal/ingestion/internal/worker"
)

// Server holds every dependency our handlers need to do their job.
// Instead of global variables that any function could reach into from anywhere,
// each handler gets these through this one struct — this is "dependency injection."
type Server struct {
	Plaid    *plaid.APIClient        // the configured Plaid client
	DB       *pgxpool.Pool           // the Postgres connection pool
	Pool     *worker.Pool            // the background worker pool — fed by HandleWebhook
	Producer *kafkaproducer.Producer // publishes normalized transaction events to Kafka
}

// NewServer is a "constructor" — a function whose only job is building a Server
// with its dependencies already filled in, so callers never build one by hand.
func NewServer(plaidClient *plaid.APIClient, db *pgxpool.Pool, pool *worker.Pool, producer *kafkaproducer.Producer) *Server {
	// &Server{...} creates a Server struct and immediately takes its address (&),
	// returning a pointer. We return a pointer so every handler method below shares
	// the exact same Server instance (and therefore the same DB pool/Plaid client),
	// rather than each accidentally getting its own separate copy.
	return &Server{Plaid: plaidClient, DB: db, Pool: pool, Producer: producer}
}

// fetchWebhookVerificationKey adapts plaidclient.GetWebhookVerificationKey
// (which needs s.Plaid) to webhookverify.KeyFetcher's plain function shape —
// this method value (s.fetchWebhookVerificationKey, no parentheses) is what
// gets passed into webhookverify.Verify below.
func (s *Server) fetchWebhookVerificationKey(ctx context.Context, keyID string) (plaid.JWKPublicKey, error) {
	return plaidclient.GetWebhookVerificationKey(ctx, s.Plaid, keyID)
}

// SyncItemTransactions is the actual "pull transactions and store them" logic,
// pulled out of HandleSyncTransactions so it isn't tied to any HTTP request.
// It's a plain function (not a Server method) so it can be called two ways:
// directly from a Server method (passing s.Plaid, s.DB, s.Producer), or from a
// worker pool's HandlerFunc closure in main.go, which has its own references
// to the same dependencies without needing a Server at all.
//
// As of Phase 4, every successfully stored transaction is also published to
// Kafka as a NormalizedTransactionEvent. If publishing fails, this function
// returns the error just like a DB or Plaid failure would — which means the
// worker pool's existing retry-with-backoff logic (Phase 3) automatically
// covers Kafka publish failures too, with no new retry code needed here.
func SyncItemTransactions(ctx context.Context, plaidClient *plaid.APIClient, db *pgxpool.Pool, producer *kafkaproducer.Producer, itemID string) (stored int, hasMore bool, err error) {
	accessToken, err := storage.GetAccessToken(ctx, db, itemID)
	if err != nil {
		return 0, false, err
	}

	added, hasMore, err := plaidclient.SyncTransactions(ctx, plaidClient, accessToken)
	if err != nil {
		return 0, false, err
	}

	for _, txn := range added {
		if err := storage.SaveTransaction(ctx, db, itemID, txn); err != nil {
			return stored, hasMore, err
		}

		if err := publishTransactionEvent(ctx, producer, txn); err != nil {
			return stored, hasMore, err
		}

		stored++
	}

	return stored, hasMore, nil
}

// publishTransactionEvent builds a NormalizedTransactionEvent from one raw
// Plaid transaction and publishes it. Kept separate from SyncItemTransactions
// just to keep that function's main loop readable.
func publishTransactionEvent(ctx context.Context, producer *kafkaproducer.Producer, txn plaid.Transaction) error {
	rawPayload, err := json.Marshal(txn)
	if err != nil {
		return err
	}

	// The event's timestamp is the transaction's own date, not "when this
	// event was published" — Python needs to know when the financial event
	// actually happened, not when Go happened to process it.
	transactionDate, err := time.Parse("2006-01-02", txn.GetDate())
	if err != nil {
		return err
	}

	event := events.NormalizedTransactionEvent{
		AccountID:          txn.GetAccountId(),
		PlaidTransactionID: txn.GetTransactionId(),
		RawPayload:         rawPayload,
		NormalizedAmount:   txn.GetAmount(),
		Timestamp:          transactionDate,
	}

	return producer.Publish(ctx, event)
}

// (s *Server) before the function name makes this a "method" — a function attached
// to Server, callable as `srv.HandleCreateLinkToken(...)`. `s` is how the method refers
// to the specific Server instance it was called on (here, giving it access to s.Plaid).
func (s *Server) HandleCreateLinkToken(w http.ResponseWriter, r *http.Request) {
	// r.Context() gives us the request's context.Context — used for cancellation/timeouts,
	// passed down into every downstream call so they all share the same request lifecycle.
	linkToken, err := plaidclient.CreateLinkToken(r.Context(), s.Plaid)
	if err != nil {
		// http.Error writes an error message plus an HTTP status code to the response,
		// then we `return` immediately so no further code in this function runs.
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Tell the client the response body will be JSON.
	w.Header().Set("Content-Type", "application/json")
	// json.NewEncoder(w) writes JSON directly to the response writer `w`.
	// map[string]string{...} builds a small JSON object with one key, "link_token".
	json.NewEncoder(w).Encode(map[string]string{
		"link_token": linkToken,
	})
}

func (s *Server) HandleExchangePublicToken(w http.ResponseWriter, r *http.Request) {
	// An anonymous (inline) struct type, used just to describe the shape of the
	// JSON body we expect: {"public_token": "..."}. The backtick text after the
	// field is a "struct tag" telling the JSON decoder which JSON key maps to this field.
	var body struct {
		PublicToken string `json:"public_token"`
	}
	// json.NewDecoder(r.Body) reads the raw request body; .Decode(&body) parses it
	// as JSON and fills our `body` struct. `&body` passes body's memory address so
	// Decode can write into it directly.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Grabbing the context once and reusing it below, instead of calling r.Context() repeatedly.
	ctx := r.Context()
	accessToken, itemID, err := plaidclient.ExchangePublicToken(ctx, s.Plaid, body.PublicToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// []byte(accessToken) converts the string into a byte slice, since Encrypt works on bytes.
	encryptedToken, err := crypto.Encrypt([]byte(accessToken))
	if err != nil {
		http.Error(w, "encryption failed", http.StatusInternalServerError)
		return
	}

	// storage.SaveItem does the actual database write. Note: we never keep accessToken
	// around after this point, and we never send it back in any response — only
	// encryptedToken ever touches the database, and only item_id ever leaves this function.
	if err := storage.SaveItem(ctx, s.DB, itemID, encryptedToken); err != nil {
		http.Error(w, "failed to store item", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"item_id": itemID,
		"status":  "linked",
	})
}

// HandleSandboxLink is a dev-only shortcut: it simulates an entire Link flow
// server-side, then runs the exact same store-the-token logic as above.
func (s *Server) HandleSandboxLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Ask Plaid's Sandbox-only endpoint for a fake public_token, as if a user had
	// just finished clicking through Plaid Link in a browser.
	publicToken, err := plaidclient.CreateSandboxPublicToken(ctx, s.Plaid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// From here down, this is identical to HandleExchangePublicToken — same exchange,
	// same encryption, same storage call — just starting from a Sandbox-generated
	// public_token instead of one supplied by a real frontend.
	accessToken, itemID, err := plaidclient.ExchangePublicToken(ctx, s.Plaid, publicToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	encryptedToken, err := crypto.Encrypt([]byte(accessToken))
	if err != nil {
		http.Error(w, "encryption failed", http.StatusInternalServerError)
		return
	}

	if err := storage.SaveItem(ctx, s.DB, itemID, encryptedToken); err != nil {
		http.Error(w, "failed to store item", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"item_id": itemID,
		"status":  "linked",
	})
}

// HandleSyncTransactions pulls transactions for a given item_id from Plaid,
// normalizes each one, and stores it.
func (s *Server) HandleSyncTransactions(w http.ResponseWriter, r *http.Request) {
	// r.URL.Query() parses the URL's "?key=value" part; .Get("item_id") reads
	// that specific parameter (empty string if it wasn't provided at all).
	itemID := r.URL.Query().Get("item_id")
	if itemID == "" {
		http.Error(w, "item_id query param required", http.StatusBadRequest)
		return
	}

	// All the real work now lives in SyncItemTransactions, shared with the
	// worker pool — this handler is just the HTTP wrapper around it.
	stored, hasMore, err := SyncItemTransactions(r.Context(), s.Plaid, s.DB, s.Producer, itemID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// map[string]interface{} allows mixed value types in one map (a number and a boolean here),
	// unlike map[string]string used in the other handlers, which only allows string values.
	json.NewEncoder(w).Encode(map[string]interface{}{
		"stored":   stored,
		"has_more": hasMore,
	})
}

// HandleWebhook is the real endpoint Plaid calls automatically — no curl, no
// manual trigger. It's intentionally tiny: verify it's genuinely from Plaid,
// parse just enough of the payload to know what happened, hand off any real
// work to the worker pool, and respond fast. Plaid expects webhook receivers
// to acknowledge quickly; slow or blocking work here would risk Plaid
// treating the delivery as failed and retrying it unnecessarily.
func (s *Server) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	// Read the raw bytes ourselves, rather than json.NewDecoder(r.Body)
	// straight away — webhookverify.Verify needs the exact raw body to hash
	// and compare against the verification JWT's own claim, which a decoder
	// that's already consumed and parsed the stream can't hand back.
	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	if err := webhookverify.Verify(r.Context(), s.fetchWebhookVerificationKey, r.Header.Get("Plaid-Verification"), rawBody); err != nil {
		// Logged, not returned to the caller — telling a forger exactly
		// which check failed would just help them craft a better forgery.
		log.Printf("rejected webhook: %v", err)
		http.Error(w, "webhook verification failed", http.StatusUnauthorized)
		return
	}

	// We only care about three fields out of everything Plaid might send, so
	// we decode into a small anonymous struct with just those — any other
	// fields in the JSON body (there are more, depending on webhook_type) are
	// simply ignored by the decoder rather than causing an error.
	var payload struct {
		WebhookType string `json:"webhook_type"`
		WebhookCode string `json:"webhook_code"`
		ItemID      string `json:"item_id"`
	}
	if err := json.Unmarshal(rawBody, &payload); err != nil {
		http.Error(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}

	// Plaid sends many different webhook_type/webhook_code combinations (item
	// errors, auth events, etc.) — for now we only act on the one that means
	// "new transaction data is ready to sync." Everything else is acknowledged
	// with a 200 and otherwise ignored; we're not handling those cases yet.
	if payload.WebhookType == "TRANSACTIONS" && payload.WebhookCode == "SYNC_UPDATES_AVAILABLE" {
		// Enqueue returns almost immediately (it just places the job on a
		// channel) — the actual Plaid API call and database writes happen
		// later, on a worker goroutine, well after this function has returned.
		s.Pool.Enqueue(worker.Job{ItemID: payload.ItemID})
	}

	// Respond 200 regardless of webhook_type/webhook_code — this just tells
	// Plaid "delivery received," not "processing finished."
	w.WriteHeader(http.StatusOK)
}

// HandleSetWebhook is a dev-only endpoint that tells Plaid where to send
// webhooks for a given item — needed once, so Plaid's real servers know our
// (temporary, ngrok-tunneled) public URL.
func (s *Server) HandleSetWebhook(w http.ResponseWriter, r *http.Request) {
	itemID := r.URL.Query().Get("item_id")
	if itemID == "" {
		http.Error(w, "item_id query param required", http.StatusBadRequest)
		return
	}

	var body struct {
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.WebhookURL == "" {
		http.Error(w, "webhook_url required in request body", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	// The access_token never leaves this function — it's fetched, used, and
	// discarded, same discipline as every other handler that touches it.
	accessToken, err := storage.GetAccessToken(ctx, s.DB, itemID)
	if err != nil {
		http.Error(w, "failed to get access token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := plaidclient.UpdateWebhook(ctx, s.Plaid, accessToken, body.WebhookURL); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// HandleFireWebhook is a dev-only endpoint that asks Plaid to actually send a
// real webhook for a given item — from Plaid's own servers, to whatever URL
// was set via HandleSetWebhook. This is the real end-to-end test: unlike our
// earlier synthetic curl payloads, this webhook genuinely comes from Plaid.
func (s *Server) HandleFireWebhook(w http.ResponseWriter, r *http.Request) {
	itemID := r.URL.Query().Get("item_id")
	if itemID == "" {
		http.Error(w, "item_id query param required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	accessToken, err := storage.GetAccessToken(ctx, s.DB, itemID)
	if err != nil {
		http.Error(w, "failed to get access token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := plaidclient.FireSandboxWebhook(ctx, s.Plaid, accessToken, "SYNC_UPDATES_AVAILABLE"); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusOK)
}
