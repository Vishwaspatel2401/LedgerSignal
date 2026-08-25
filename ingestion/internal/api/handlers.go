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

type Server struct {
	Plaid *plaid.APIClient
	DB    *pgxpool.Pool
}

func NewServer(plaidClient *plaid.APIClient, db *pgxpool.Pool) *Server {
	return &Server{Plaid: plaidClient, DB: db}
}

func (s *Server) HandleCreateLinkToken(w http.ResponseWriter, r *http.Request) {
	linkToken, err := plaidclient.CreateLinkToken(r.Context(), s.Plaid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"link_token": linkToken,
	})
}

func (s *Server) HandleExchangePublicToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PublicToken string `json:"public_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	accessToken, itemID, err := plaidclient.ExchangePublicToken(ctx, s.Plaid, body.PublicToken)
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

func (s *Server) HandleSandboxLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	publicToken, err := plaidclient.CreateSandboxPublicToken(ctx, s.Plaid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

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

func (s *Server) HandleSyncTransactions(w http.ResponseWriter, r *http.Request) {
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

	added, hasMore, err := plaidclient.SyncTransactions(ctx, s.Plaid, accessToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	stored := 0
	for _, txn := range added {
		if err := storage.SaveTransaction(ctx, s.DB, itemID, txn); err != nil {
			http.Error(w, "failed to store transaction "+txn.GetTransactionId()+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		stored++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"stored":   stored,
		"has_more": hasMore,
	})
}