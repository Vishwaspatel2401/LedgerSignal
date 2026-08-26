package plaidclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/plaid/plaid-go/v46/plaid"
)

// newTestClient builds a real *plaid.APIClient, but points it at a local
// httptest server instead of Plaid's actual Sandbox — so these tests exercise
// the genuine HTTP request/response handling in the plaid-go SDK, without any
// network call ever leaving this machine. plaid-go's Configuration.Servers is
// just a list of base URLs it's willing to call, so overwriting it here is
// the only "trick" involved; everything else about the client is real.
func newTestClient(t *testing.T, handler http.HandlerFunc) *plaid.APIClient {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close) // shuts the fake server down when this test finishes

	configuration := plaid.NewConfiguration()
	configuration.Servers = plaid.ServerConfigurations{
		{URL: server.URL},
	}
	return plaid.NewAPIClient(configuration)
}

// jsonHandler is a small helper for tests that only need to return one canned
// JSON response, regardless of what request came in.
func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
}

func TestNewClient_AttachesAuthHeaders(t *testing.T) {
	var gotClientID, gotSecret string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotClientID = r.Header.Get("PLAID-CLIENT-ID")
		gotSecret = r.Header.Get("PLAID-SECRET")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"link_token":"test-token"}`))
	}))
	t.Cleanup(server.Close)

	// Build the client the real way — through NewClient — then point it at
	// the fake server afterward, so this test proves NewClient itself wires
	// the headers correctly, not just that headers CAN be set.
	client := NewClient("test-client-id", "test-secret")
	client.GetConfig().Servers = plaid.ServerConfigurations{{URL: server.URL}}

	if _, err := CreateLinkToken(context.Background(), client); err != nil {
		t.Fatalf("CreateLinkToken returned an error: %v", err)
	}

	if gotClientID != "test-client-id" {
		t.Errorf("expected PLAID-CLIENT-ID header %q, got %q", "test-client-id", gotClientID)
	}
	if gotSecret != "test-secret" {
		t.Errorf("expected PLAID-SECRET header %q, got %q", "test-secret", gotSecret)
	}
}

func TestCreateLinkToken_Success(t *testing.T) {
	client := newTestClient(t, jsonHandler(http.StatusOK, `{"link_token":"link-sandbox-abc123"}`))

	got, err := CreateLinkToken(context.Background(), client)
	if err != nil {
		t.Fatalf("CreateLinkToken returned an error: %v", err)
	}
	if got != "link-sandbox-abc123" {
		t.Errorf("expected link_token %q, got %q", "link-sandbox-abc123", got)
	}
}

func TestCreateLinkToken_PropagatesServerError(t *testing.T) {
	client := newTestClient(t, jsonHandler(http.StatusInternalServerError, `{"error_message":"boom"}`))

	if _, err := CreateLinkToken(context.Background(), client); err == nil {
		t.Fatal("expected CreateLinkToken to return an error on a 500 response, got nil")
	}
}

func TestExchangePublicToken_Success(t *testing.T) {
	client := newTestClient(t, jsonHandler(http.StatusOK, `{"access_token":"access-sandbox-xyz","item_id":"item-abc"}`))

	accessToken, itemID, err := ExchangePublicToken(context.Background(), client, "public-sandbox-123")
	if err != nil {
		t.Fatalf("ExchangePublicToken returned an error: %v", err)
	}
	if accessToken != "access-sandbox-xyz" {
		t.Errorf("expected access_token %q, got %q", "access-sandbox-xyz", accessToken)
	}
	if itemID != "item-abc" {
		t.Errorf("expected item_id %q, got %q", "item-abc", itemID)
	}
}

func TestCreateSandboxPublicToken_Success(t *testing.T) {
	client := newTestClient(t, jsonHandler(http.StatusOK, `{"public_token":"public-sandbox-999"}`))

	got, err := CreateSandboxPublicToken(context.Background(), client)
	if err != nil {
		t.Fatalf("CreateSandboxPublicToken returned an error: %v", err)
	}
	if got != "public-sandbox-999" {
		t.Errorf("expected public_token %q, got %q", "public-sandbox-999", got)
	}
}

func TestSyncTransactions_Success(t *testing.T) {
	body := `{
		"added": [
			{
				"account_id": "acct-1",
				"transaction_id": "txn-1",
				"amount": 5.4,
				"iso_currency_code": "USD",
				"date": "2026-08-12",
				"name": "Uber",
				"pending": false,
				"personal_finance_category": {"primary": "TRANSPORTATION", "detailed": "TRANSPORTATION_TAXIS"}
			}
		],
		"modified": [],
		"removed": [],
		"next_cursor": "cursor-abc",
		"has_more": false
	}`
	client := newTestClient(t, jsonHandler(http.StatusOK, body))

	added, hasMore, err := SyncTransactions(context.Background(), client, "access-sandbox-xyz")
	if err != nil {
		t.Fatalf("SyncTransactions returned an error: %v", err)
	}
	if hasMore {
		t.Error("expected has_more to be false")
	}
	if len(added) != 1 {
		t.Fatalf("expected 1 transaction, got %d", len(added))
	}
	if added[0].GetTransactionId() != "txn-1" {
		t.Errorf("expected transaction_id %q, got %q", "txn-1", added[0].GetTransactionId())
	}
}

func TestUpdateWebhook_Success(t *testing.T) {
	var gotBody map[string]interface{}
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"item":{},"request_id":"req-1"}`))
	})

	err := UpdateWebhook(context.Background(), client, "access-sandbox-xyz", "https://example.ngrok-free.dev/webhooks/plaid")
	if err != nil {
		t.Fatalf("UpdateWebhook returned an error: %v", err)
	}
	if gotBody["webhook"] != "https://example.ngrok-free.dev/webhooks/plaid" {
		t.Errorf("expected request body to include the new webhook URL, got %v", gotBody["webhook"])
	}
}

func TestFireSandboxWebhook_Success(t *testing.T) {
	var gotBody map[string]interface{}
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"webhook_fired":true}`))
	})

	err := FireSandboxWebhook(context.Background(), client, "access-sandbox-xyz", "SYNC_UPDATES_AVAILABLE")
	if err != nil {
		t.Fatalf("FireSandboxWebhook returned an error: %v", err)
	}
	if gotBody["webhook_code"] != "SYNC_UPDATES_AVAILABLE" {
		t.Errorf("expected webhook_code %q in request body, got %v", "SYNC_UPDATES_AVAILABLE", gotBody["webhook_code"])
	}
}
