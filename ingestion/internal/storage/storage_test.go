package storage

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/plaid/plaid-go/v46/plaid"

	"ledgersignal/ingestion/internal/crypto"
)

// fakeDB is a hand-written stand-in for *pgxpool.Pool, implementing just the
// two methods our DBPool interface requires. Tests use this instead of a real
// Postgres connection — no Docker, no live database needed to run these.
type fakeDB struct {
	execSQL  string // the last SQL string passed to Exec, captured for inspection
	execArgs []any  // the last argument list passed to Exec
	execErr  error  // error to return from Exec, if any

	rowValue []byte // bytes to hand back from QueryRow(...).Scan(...)
	rowErr   error  // error to return from Scan, if any
}

func (f *fakeDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.execSQL = sql
	f.execArgs = args
	return pgconn.CommandTag{}, f.execErr
}

func (f *fakeDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return &fakeRow{value: f.rowValue, err: f.rowErr}
}

// fakeRow implements the one-method pgx.Row interface.
type fakeRow struct {
	value []byte
	err   error
}

func (r *fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	ptr, ok := dest[0].(*[]byte)
	if !ok {
		return errors.New("fakeRow.Scan: unsupported destination type")
	}
	*ptr = r.value
	return nil
}

// setTestEncryptionKey gives crypto.Encrypt/Decrypt a valid throwaway key for
// the duration of one test, restored automatically afterward by t.Setenv.
func setTestEncryptionKey(t *testing.T) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
}

func TestSaveItem_PassesItemIDAndTokenThrough(t *testing.T) {
	db := &fakeDB{}

	err := SaveItem(context.Background(), db, "item-123", []byte("encrypted-bytes"))
	if err != nil {
		t.Fatalf("SaveItem returned an error: %v", err)
	}

	if len(db.execArgs) != 2 {
		t.Fatalf("expected 2 args passed to Exec, got %d", len(db.execArgs))
	}
	if db.execArgs[0] != "item-123" {
		t.Errorf("expected item_id %q, got %q", "item-123", db.execArgs[0])
	}
	if string(db.execArgs[1].([]byte)) != "encrypted-bytes" {
		t.Errorf("expected encrypted token %q, got %q", "encrypted-bytes", db.execArgs[1])
	}
}

func TestSaveItem_PropagatesExecError(t *testing.T) {
	db := &fakeDB{execErr: errors.New("connection refused")}

	err := SaveItem(context.Background(), db, "item-123", []byte("x"))
	if err == nil {
		t.Fatal("expected SaveItem to return the underlying Exec error, got nil")
	}
}

func TestGetAccessToken_DecryptsStoredToken(t *testing.T) {
	setTestEncryptionKey(t)

	original := "access-sandbox-real-token"
	encrypted, err := crypto.Encrypt([]byte(original))
	if err != nil {
		t.Fatalf("failed to encrypt test fixture: %v", err)
	}

	db := &fakeDB{rowValue: encrypted}

	got, err := GetAccessToken(context.Background(), db, "item-123")
	if err != nil {
		t.Fatalf("GetAccessToken returned an error: %v", err)
	}
	if got != original {
		t.Errorf("expected decrypted token %q, got %q", original, got)
	}
}

func TestGetAccessToken_PropagatesRowError(t *testing.T) {
	db := &fakeDB{rowErr: errors.New("no rows in result set")}

	if _, err := GetAccessToken(context.Background(), db, "missing-item"); err == nil {
		t.Fatal("expected GetAccessToken to return the underlying row error, got nil")
	}
}

// buildTestTransaction constructs a minimal plaid.Transaction with only the
// fields SaveTransaction actually reads — everything else stays at its
// zero value, which is fine since we're testing our own normalization logic,
// not Plaid's full object shape.
func buildTestTransaction(t *testing.T, accountID, transactionID, name, merchantName, categoryPrimary, date string, amount float64, pending bool) plaid.Transaction {
	t.Helper()
	txn := plaid.NewTransactionWithDefaults()
	txn.SetAccountId(accountID)
	txn.SetTransactionId(transactionID)
	txn.SetName(name)
	if merchantName != "" {
		txn.SetMerchantName(merchantName)
	}
	txn.SetAmount(amount)
	txn.SetIsoCurrencyCode("USD")
	txn.SetDate(date)
	txn.SetPending(pending)
	txn.SetPersonalFinanceCategory(*plaid.NewPersonalFinanceCategory(categoryPrimary, categoryPrimary+"_DETAILED"))
	return *txn
}

func TestSaveTransaction_MerchantNameFallback(t *testing.T) {
	// Table-driven test: two scenarios, same assertions run against both.
	cases := []struct {
		name             string
		merchantName     string
		rawName          string
		wantMerchantName string
	}{
		{
			name:             "uses Plaid's clean merchant_name when present",
			merchantName:     "Uber",
			rawName:          "Uber 072515 SF**POOL**",
			wantMerchantName: "Uber",
		},
		{
			name:             "falls back to raw name when merchant_name is empty",
			merchantName:     "",
			rawName:          "AUTOMATIC PAYMENT - THANK",
			wantMerchantName: "AUTOMATIC PAYMENT - THANK",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeDB{}
			txn := buildTestTransaction(t, "acct-1", "txn-1", tc.rawName, tc.merchantName, "TRANSPORTATION", "2026-08-12", 5.40, false)

			if err := SaveTransaction(context.Background(), db, "item-1", txn); err != nil {
				t.Fatalf("SaveTransaction returned an error: %v", err)
			}

			// merchant_name is argument index 6 in the INSERT — see storage.go's
			// Exec call for the full column/argument order.
			got := db.execArgs[6].(string)
			if got != tc.wantMerchantName {
				t.Errorf("expected merchant_name %q, got %q", tc.wantMerchantName, got)
			}
		})
	}
}

func TestSaveTransaction_UsesPersonalFinanceCategoryPrimary(t *testing.T) {
	db := &fakeDB{}
	txn := buildTestTransaction(t, "acct-1", "txn-1", "Uber 072515", "Uber", "TRANSPORTATION", "2026-08-12", 5.40, false)

	if err := SaveTransaction(context.Background(), db, "item-1", txn); err != nil {
		t.Fatalf("SaveTransaction returned an error: %v", err)
	}

	// category is argument index 7.
	got := db.execArgs[7].(string)
	if got != "TRANSPORTATION" {
		t.Errorf("expected category %q, got %q", "TRANSPORTATION", got)
	}
}

func TestSaveTransaction_ParsesDateCorrectly(t *testing.T) {
	db := &fakeDB{}
	txn := buildTestTransaction(t, "acct-1", "txn-1", "Uber", "Uber", "TRANSPORTATION", "2026-08-12", 5.40, false)

	if err := SaveTransaction(context.Background(), db, "item-1", txn); err != nil {
		t.Fatalf("SaveTransaction returned an error: %v", err)
	}

	// transaction_date is argument index 8, and should be a real time.Time,
	// not the original string — that conversion is exactly what we're checking.
	got, ok := db.execArgs[8].(time.Time)
	if !ok {
		t.Fatalf("expected transaction_date to be a time.Time, got %T", db.execArgs[8])
	}
	want := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("expected transaction_date %v, got %v", want, got)
	}
}

func TestSaveTransaction_RejectsUnparsableDate(t *testing.T) {
	db := &fakeDB{}
	txn := buildTestTransaction(t, "acct-1", "txn-1", "Uber", "Uber", "TRANSPORTATION", "not-a-date", 5.40, false)

	if err := SaveTransaction(context.Background(), db, "item-1", txn); err == nil {
		t.Fatal("expected SaveTransaction to reject an unparsable date, got nil error")
	}
}

func TestSaveTransaction_PropagatesExecError(t *testing.T) {
	db := &fakeDB{execErr: errors.New("upsert failed")}
	txn := buildTestTransaction(t, "acct-1", "txn-1", "Uber", "Uber", "TRANSPORTATION", "2026-08-12", 5.40, false)

	if err := SaveTransaction(context.Background(), db, "item-1", txn); err == nil {
		t.Fatal("expected SaveTransaction to return the underlying Exec error, got nil")
	}
}
