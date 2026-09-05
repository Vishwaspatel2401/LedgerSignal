// Package plaidclient wraps every direct call to the Plaid SDK.
// Nothing outside this package should ever call the plaid-go library directly —
// this is the ONE place that knows Plaid's API shape, so if Plaid changes something,
// only this file needs to change.
package plaidclient

import (
	"context" // context.Context carries request-scoped data (like cancellation signals) through calls

	// This is Plaid's official Go SDK — a third-party dependency we `go get`-ed earlier.
	"github.com/plaid/plaid-go/v46/plaid"
)

// NewClient builds and configures a Plaid API client, ready to make calls.
// It takes the Client ID and Secret as plain arguments rather than reading env vars itself —
// that keeps this function reusable/testable without depending on global environment state.
func NewClient(clientID, secret string) *plaid.APIClient {
	// plaid.NewConfiguration() creates a blank configuration object we then customize below.
	configuration := plaid.NewConfiguration()

	// Plaid authenticates every request using these two HTTP headers.
	// AddDefaultHeader means "attach this header to every request this client ever makes."
	configuration.AddDefaultHeader("PLAID-CLIENT-ID", clientID)
	configuration.AddDefaultHeader("PLAID-SECRET", secret)

	// Tells the client to talk to Plaid's Sandbox environment (fake banks, fake data)
	// instead of Production. This is the one line that would change to go live for real.
	configuration.UseEnvironment(plaid.Sandbox)

	// plaid.NewAPIClient builds the actual client object using the configuration above,
	// and we return a pointer to it (*plaid.APIClient) so callers can reuse the same
	// client instance for every request, instead of rebuilding it each time.
	return plaid.NewAPIClient(configuration)
}

// CreateLinkToken asks Plaid for a link_token — the token a frontend uses to open
// Plaid Link and start a bank-connection flow.
func CreateLinkToken(ctx context.Context, client *plaid.APIClient) (string, error) {
	// This struct just identifies which user is linking an account, from Plaid's point of view.
	// In a real multi-user app, "dev-user-1" would instead be the real logged-in user's ID.
	user := plaid.LinkTokenCreateRequestUser{
		ClientUserId: "dev-user-1",
	}

	// plaid.NewLinkTokenCreateRequest builds the request body Plaid expects:
	// a display name for your app, a language code, and which countries to support.
	request := plaid.NewLinkTokenCreateRequest(
		"LedgerSignal",
		"en",
		[]plaid.CountryCode{plaid.COUNTRYCODE_US}, // a slice containing just one country code
	)

	// These two lines attach extra fields onto the request object we just built,
	// using "setter" methods (Go's SDK convention, since Go doesn't have named/optional
	// constructor arguments like some other languages).
	request.SetUser(user)
	request.SetProducts([]plaid.Products{plaid.PRODUCTS_TRANSACTIONS})

	// This is a "method chain" — each `.` calls another method on the result of the last one.
	// Read top to bottom: get the LinkTokenCreate call builder, attach our request to it,
	// then Execute() actually sends the HTTP request to Plaid and waits for the response.
	// It returns three things: the parsed response, the raw HTTP response (ignored via `_`),
	// and an error.
	resp, _, err := client.PlaidApi.LinkTokenCreate(ctx).
		LinkTokenCreateRequest(*request).
		Execute()
	if err != nil {
		// On failure, return an empty string alongside the error — the caller must check
		// the error before ever trusting the string.
		return "", err
	}

	// resp.GetLinkToken() reads the link_token field out of Plaid's response struct.
	return resp.GetLinkToken(), nil
}

// ExchangePublicToken takes a public_token (short-lived, from a completed Link flow)
// and exchanges it for a permanent access_token + item_id.
// Named return values `(accessToken, itemID string, err error)` mean the function
// signature itself documents what each returned value represents.
func ExchangePublicToken(ctx context.Context, client *plaid.APIClient, publicToken string) (accessToken, itemID string, err error) {
	resp, _, err := client.PlaidApi.ItemPublicTokenExchange(ctx).
		ItemPublicTokenExchangeRequest(
			// plaid.NewItemPublicTokenExchangeRequest builds the request body containing
			// the public_token. The `*` dereferences the pointer it returns, since the
			// method call here expects the actual struct value, not a pointer to it.
			*plaid.NewItemPublicTokenExchangeRequest(publicToken),
		).Execute()
	if err != nil {
		// Three-value return: on error, give back empty strings for both tokens plus the error.
		return "", "", err
	}
	// Pull both fields out of the response and return them together.
	return resp.GetAccessToken(), resp.GetItemId(), nil
}

// CreateSandboxPublicToken simulates a completed Link flow entirely on Plaid's side —
// no real bank login, no frontend needed. Sandbox-only; this endpoint doesn't exist in Production.
func CreateSandboxPublicToken(ctx context.Context, client *plaid.APIClient) (string, error) {
	resp, _, err := client.PlaidApi.SandboxPublicTokenCreate(ctx).
		SandboxPublicTokenCreateRequest(
			*plaid.NewSandboxPublicTokenCreateRequest(
				"ins_109508", // Plaid's canonical Sandbox test bank
				[]plaid.Products{plaid.PRODUCTS_TRANSACTIONS}, // which Plaid products to simulate
			),
		).Execute()
	if err != nil {
		return "", err
	}
	return resp.GetPublicToken(), nil
}

// SyncTransactions pulls transaction data for a given access_token using Plaid's
// /transactions/sync endpoint. Named return values again document the shape:
// a list of new transactions, whether there's more data to page through, and an error.
func SyncTransactions(ctx context.Context, client *plaid.APIClient, accessToken string) (added []plaid.Transaction, hasMore bool, err error) {
	// Builds the sync request. We're not setting a cursor here, which tells Plaid
	// "give me everything from the very beginning" rather than "just what changed since X."
	request := plaid.NewTransactionsSyncRequest(accessToken)

	resp, _, err := client.PlaidApi.TransactionsSync(ctx).
		TransactionsSyncRequest(*request).
		Execute()
	if err != nil {
		// On error, return nil for the slice (an empty/absent list), false for hasMore,
		// and the error itself.
		return nil, false, err
	}

	// resp.GetAdded() returns the slice of new transactions Plaid found.
	// resp.GetHasMore() tells us whether there's a further page we haven't fetched yet.
	return resp.GetAdded(), resp.GetHasMore(), nil
}

// UpdateWebhook tells Plaid which URL to POST webhooks to for a given item —
// this is what makes Plaid's servers actually able to reach ours, instead of
// us only ever reaching out to Plaid.
func UpdateWebhook(ctx context.Context, client *plaid.APIClient, accessToken, webhookURL string) error {
	request := plaid.NewItemWebhookUpdateRequest(accessToken)
	request.SetWebhook(webhookURL)

	_, _, err := client.PlaidApi.ItemWebhookUpdate(ctx).
		ItemWebhookUpdateRequest(*request).
		Execute()
	return err
}

// FireSandboxWebhook asks Plaid to actually send a real webhook — from Plaid's
// own servers, over the real internet — to whatever URL was set via
// UpdateWebhook. This is Sandbox-only; it's how we test the real delivery path
// without waiting for a webhook to happen to fire naturally.
func FireSandboxWebhook(ctx context.Context, client *plaid.APIClient, accessToken, webhookCode string) error {
	request := plaid.NewSandboxItemFireWebhookRequest(accessToken, webhookCode)

	_, _, err := client.PlaidApi.SandboxItemFireWebhook(ctx).
		SandboxItemFireWebhookRequest(*request).
		Execute()
	return err
}

// GetWebhookVerificationKey fetches Plaid's public key for one key ID (a
// "kid" pulled from an incoming webhook's Plaid-Verification header) — the
// key webhookverify.Verify checks that header's signature against. See
// https://plaid.com/docs/api/webhooks/webhook-verification/.
func GetWebhookVerificationKey(ctx context.Context, client *plaid.APIClient, keyID string) (plaid.JWKPublicKey, error) {
	resp, _, err := client.PlaidApi.WebhookVerificationKeyGet(ctx).
		WebhookVerificationKeyGetRequest(*plaid.NewWebhookVerificationKeyGetRequest(keyID)).
		Execute()
	if err != nil {
		return plaid.JWKPublicKey{}, err
	}
	return resp.GetKey(), nil
}
