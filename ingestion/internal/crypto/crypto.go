// Package crypto handles encrypting and decrypting sensitive data (like Plaid access tokens)
// before they're stored in Postgres, and reading them back out again.
package crypto

// Go groups imports in one block. These are all from Go's standard library —
// nothing here is a third-party dependency.
import (
	"crypto/aes"       // implements the AES cipher algorithm itself
	"crypto/cipher"    // generic cipher interfaces, including GCM (the mode we use on top of AES)
	"crypto/rand"      // a cryptographically secure random number source (NOT math/rand, which is not secure)
	"encoding/base64"  // lets us turn our base64-text encryption key back into raw bytes
	"errors"           // lets us create simple custom error values with errors.New(...)
	"io"               // gives us io.ReadFull, used to fill a byte slice completely from a reader
	"os"               // lets us read environment variables like ENCRYPTION_KEY
)

// Encrypt takes plaintext bytes (e.g. a Plaid access_token) and returns them encrypted.
// It returns ([]byte, error) — Go's standard pattern: every risky operation returns
// a result AND an error, and the caller is expected to check the error before trusting the result.
func Encrypt(plaintext []byte) ([]byte, error) {
	// os.Getenv reads the ENCRYPTION_KEY value from .env (loaded earlier in main()).
	// It's stored as base64 text, so we decode it back into raw bytes here.
	key, err := base64.StdEncoding.DecodeString(os.Getenv("ENCRYPTION_KEY"))
	if err != nil {
		// If decoding fails, we can't continue — return immediately with nil (no result) and the error.
		return nil, err
	}

	// aes.NewCipher builds an AES "cipher block" using our key. This is the core encryption
	// algorithm, but on its own it can only encrypt data exactly one block (16 bytes) at a time.
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// GCM ("Galois/Counter Mode") wraps the raw AES block so it can encrypt data of any length,
	// AND it adds authentication — meaning if the encrypted data is tampered with, decryption
	// will fail loudly instead of silently returning corrupted data.
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// A "nonce" (number used once) must be random and different every single time we encrypt,
	// even if we're encrypting the exact same plaintext twice. This is what makes two encryptions
	// of the same access_token produce two completely different-looking ciphertexts.
	// make([]byte, n) creates a byte slice of length n, filled with zeros for now.
	nonce := make([]byte, gcm.NonceSize())

	// io.ReadFull(rand.Reader, nonce) fills the `nonce` slice completely with secure random bytes,
	// overwriting the zeros. The `if _, err := ...; err != nil` pattern runs a statement and
	// checks its error in the same line, without keeping the first return value.
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// gcm.Seal does the actual encryption. Its first argument is a "destination slice" to
	// append the result onto — passing `nonce` here means the output starts with the nonce
	// bytes, followed immediately by the real ciphertext. That's a deliberate trick: it means
	// the nonce travels attached to the ciphertext, so Decrypt() can pull it back off later
	// without needing it stored anywhere else.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt reverses Encrypt — given ciphertext (with the nonce stuck to the front of it,
// exactly as Encrypt left it), it returns the original plaintext bytes.
func Decrypt(ciphertext []byte) ([]byte, error) {
	// Same key-loading and cipher setup as Encrypt — decrypting needs the identical key
	// that was used to encrypt, or this will fail.
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

	// gcm.NonceSize() tells us how many bytes long the nonce is (a fixed, known size).
	nonceSize := gcm.NonceSize()

	// Safety check: if the ciphertext is shorter than a single nonce, something is very wrong
	// (corrupted data, wrong key, etc.) — bail out with a clear error instead of crashing below.
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	// Slice syntax `ciphertext[:nonceSize]` means "everything from the start up to (not including)
	// index nonceSize" — that's the nonce we tucked onto the front during Encrypt.
	// `ciphertext[nonceSize:]` means "everything from nonceSize to the end" — the real encrypted data.
	// This one line declares and assigns two variables at once, split by the comma.
	nonce, encrypted := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// gcm.Open is the reverse of gcm.Seal — it decrypts `encrypted` using `nonce`, and also
	// verifies the data wasn't tampered with (thanks to GCM's built-in authentication).
	// If verification fails, this returns an error instead of garbage plaintext.
	return gcm.Open(nil, nonce, encrypted, nil)
}
