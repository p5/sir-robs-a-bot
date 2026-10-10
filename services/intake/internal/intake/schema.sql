CREATE TABLE IF NOT EXISTS intake_connections (
    provider text NOT NULL,
    account text NOT NULL,
    activated_at timestamptz NOT NULL,
    acknowledgement_not_before timestamptz,
    PRIMARY KEY(provider, account)
);

CREATE TABLE IF NOT EXISTS intake_requests (
    id text PRIMARY KEY,
    provider text NOT NULL,
    account text NOT NULL,
    scope text NOT NULL,
    source_kind text NOT NULL,
    source_id text NOT NULL,
    snapshot bytea NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY(provider, account) REFERENCES intake_connections(provider, account),
    UNIQUE(provider, account, scope, source_kind, source_id)
);

CREATE TABLE IF NOT EXISTS intake_outbox (
    request_id text PRIMARY KEY REFERENCES intake_requests(id),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS intake_acknowledgements (
    request_id text PRIMARY KEY REFERENCES intake_requests(id),
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    attempts bigint NOT NULL DEFAULT 0,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','delivered','terminal','uncertain','quarantined')),
    last_error text,
    delivered_at timestamptz
);
CREATE INDEX IF NOT EXISTS intake_acknowledgements_pending
    ON intake_acknowledgements(next_attempt_at, request_id)
    WHERE state = 'pending';
