-- SmartRoute Core schema. Every statement is idempotent. Statements are separated by a line holding only --;;
-- The embedding dimension (384) matches all-MiniLM-L6-v2 and bge-small. Changing the model means changing it here.

CREATE EXTENSION IF NOT EXISTS vector;
--;;
CREATE TABLE IF NOT EXISTS cache_entries (
    id                BIGSERIAL PRIMARY KEY,
    namespace         TEXT        NOT NULL,
    exact_key         TEXT        NOT NULL,
    prompt_text       TEXT        NOT NULL DEFAULT '',
    embedding         vector(384),
    model             TEXT        NOT NULL,
    content           TEXT        NOT NULL,
    finish_reason     TEXT        NOT NULL,
    prompt_tokens     INTEGER     NOT NULL DEFAULT 0,
    completion_tokens INTEGER     NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ NOT NULL,
    UNIQUE (namespace, exact_key)
);
--;;
CREATE INDEX IF NOT EXISTS cache_entries_embedding_hnsw
    ON cache_entries USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
--;;
CREATE INDEX IF NOT EXISTS cache_entries_expires_idx ON cache_entries (expires_at);
--;;
CREATE INDEX IF NOT EXISTS cache_entries_namespace_idx ON cache_entries (namespace);
--;;
CREATE TABLE IF NOT EXISTS request_log (
    id                BIGSERIAL PRIMARY KEY,
    ts                TIMESTAMPTZ      NOT NULL DEFAULT now(),
    request_id        TEXT             NOT NULL,
    tenant            TEXT             NOT NULL,
    model             TEXT             NOT NULL,
    outcome           TEXT             NOT NULL,
    similarity        DOUBLE PRECISION NOT NULL DEFAULT 0,
    status            INTEGER          NOT NULL,
    stream            BOOLEAN          NOT NULL,
    prompt_tokens     INTEGER          NOT NULL DEFAULT 0,
    completion_tokens INTEGER          NOT NULL DEFAULT 0,
    est_cost_saved_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_ms          DOUBLE PRECISION NOT NULL,
    stages            JSONB            NOT NULL DEFAULT '{}'::jsonb
);
--;;
CREATE INDEX IF NOT EXISTS request_log_ts_idx ON request_log (ts);
