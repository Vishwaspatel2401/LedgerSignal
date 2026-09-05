// Package webhookverify implements Plaid's documented webhook verification
// algorithm (https://plaid.com/docs/api/webhooks/webhook-verification/) —
// the fix for the gap logged in docs/caveats.md: until now, HandleWebhook
// trusted every incoming request, meaning anyone who found the (ngrok)
// webhook URL could POST a fake SYNC_UPDATES_AVAILABLE payload and trigger
// a real, expensive sync.
//
// Two independent checks matter here, not one:
//  1. The Plaid-Verification header is itself a JWT, signed by Plaid with a
//     key it publishes per key ID. Verifying that signature proves the
//     header wasn't forged.
//  2. That JWT's own request_body_sha256 claim is checked against the real
//     request body's hash. This proves the BODY wasn't altered in transit —
//     a valid header alone says nothing about whether the payload attached
//     to it is the one Plaid actually sent.
//
// Deliberately its own package (matching crypto/storage/worker's "one
// concern per package" pattern) rather than living inside internal/api —
// this doesn't know about HTTP handlers or the worker pool, only "is this
// header+body combination genuinely from Plaid."
package webhookverify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/plaid/plaid-go/v46/plaid"
)

// KeyFetcher fetches Plaid's public verification key for one key ID. Kept
// as a function type, not a concrete dependency on plaidclient or a real
// *plaid.APIClient, so Verify can be tested with a fake fetcher instead of
// making real network calls.
type KeyFetcher func(ctx context.Context, keyID string) (plaid.JWKPublicKey, error)

// maxClockSkew rejects a verification JWT whose iat (issued-at) claim is
// older than this — Plaid's own recommended replay-protection window.
const maxClockSkew = 5 * time.Minute

// Package-level cache: Plaid's keys rotate infrequently, and Plaid's own
// docs recommend caching them rather than calling /webhook_verification_key/get
// on every single webhook. Guarded by a mutex since multiple webhook
// deliveries can arrive concurrently, each on its own goroutine.
var (
	cacheMu sync.Mutex
	cache   = map[string]plaid.JWKPublicKey{}
)

// Verify returns nil if rawBody + verificationHeader together prove this
// request genuinely came from Plaid, unaltered. Any non-nil error means
// "reject this webhook" — the caller shouldn't try to distinguish between
// error types, just refuse to process the request either way.
func Verify(ctx context.Context, fetchKey KeyFetcher, verificationHeader string, rawBody []byte) error {
	if verificationHeader == "" {
		return errors.New("missing Plaid-Verification header")
	}

	// Parse the JWT's header WITHOUT verifying anything yet — we need the
	// kid (which key to check against) before we even know which key to
	// verify with. ParseUnverified is the standard escape hatch for this
	// "read metadata before you can verify" chicken-and-egg problem.
	unverified, _, err := jwt.NewParser().ParseUnverified(verificationHeader, jwt.MapClaims{})
	if err != nil {
		return fmt.Errorf("malformed webhook verification JWT: %w", err)
	}

	kid, ok := unverified.Header["kid"].(string)
	if !ok || kid == "" {
		return errors.New("webhook verification JWT missing kid")
	}

	key, err := getKey(ctx, fetchKey, kid)
	if err != nil {
		return fmt.Errorf("fetching verification key: %w", err)
	}

	pubKey, err := jwkToECDSA(key)
	if err != nil {
		return fmt.Errorf("building public key: %w", err)
	}

	// Now the real verification: parse again, this time actually checking
	// the signature against pubKey. jwt.WithValidMethods pins the algorithm
	// to ES256 — without it, a malicious token could claim a different
	// (weaker or unintended) algorithm in its own header and the library
	// would trust that claim instead of enforcing what Plaid actually uses.
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(verificationHeader, claims, func(t *jwt.Token) (interface{}, error) {
		return pubKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil {
		return fmt.Errorf("webhook verification JWT signature invalid: %w", err)
	}

	iat, ok := claims["iat"].(float64)
	if !ok {
		return errors.New("webhook verification JWT missing iat")
	}
	if time.Since(time.Unix(int64(iat), 0)) > maxClockSkew {
		return errors.New("webhook verification JWT too old (possible replay)")
	}

	claimedHash, ok := claims["request_body_sha256"].(string)
	if !ok {
		return errors.New("webhook verification JWT missing request_body_sha256")
	}
	actualHash := sha256.Sum256(rawBody)
	if claimedHash != hex.EncodeToString(actualHash[:]) {
		return errors.New("webhook body does not match verification JWT (tampered in transit?)")
	}

	return nil
}

// getKey checks the cache first, but never trusts a cached key that's been
// marked expired (Plaid sets ExpiredAt once a key is rotated out) — an
// expired entry is re-fetched rather than served stale.
func getKey(ctx context.Context, fetchKey KeyFetcher, kid string) (plaid.JWKPublicKey, error) {
	cacheMu.Lock()
	cached, found := cache[kid]
	cacheMu.Unlock()

	if found && !cached.ExpiredAt.IsSet() {
		return cached, nil
	}

	key, err := fetchKey(ctx, kid)
	if err != nil {
		return plaid.JWKPublicKey{}, err
	}

	cacheMu.Lock()
	cache[kid] = key
	cacheMu.Unlock()
	return key, nil
}

// jwkToECDSA reconstructs a Go *ecdsa.PublicKey from the JWK's raw x/y
// coordinates — the format Plaid's key-fetch endpoint returns, but not one
// any Go crypto function accepts directly.
func jwkToECDSA(key plaid.JWKPublicKey) (*ecdsa.PublicKey, error) {
	if key.Kty != "EC" || key.Crv != "P-256" {
		return nil, fmt.Errorf("unsupported key type/curve: %s/%s", key.Kty, key.Crv)
	}

	xBytes, err := base64.RawURLEncoding.DecodeString(key.X)
	if err != nil {
		return nil, fmt.Errorf("decoding x coordinate: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(key.Y)
	if err != nil {
		return nil, fmt.Errorf("decoding y coordinate: %w", err)
	}

	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(xBytes),
		Y:     new(big.Int).SetBytes(yBytes),
	}, nil
}
