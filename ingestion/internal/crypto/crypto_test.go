package crypto

import (
	"bytes"       // bytes.Equal compares two []byte slices for equality
	"crypto/rand" // secure randomness, used here to generate a throwaway test key
	"encoding/base64"
	"testing" // Go's built-in testing package — no third-party test library needed
)

// generateTestKey creates a fresh, valid 32-byte AES-256 key, base64-encoded
// exactly like ENCRYPTION_KEY is expected to be, and sets it as the env var
// Encrypt/Decrypt read from. t.Setenv automatically restores the previous
// value after this test finishes, so tests never leak env state into each other.
func generateTestKey(t *testing.T) {
	t.Helper() // marks this as a helper, so failures report the caller's line, not this one

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}
	t.Setenv("ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(key))
}

// TestEncryptDecryptRoundTrip is the most important property: whatever you
// encrypt, decrypting it must give back the exact original bytes.
func TestEncryptDecryptRoundTrip(t *testing.T) {
	generateTestKey(t)

	original := []byte("access-sandbox-super-secret-token")

	ciphertext, err := Encrypt(original)
	if err != nil {
		t.Fatalf("Encrypt returned an error: %v", err)
	}

	decrypted, err := Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt returned an error: %v", err)
	}

	// bytes.Equal, not `==`, because Go can't compare slices directly with `==`
	// (only arrays of a fixed size can be compared that way).
	if !bytes.Equal(original, decrypted) {
		t.Fatalf("round trip mismatch: got %q, want %q", decrypted, original)
	}
}

// TestEncryptIsRandomized checks that encrypting the exact same plaintext
// twice produces two different ciphertexts — proof the nonce really is random
// each time, which is what makes the encryption secure.
func TestEncryptIsRandomized(t *testing.T) {
	generateTestKey(t)

	plaintext := []byte("same input both times")

	first, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("first Encrypt call failed: %v", err)
	}
	second, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("second Encrypt call failed: %v", err)
	}

	if bytes.Equal(first, second) {
		t.Fatal("two encryptions of the same plaintext produced identical ciphertext — nonce is not random")
	}
}

// TestDecryptRejectsTamperedCiphertext confirms GCM's built-in authentication
// actually works: if even one byte of the ciphertext is changed after
// encryption, decryption must fail loudly instead of returning corrupted data.
func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	generateTestKey(t)

	ciphertext, err := Encrypt([]byte("do not tamper with me"))
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Flip one bit in the last byte — anywhere in the ciphertext would do,
	// since GCM authenticates the entire thing, not just specific bytes.
	tampered := make([]byte, len(ciphertext))
	copy(tampered, ciphertext)
	tampered[len(tampered)-1] ^= 0xFF // XOR with 0xFF flips every bit in that byte

	if _, err := Decrypt(tampered); err == nil {
		t.Fatal("Decrypt succeeded on tampered ciphertext — authentication check is not working")
	}
}

// TestDecryptRejectsTooShortCiphertext checks the explicit safety check in
// Decrypt: anything shorter than a single nonce can't possibly be valid, and
// should fail with a clear error rather than panicking on an invalid slice index.
func TestDecryptRejectsTooShortCiphertext(t *testing.T) {
	generateTestKey(t)

	tooShort := []byte{1, 2, 3} // nowhere near a full nonce's worth of bytes

	if _, err := Decrypt(tooShort); err == nil {
		t.Fatal("Decrypt succeeded on a ciphertext too short to be valid")
	}
}
