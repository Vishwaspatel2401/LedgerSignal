## POST /link/token 

Calls Plaid to generate `link token` - this is what the frontend would use to open Plaid link and start the bank connection flow 
`fun handleCreateLinkToken`
Needs - PLAID_CLIENT_ID and PLAID_SECRET from .env 
returns {'link_token' = "...."}


`func exchangePublicToken` - Helper Function, takes a `publicToken` and calls Plaid to exchange with real `Access_token` and `item_id`. Both  `fun handleCreateLinkToken` and `handleExchangePublicToken` calls this so the exchange logic only exchange once

## POST /link/exchange `func handleExchangePublicToken`
The real, production-shaped endpoint for completing account linking. Takes a public_token (from the request body), calls `exchangePublicToken` to get an access_token, encrypts it with `encrypt()`, and stores it in the items table. Returns just item_id and a status — never the token itself.

## POST /dev/sandbox-link `func handleSandboxLink`
A dev-only shortcut. Simulates a complete Plaid Link flow entirely server-side (no frontend needed) by calling Plaid's Sandbox-only endpoint to generate a fake `public_token`, then runs the exact same exchange-and-store logic as `handleExchangePublicToken`. Exists purely so the linking flow could be tested before any UI was built. 

## encrypt()
Takes plaintext bytes (an `access_token`) and returns them encrypted, using AES-256-GCM with the key from `ENCRYPTION_KEY`. Generates a fresh random nonce every time it's called, so the same input never produces the same output twice.

## decrypt()
The reverse of encrypt() — takes ciphertext, pulls the nonce back off the front of it, and returns the original plaintext. Uses the same `ENCRYPTION_KEY`.

## getAccessToken()
Given an `item_id`, looks up its encrypted token in Postgres and decrypts it in one step. Every function that needs to actually talk to Plaid on behalf of a linked account calls this rather than touching the database or `decrypt()` directly.