CREATE TABLE risk_signals (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plaid_transaction_id TEXT UNIQUE NOT NULL REFERENCES transactions(plaid_transaction_id),
    risk_score NUMERIC(5,2) NOT NULL,
    risk_level TEXT NOT NULL,
    reasons JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_risk_signals_risk_level ON risk_signals(risk_level);
