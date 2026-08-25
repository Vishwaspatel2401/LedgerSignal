package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"
	"github.com/plaid/plaid-go/v46/plaid"
	"github.com/jackc/pgx/v5/pgxpool"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"errors"
)

var plaidClient *plaid.APIClient
var dbPool *pgxpool.Pool

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on real env vars")
	}
	var err error
	dbPool, err = pgxpool.New(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal("failed to connect to postgres: ", err)
	}
	defer dbPool.Close()

	configuration := plaid.NewConfiguration()
	configuration.AddDefaultHeader("PLAID-CLIENT-ID", os.Getenv("PLAID_CLIENT_ID"))
	configuration.AddDefaultHeader("PLAID-SECRET", os.Getenv("PLAID_SECRET"))
	configuration.UseEnvironment(plaid.Sandbox)
	plaidClient = plaid.NewAPIClient(configuration)

	r := chi.NewRouter() // r is a chi router
	r.Post("/link/token", handleCreateLinkToken)
	r.Post("/link/exchange", handleExchangePublicToken)
	r.Post("/dev/sandbox-link", handleSandboxLink)
	r.Get("/dev/sync-transactions", handleSyncTransactions)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

func handleCreateLinkToken(w http.ResponseWriter, r *http.Request) { // r is request here
	ctx := context.Background()

	user := plaid.LinkTokenCreateRequestUser{
		ClientUserId: "dev-user-1",
	}
	request := plaid.NewLinkTokenCreateRequest(
		"LedgerSignal",
		"en",
		[]plaid.CountryCode{plaid.COUNTRYCODE_US},
	)
	request.SetUser(user)
	request.SetProducts([]plaid.Products{plaid.PRODUCTS_TRANSACTIONS})

	resp, _, err := plaidClient.PlaidApi.LinkTokenCreate(ctx).
		LinkTokenCreateRequest(*request).
		Execute()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"link_token": resp.GetLinkToken(),
	})
}

func exchangePublicToken(ctx context.Context, publicToken string) (accessToken, itemID string, err error) {
	resp, _, err := plaidClient.PlaidApi.ItemPublicTokenExchange(ctx).
		ItemPublicTokenExchangeRequest(
			*plaid.NewItemPublicTokenExchangeRequest(publicToken),
		).Execute()
	if err != nil {
		return "", "", err
	}
	return resp.GetAccessToken(), resp.GetItemId(), nil
}

func handleExchangePublicToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PublicToken string `json:"public_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	accessToken, itemID, err := exchangePublicToken(context.Background(), body.PublicToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}


	encryptedToken, err := encrypt([]byte(accessToken))
	if err != nil {
		http.Error(w, "encryption failed", http.StatusInternalServerError)
		return
	}

	_, err = dbPool.Exec(context.Background(),
		`INSERT INTO items (item_id, access_token_encrypted) VALUES ($1, $2)
		ON CONFLICT (item_id) DO UPDATE SET access_token_encrypted = EXCLUDED.access_token_encrypted`,
		itemID, encryptedToken,
	)
	if err != nil {
		http.Error(w, "failed to store item", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"item_id": itemID,
		"status":  "linked",
	})
}

func handleSandboxLink(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	sandboxResp, _, err := plaidClient.PlaidApi.SandboxPublicTokenCreate(ctx).
		SandboxPublicTokenCreateRequest(
			*plaid.NewSandboxPublicTokenCreateRequest(
				"ins_109508", // Plaid's canonical Sandbox test bank
				[]plaid.Products{plaid.PRODUCTS_TRANSACTIONS},
			),
		).Execute()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	accessToken, itemID, err := exchangePublicToken(ctx, sandboxResp.GetPublicToken())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	encryptedToken, err := encrypt([]byte(accessToken))
	if err != nil {
		http.Error(w, "encryption failed", http.StatusInternalServerError)
		return
	}

	_, err = dbPool.Exec(context.Background(),
		`INSERT INTO items (item_id, access_token_encrypted) VALUES ($1, $2)
		ON CONFLICT (item_id) DO UPDATE SET access_token_encrypted = EXCLUDED.access_token_encrypted`,
		itemID, encryptedToken,
	)
	if err != nil {
		http.Error(w, "failed to store item", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"item_id": itemID,
		"status":  "linked",
	})
}
func handleSyncTransactions(w http.ResponseWriter, r *http.Request) {
	itemID := r.URL.Query().Get("item_id")
	if itemID == "" {
		http.Error(w, "item_id query param required", http.StatusBadRequest)
		return
	}

	ctx := context.Background()
	accessToken, err := getAccessToken(ctx, itemID)
	if err != nil {
		http.Error(w, "failed to get access token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	request := plaid.NewTransactionsSyncRequest(accessToken)
	resp, _, err := plaidClient.PlaidApi.TransactionsSync(ctx).
		TransactionsSyncRequest(*request).
		Execute()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"added":    resp.GetAdded(),
		"has_more": resp.GetHasMore(),
	})
}

func encrypt(plaintext []byte) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(ciphertext []byte) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce, encrypted := ciphertext[:nonceSize], ciphertext[nonceSize:]

	return gcm.Open(nil, nonce, encrypted, nil)
}

func getAccessToken(ctx context.Context, itemID string) (string, error) {
	var encrypted []byte
	err := dbPool.QueryRow(ctx,
		"SELECT access_token_encrypted FROM items WHERE item_id = $1", itemID,
	).Scan(&encrypted)
	if err != nil {
		return "", err
	}

	decrypted, err := decrypt(encrypted)
	if err != nil {
		return "", err
	}
	return string(decrypted), nil
}