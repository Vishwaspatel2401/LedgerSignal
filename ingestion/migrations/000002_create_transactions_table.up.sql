CREATE TABLE transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    item_id TEXT NOT NULL REFERENCES items(item_id),
    account_id TEXT NOT NULL,
    plaid_transaction_id TEXT UNIQUE NOT NULL,
    raw_payload JSONB NOT NULL,
    amount NUMERIC(12,2) NOT NULL,
    iso_currency_code TEXT,
    merchant_name TEXT,
    category TEXT,
    transaction_date DATE NOT NULL,
    pending BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_transactions_item_id ON transactions(item_id);