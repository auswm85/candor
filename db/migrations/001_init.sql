CREATE TABLE providers (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE models (
    id          INTEGER PRIMARY KEY,
    provider_id INTEGER NOT NULL REFERENCES providers(id),
    name        TEXT NOT NULL,
    UNIQUE(provider_id, name)
);

CREATE TABLE usage_records (
    id                   INTEGER PRIMARY KEY,
    provider_id          INTEGER NOT NULL REFERENCES providers(id),
    model_id             INTEGER NOT NULL REFERENCES models(id),
    bucket_start         TEXT NOT NULL,
    bucket_end           TEXT NOT NULL,
    input_tokens         INTEGER NOT NULL,
    cached_input_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens        INTEGER NOT NULL,
    cost_usd             REAL NOT NULL,
    fetched_at           TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(provider_id, model_id, bucket_start)
);

CREATE INDEX idx_usage_time ON usage_records(bucket_start);
CREATE INDEX idx_usage_model ON usage_records(model_id, bucket_start);

CREATE TABLE config_state (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO providers (name) VALUES ('openai'), ('anthropic'), ('openrouter');