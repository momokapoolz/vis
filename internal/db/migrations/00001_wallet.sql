-- +goose Up
CREATE TABLE users (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email TEXT NOT NULL UNIQUE CHECK (email = lower(btrim(email))),
    password_digest TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'customer' CHECK (role IN ('customer','support','admin')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE refresh_tokens (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    token_digest TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE wallets (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT REFERENCES users(id),
    kind TEXT NOT NULL DEFAULT 'customer' CHECK (kind IN ('customer','system')),
    currency TEXT NOT NULL DEFAULT 'VND' CHECK (currency = 'VND'),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','blocked')),
    name TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((kind = 'customer' AND user_id IS NOT NULL) OR (kind = 'system' AND user_id IS NULL))
);
CREATE UNIQUE INDEX one_system_wallet ON wallets(kind) WHERE kind = 'system';
CREATE INDEX wallets_user ON wallets(user_id, id);
INSERT INTO wallets (kind, name) VALUES ('system','Provider Clearing');
CREATE TABLE transactions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_type TEXT NOT NULL CHECK (transaction_type IN ('deposit','transfer','reversal')),
    status TEXT NOT NULL DEFAULT 'posted' CHECK (status = 'posted'),
    reference_id TEXT,
    reversed_transaction_id BIGINT UNIQUE REFERENCES transactions(id),
    actor_user_id BIGINT REFERENCES users(id),
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((transaction_type = 'reversal') = (reversed_transaction_id IS NOT NULL)),
    CHECK (transaction_type <> 'reversal' OR (reason IS NOT NULL AND length(btrim(reason)) > 0))
);
CREATE TABLE ledger_entries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id BIGINT NOT NULL REFERENCES transactions(id),
    wallet_id BIGINT NOT NULL REFERENCES wallets(id),
    entry_type TEXT NOT NULL CHECK (entry_type IN ('debit','credit')),
    amount BIGINT NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(transaction_id, entry_type),
    UNIQUE(transaction_id, wallet_id)
);
CREATE INDEX ledger_wallet_history ON ledger_entries(wallet_id, transaction_id DESC);
CREATE TABLE idempotency_keys (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    key TEXT NOT NULL CHECK (length(key) BETWEEN 1 AND 128),
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('processing','completed')),
    response_code INTEGER,
    response_body BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(user_id, key),
    CHECK (status <> 'completed' OR (response_code IS NOT NULL AND response_body IS NOT NULL))
);
CREATE TABLE webhook_events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider_event_id TEXT NOT NULL UNIQUE,
    payload JSONB NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('processing','completed')),
    response_code INTEGER,
    response_body BYTEA,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE reconciliation_runs (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_user_id BIGINT REFERENCES users(id),
    status TEXT NOT NULL CHECK (status IN ('pass','fail')),
    report JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementBegin
CREATE FUNCTION forbid_ledger_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'posted financial records are immutable' USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER immutable_entries BEFORE UPDATE OR DELETE ON ledger_entries
FOR EACH ROW EXECUTE FUNCTION forbid_ledger_mutation();
CREATE TRIGGER immutable_transactions BEFORE UPDATE OR DELETE ON transactions
FOR EACH ROW EXECUTE FUNCTION forbid_ledger_mutation();
-- +goose StatementBegin
CREATE FUNCTION check_ledger_pair() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_id BIGINT;
BEGIN
    IF TG_TABLE_NAME = 'transactions' THEN target_id := NEW.id;
    ELSE target_id := NEW.transaction_id;
    END IF;
    IF (SELECT count(*) <> 2 OR count(DISTINCT entry_type) <> 2
        OR count(DISTINCT wallet_id) <> 2
        OR sum(CASE WHEN entry_type = 'credit' THEN amount::numeric ELSE -amount::numeric END) <> 0
        FROM ledger_entries WHERE transaction_id = target_id) THEN
        RAISE EXCEPTION 'transaction % must have a balanced pair', target_id USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER balanced_transaction AFTER INSERT ON transactions
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_ledger_pair();
CREATE CONSTRAINT TRIGGER balanced_entry AFTER INSERT ON ledger_entries
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_ledger_pair();
GRANT USAGE ON SCHEMA public TO wallet_app;
GRANT SELECT, INSERT ON users, wallets, transactions, ledger_entries, refresh_tokens,
    idempotency_keys, webhook_events, reconciliation_runs TO wallet_app;
GRANT UPDATE (revoked_at) ON refresh_tokens TO wallet_app;
GRANT UPDATE (id) ON wallets, transactions TO wallet_app;
GRANT UPDATE (status,response_code,response_body) ON idempotency_keys TO wallet_app;
GRANT UPDATE (status,response_code,response_body,processed_at) ON webhook_events TO wallet_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO wallet_app;

-- +goose Down
DROP TABLE reconciliation_runs, webhook_events, idempotency_keys, ledger_entries,
    transactions, wallets, refresh_tokens, users;
DROP FUNCTION check_ledger_pair(), forbid_ledger_mutation();
