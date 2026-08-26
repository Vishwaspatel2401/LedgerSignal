// Package api contains the HTTP layer — every route handler LedgerSignal's ingestion
// service exposes. This layer stays "thin" on purpose: it reads requests, calls into
// plaidclient/storage/crypto to do the real work, and writes responses. No SQL, no
// direct Plaid SDK calls live here.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/plaid/plaid-go/v46/plaid"

	"ledgersignal/ingestion/internal/crypto"
	"ledgersignal/ingestion/internal/plaidclient"
	"ledgersignal/ingestion/internal/storage"
)

// Server holds every dependency our handlers need to do their job.
// Instead of global variables that any function could reach into from anywhere,
// each handler gets these through this one struct — this is "dependency injection."
type Server struct {
	Plaid *plaid.APIClient // the configured Plaid client
	DB    *pgxpool.Pool    // the Postgres connection pool
}

// NewServer is a "constructor" — a function whose only job is building a Server
// with its dependencies already filled in, so callers never build one by hand.
func NewServer(plaidClient *plaid.APIClient, db *pgxpool.Pool) *Server {
	// &Server{...} creates a Server struct and immediately takes its address (&),
	// returning a pointer. We return a pointer so every handler method below shares
	// the exact same Server instance (and therefore the same DB pool/Plaid client),
	// rather than each accidentally getting its own separate copy.
	return &Server{Plaid: plaidClient, DB: db}
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

	ctx := r.Context()
	// Look up this item's real access_token (already decrypted for us by this call).
	accessToken, err := storage.GetAccessToken(ctx, s.DB, itemID)
	if err != nil {
		// string concatenation with `+` builds a more specific error message,
		// including the underlying error's own text.
		http.Error(w, "failed to get access token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Ask Plaid for this account's transactions.
	added, hasMore, err := plaidclient.SyncTransactions(ctx, s.Plaid, accessToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// A running counter of how many transactions we successfully stored.
	stored := 0
	// `for _, txn := range added` loops over every element in the `added` slice.
	// `_` throws away the index (we don't need "which position in the list"),
	// `txn` is the current transaction on each pass through the loop.
	for _, txn := range added {
		if err := storage.SaveTransaction(ctx, s.DB, itemID, txn); err != nil {
			// If even one transaction fails to save, stop immediately and report which
			// one failed, rather than silently skipping it or storing a partial result.
			http.Error(w, "failed to store transaction "+txn.GetTransactionId()+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		// `stored++` increments the counter by one — shorthand for `stored = stored + 1`.
		stored++
	}

	w.Header().Set("Content-Type", "application/json")
	// map[string]interface{} allows mixed value types in one map (a number and a boolean here),
	// unlike map[string]string used in the other handlers, which only allows string values.
	json.NewEncoder(w).Encode(map[string]interface{}{
		"stored":   stored,
		"has_more": hasMore,
	})
}
