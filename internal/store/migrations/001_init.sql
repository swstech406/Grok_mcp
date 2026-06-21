CREATE TABLE IF NOT EXISTS apikeys (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    key_hash    TEXT NOT NULL UNIQUE,
    key_prefix  TEXT NOT NULL,
    rate_limit  INTEGER NOT NULL DEFAULT 0,
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL,
    last_used_at TEXT,
    total_calls INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_apikeys_key_hash ON apikeys(key_hash);

CREATE TABLE IF NOT EXISTS usage_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key_id      TEXT NOT NULL,
    tool_name   TEXT NOT NULL,
    timestamp   TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    FOREIGN KEY (key_id) REFERENCES apikeys(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_usage_log_key_id ON usage_log(key_id);
CREATE INDEX IF NOT EXISTS idx_usage_log_timestamp ON usage_log(timestamp);