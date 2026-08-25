package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/plaid/plaid-go/v46/plaid"

	"ledgersignal/ingestion/internal/crypto"
)

func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, databaseURL)
}

func SaveItem(ctx context.Context, pool *pgxpool.Pool, itemID string, encryptedToken []byte) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO items (item_id, access_token_encrypted) VALUES ($1, $2)
		ON CONFLICT (item_id) DO UPDATE SET access_token_encrypted = EXCLUDED.access_token_encrypted`,
		itemID, encryptedToken,
	)
	return err
}

func GetAccessToken(ctx context.Context, pool *pgxpool.Pool, itemID string) (string, error) {
	var encrypted []byte
	err := pool.QueryRow(ctx,
		"SELECT access_token_encrypted FROM items WHERE item_id = $1", itemID,
	).Scan(&encrypted)
	if err != nil {
		return "", err
	}

	decrypted, err := crypto.Decrypt(encrypted)
	if err != nil {
		return "", err
	}
	return string(decrypted), nil
}

func SaveTransaction(ctx context.Context, pool *pgxpool.Pool, itemID string, txn plaid.Transaction) error {
	rawPayload, err := json.Marshal(txn)
	if err != nil {
		return err
	}

	merchantName := txn.GetMerchantName()
	if merchantName == "" {
		merchantName = txn.GetName()
	}

	pfc := txn.GetPersonalFinanceCategory()
	category := pfc.GetPrimary()

	transactionDate, err := time.Parse("2006-01-02", txn.GetDate())
	if err != nil {
		return err
	}

	_, err = pool.Exec(ctx,
		`INSERT INTO transactions (
			item_id, account_id, plaid_transaction_id, raw_payload,
			amount, iso_currency_code, merchant_name, category,
			transaction_date, pending, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (plaid_transaction_id) DO UPDATE SET
			raw_payload = EXCLUDED.raw_payload,
			amount = EXCLUDED.amount,
			iso_currency_code = EXCLUDED.iso_currency_code,
			merchant_name = EXCLUDED.merchant_name,
			category = EXCLUDED.category,
			transaction_date = EXCLUDED.transaction_date,
			pending = EXCLUDED.pending,
			updated_at = now()`,
		itemID,
		txn.GetAccountId(),
		txn.GetTransactionId(),
		rawPayload,
		txn.GetAmount(),
		txn.GetIsoCurrencyCode(),
		merchantName,
		category,
		transactionDate,
		txn.GetPending(),
	)
	return err
}