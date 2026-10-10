CREATE TABLE IF NOT EXISTS prototype_documents (
    namespace text NOT NULL,
    id text NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    body text NOT NULL,
    observed_revision bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (namespace, id)
);
CREATE TABLE IF NOT EXISTS prototype_outbox (
    namespace text NOT NULL,
    id text NOT NULL,
    revision bigint NOT NULL,
    priority bigint NOT NULL,
    PRIMARY KEY (namespace, id, revision)
);
CREATE TABLE IF NOT EXISTS prototype_publications (
    namespace text NOT NULL,
    id text NOT NULL,
    revision bigint NOT NULL,
    body text NOT NULL,
    ready_at timestamptz NOT NULL,
    PRIMARY KEY (namespace, id, revision)
);
