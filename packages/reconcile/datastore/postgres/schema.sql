CREATE TABLE factory_reconcile_resources (
    namespace text NOT NULL,
    kind text NOT NULL,
    id text COLLATE "C" NOT NULL,
    abandoned bigint NOT NULL DEFAULT 0 CHECK (abandoned BETWEEN 0 AND 4294967295),
    priority bigint NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 4294967295),
    not_before timestamptz,
    failures bigint NOT NULL DEFAULT 0 CHECK (failures BETWEEN 0 AND 4294967295),
    last_error text NOT NULL DEFAULT '',
    pending boolean NOT NULL DEFAULT true,
    wake_sequence bigint NOT NULL DEFAULT 1 CHECK (wake_sequence > 0),
    fence bigint NOT NULL DEFAULT 0 CHECK (fence >= 0),
    due_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_until timestamptz,
    PRIMARY KEY (namespace, kind, id)
);
CREATE INDEX factory_reconcile_due ON factory_reconcile_resources (namespace, priority DESC, due_at, kind, id)
    WHERE pending;
INSERT INTO factory_reconcile_schema (singleton, version) VALUES (true, 1);
