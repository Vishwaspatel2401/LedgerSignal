package webhookverify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/plaid/plaid-go/v46/plaid"
)

// Real Plaid webhooks can't be tested here without a public URL (ngrok) and
// a live Sandbox delivery — so this signs a JWT with a throwaway key,
// exactly the shape Plaid's real ones take, and points Verify at a fake
// KeyFetcher instead of a real Plaid API call. This proves the actual
// crypto/parsing logic is correct (not just "garbage input gets rejected",
// which a broken implementation would also do).
func generateSignedWebhook(t *testing.T, body []byte, iat time.Time) (string, KeyFetcher) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}

	bodyHash := sha256.Sum256(body)

	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iat":                 iat.Unix(),
		"request_body_sha256": hex.EncodeToString(bodyHash[:]),
	})
	token.Header["kid"] = "test-key-id"

	signed, err := token.SignedString(priv)
	if err != nil {
		t.Fatalf("signing test token: %v", err)
	}

	// The x/y coordinates Plaid's real /webhook_verification_key/get would
	// return — same base64url, no-padding encoding used throughout the JWK spec.
	fetcher := func(ctx context.Context, keyID string) (plaid.JWKPublicKey, error) {
		return plaid.JWKPublicKey{
			Kty: "EC",
			Crv: "P-256",
			Kid: keyID,
			X:   base64.RawURLEncoding.EncodeToString(priv.PublicKey.X.Bytes()),
			Y:   base64.RawURLEncoding.EncodeToString(priv.PublicKey.Y.Bytes()),
		}, nil
	}

	return signed, fetcher
}

func TestVerify_AcceptsGenuineWebhook(t *testing.T) {
	body := []byte(`{"webhook_type":"TRANSACTIONS","webhook_code":"SYNC_UPDATES_AVAILABLE","item_id":"real-item"}`)
	token, fetcher := generateSignedWebhook(t, body, time.Now())

	if err := Verify(context.Background(), fetcher, token, body); err != nil {
		t.Errorf("expected a correctly signed, untampered webhook to be accepted, got error: %v", err)
	}
}

func TestVerify_RejectsTamperedBody(t *testing.T) {
	originalBody := []byte(`{"webhook_type":"TRANSACTIONS","webhook_code":"SYNC_UPDATES_AVAILABLE","item_id":"real-item"}`)
	token, fetcher := generateSignedWebhook(t, originalBody, time.Now())

	// The signature is valid, but the body arriving at Verify doesn't match
	// what was actually signed — simulating a body altered in transit.
	tamperedBody := []byte(`{"webhook_type":"TRANSACTIONS","webhook_code":"SYNC_UPDATES_AVAILABLE","item_id":"attacker-item"}`)

	if err := Verify(context.Background(), fetcher, token, tamperedBody); err == nil {
		t.Error("expected a tampered body to be rejected, but Verify returned nil")
	}
}

func TestVerify_RejectsExpiredToken(t *testing.T) {
	body := []byte(`{"webhook_type":"TRANSACTIONS","webhook_code":"SYNC_UPDATES_AVAILABLE","item_id":"real-item"}`)
	token, fetcher := generateSignedWebhook(t, body, time.Now().Add(-10*time.Minute))

	if err := Verify(context.Background(), fetcher, token, body); err == nil {
		t.Error("expected a webhook signed 10 minutes ago to be rejected as a possible replay, but Verify returned nil")
	}
}

func TestVerify_RejectsMissingHeader(t *testing.T) {
	if err := Verify(context.Background(), nil, "", []byte("{}")); err == nil {
		t.Error("expected a missing Plaid-Verification header to be rejected, but Verify returned nil")
	}
}

func TestVerify_RejectsMalformedJWT(t *testing.T) {
	if err := Verify(context.Background(), nil, "not.a.real.jwt", []byte("{}")); err == nil {
		t.Error("expected a malformed JWT to be rejected, but Verify returned nil")
	}
}
