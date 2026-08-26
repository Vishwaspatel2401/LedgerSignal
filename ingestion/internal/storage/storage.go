// Package storage handles every direct interaction with Postgres.
// Nothing outside this package writes raw SQL — callers just call named functions
// like SaveItem or SaveTransaction, without needing to know the actual queries involved.
package storage

import (
	"context"
	"encoding/json" // lets us convert a Go struct into JSON bytes (json.Marshal)
	"time"          // lets us parse a plain date string into a real date/time value

	"github.com/jackc/pgx/v5/pgxpool" // the Postgres driver/connection-pool library we use
	"github.com/plaid/plaid-go/v46/plaid" // needed because SaveTransaction takes a plaid.Transaction

	// Our own crypto package, imported by its module path. This is how one of our own
	// internal packages calls another — same mechanism as importing a third-party library.
	"ledgersignal/ingestion/internal/crypto"
)

// NewPool creates a connection pool to Postgres, given a full connection string
// (like postgres://user:pass@host:port/dbname). A "pool" means many queries can run
// concurrently, each briefly borrowing a connection rather than everything sharing one.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	// This function is a thin wrapper — it doesn't add logic, it just gives the rest
	// of our code a name (`storage.NewPool`) that doesn't expose which specific
	// Postgres driver library we're using underneath.
	return pgxpool.New(ctx, databaseURL)
}

// SaveItem stores (or updates) one linked account's encrypted access token.
func SaveItem(ctx context.Context, pool *pgxpool.Pool, itemID string, encryptedToken []byte) error {
	// pool.Exec runs a SQL statement that doesn't return rows (an INSERT, here).
	// The backtick-quoted string is a "raw string literal" in Go — it can span multiple
	// lines and doesn't need escape characters, which is why we use it for SQL.
	// $1 and $2 are placeholders, filled in order by the arguments listed after the query —
	// this is what prevents SQL injection, since the values are never pasted into the query text.
	_, err := pool.Exec(ctx,
		`INSERT INTO items (item_id, access_token_encrypted) VALUES ($1, $2)
		ON CONFLICT (item_id) DO UPDATE SET access_token_encrypted = EXCLUDED.access_token_encrypted`,
		itemID, encryptedToken,
	)
	// pool.Exec returns (a result summary, error) — we only care about the error here,
	// so the first value is thrown away using `_`.
	return err
}

// GetAccessToken looks up an item's encrypted token by item_id, decrypts it,
// and returns the real, usable access_token as a plain string.
func GetAccessToken(ctx context.Context, pool *pgxpool.Pool, itemID string) (string, error) {
	// Declares a variable to hold the raw encrypted bytes we're about to read from the DB.
	// `var encrypted []byte` starts it as nil/empty; QueryRow below will fill it in.
	var encrypted []byte

	// pool.QueryRow runs a SELECT expected to return at most one row.
	// .Scan(&encrypted) copies that row's one column into our `encrypted` variable.
	// The `&` means "give Scan the memory address of this variable," so it can write into it directly.
	err := pool.QueryRow(ctx,
		"SELECT access_token_encrypted FROM items WHERE item_id = $1", itemID,
	).Scan(&encrypted)
	if err != nil {
		// This covers both "a real DB error" and "no row found for this item_id" —
		// either way, we can't continue, so return an empty string and the error.
		return "", err
	}

	// Hand the encrypted bytes to our crypto package to reverse the encryption.
	decrypted, err := crypto.Decrypt(encrypted)
	if err != nil {
		return "", err
	}

	// Decrypt returns []byte; string(decrypted) converts those bytes into a normal Go string.
	return string(decrypted), nil
}

// SaveTransaction takes one raw Plaid transaction, normalizes the fields worth normalizing,
// and writes both the raw and normalized versions into the transactions table.
func SaveTransaction(ctx context.Context, pool *pgxpool.Pool, itemID string, txn plaid.Transaction) error {
	// json.Marshal converts the whole `txn` struct back into JSON bytes — this becomes
	// the untouched raw_payload column, preserving every field Plaid sent us.
	rawPayload, err := json.Marshal(txn)
	if err != nil {
		return err
	}

	// Normalization decision #1: prefer Plaid's cleaned-up merchant name...
	merchantName := txn.GetMerchantName()
	if merchantName == "" {
		// ...but if Plaid didn't have one (empty string), fall back to the raw,
		// messier `name` field instead, so this column is never blank.
		merchantName = txn.GetName()
	}

	// Normalization decision #2: use Plaid's newer category system instead of the old array one.
	// txn.GetPersonalFinanceCategory() returns a struct (not a pointer), so we store it in a
	// variable first — GetPrimary() has a "pointer receiver," meaning Go needs an addressable
	// variable to call it on; it can't be chained directly onto a function's return value.
	pfc := txn.GetPersonalFinanceCategory()
	category := pfc.GetPrimary()

	// Normalization decision #3: parse Plaid's plain date string into a real date value.
	// "2006-01-02" is Go's odd but standard way of describing a date FORMAT — it's not a
	// literal date, it's Go's reference date written in the pattern we're matching against
	// (four-digit year, dash, two-digit month, dash, two-digit day).
	transactionDate, err := time.Parse("2006-01-02", txn.GetDate())
	if err != nil {
		return err
	}

	// The main upsert: insert a new row, or if plaid_transaction_id already exists,
	// update every column instead of erroring out. EXCLUDED refers to the row we
	// attempted to insert — "set this column to the new value we were trying to insert."
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
		// These arguments fill $1 through $10, in order, matching the column list above.
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
