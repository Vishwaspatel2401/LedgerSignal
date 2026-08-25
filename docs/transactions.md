## GET /dev/sync-transactions `func handleSyncTransactions`

Takes an `item_id` (from the URL query string), looks up and decrypts that item's `access_token` via `getAccessToken()` (see architecture.md), then calls Plaid's `/transactions/sync` to pull real transaction data for that account.

Right now this just returns the raw transaction list (`added`) and `has_more` as JSON — it doesn't write anything to Postgres yet. This is a dev/test endpoint proving the token-lookup → Plaid-call chain works, ahead of the normalization logic that will plug into it next.

## transactions table

Stores both the untouched raw data from Plaid and a normalized, decided version of each transaction — for reasons explained below.

| Column | Purpose |
|---|---|
| `id` | Internal primary key (UUID) |
| `item_id` | Foreign key to `items.item_id` — which linked account this belongs to |
| `account_id` | Plaid's account identifier within that item (one item can have multiple accounts) |
| `plaid_transaction_id` | `UNIQUE` — makes idempotent upserts possible, same as `item_id` on the `items` table |
| `raw_payload` | `JSONB` — the full, untouched transaction object from Plaid. Nothing is ever discarded. |
| `amount` | Normalized amount — kept as Plaid's own sign convention (positive = money out, negative = money in) |
| `iso_currency_code` | Currency, passed through as-is |
| `merchant_name` | Normalized merchant name: Plaid's `merchant_name` when present, falling back to the raw `name` field when it's missing |
| `category` | Normalized category — `personal_finance_category.primary` (Plaid's newer taxonomy), not the older `category` array |
| `transaction_date` | Normalized date — Plaid's `date` (posted), not `authorized_date` (often null) |
| `pending` | Passed through as-is |
| `created_at` / `updated_at` | Bookkeeping; `updated_at` matters once upserts start updating existing rows |

### Why both raw and normalized columns exist

Plaid's raw transaction data has several fields that answer the same question in more than one way — see the "messiness" walkthrough for the full breakdown, but the short version: `merchant_name` vs `name`, the old `category` array vs the new `personal_finance_category`, `date` vs `authorized_date`. Rather than picking one and losing the rest, `raw_payload` keeps everything, while the normalized columns are *one decided answer* per concept — so every other part of the app (risk scoring, the dashboard, the NL query interface) only ever has to trust one clean column instead of re-deciding this logic every time.

### Current status

The table exists (migration applied), and `/dev/sync-transactions` proves real data can be pulled from Plaid. **Not yet written**: the actual normalization function that maps a raw Plaid transaction into the normalized columns above, and the idempotent upsert (`INSERT ... ON CONFLICT (plaid_transaction_id) DO UPDATE`) that writes both raw and normalized data into this table. That's the next piece of work.
