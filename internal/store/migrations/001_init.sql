-- Current complete schema for new SQLite databases.
-- Historical migrations were squashed into this baseline.

CREATE TABLE IF NOT EXISTS tiers (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE COLLATE NOCASE,
    level         INTEGER NOT NULL,
    rpm           INTEGER NOT NULL DEFAULT 0,
    success_limit INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

INSERT INTO tiers (id, name, level, rpm, success_limit, created_at, updated_at) VALUES
('00000000-0000-4000-8000-tier0000000', 'tier0', 0,   10, 800,    datetime('now'), datetime('now')),
('00000000-0000-4000-8000-tier0000001', 'tier1', 1,   20, 4000,   datetime('now'), datetime('now')),
('00000000-0000-4000-8000-tier0000002', 'tier2', 2,   40, 16000,  datetime('now'), datetime('now')),
('00000000-0000-4000-8000-tier0000003', 'tier3', 3,   60, 40000,  datetime('now'), datetime('now')),
('00000000-0000-4000-8000-tier0000004', 'tier4', 4,  120, 160000, datetime('now'), datetime('now')),
('00000000-0000-4000-8000-tier0000005', 'tier5', 5,  300, 800000, datetime('now'), datetime('now')),
('00000000-0000-4000-8000-tier0000006', 'tier6', 6,    0, 0,      datetime('now'), datetime('now'));

CREATE TABLE IF NOT EXISTS users (
    id             TEXT PRIMARY KEY,
    username       TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash  TEXT NOT NULL,
    role           TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('admin', 'user')),
    enabled        INTEGER NOT NULL DEFAULT 1,
    tier_id        TEXT REFERENCES tiers(id) ON DELETE SET NULL,
    success_calls  INTEGER NOT NULL DEFAULT 0,
    success_period TEXT NOT NULL DEFAULT '1970-01',
    token_version  INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

CREATE TABLE IF NOT EXISTS apikeys (
    id                     TEXT PRIMARY KEY,
    user_id                TEXT REFERENCES users(id) ON DELETE CASCADE,
    name                   TEXT NOT NULL,
    key_hash               TEXT NOT NULL UNIQUE,
    key_prefix             TEXT NOT NULL,
    key_ciphertext         TEXT NOT NULL DEFAULT '',
    key_nonce              TEXT NOT NULL DEFAULT '',
    key_encryption_version INTEGER NOT NULL DEFAULT 0,
    enabled                INTEGER NOT NULL DEFAULT 1,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    last_used_at           TEXT,
    total_calls            INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_apikeys_key_hash ON apikeys(key_hash);

CREATE TABLE IF NOT EXISTS usage_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key_id      TEXT NOT NULL,
    tool_name   TEXT NOT NULL,
    timestamp   TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    success     INTEGER NOT NULL DEFAULT 1,
    debug_json  TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (key_id) REFERENCES apikeys(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_usage_log_key_id ON usage_log(key_id);
CREATE INDEX IF NOT EXISTS idx_usage_log_timestamp ON usage_log(timestamp);

CREATE TABLE IF NOT EXISTS usage_log_debug_body_chunks (
    usage_id    INTEGER NOT NULL,
    body_kind   TEXT NOT NULL CHECK (body_kind IN ('request', 'response')),
    chunk_index INTEGER NOT NULL,
    body_data   BLOB NOT NULL,
    PRIMARY KEY (usage_id, body_kind, chunk_index),
    FOREIGN KEY (usage_id) REFERENCES usage_log(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_usage_debug_body_chunks_usage_id
    ON usage_log_debug_body_chunks(usage_id);

CREATE TABLE IF NOT EXISTS server_settings (
    id                             TEXT PRIMARY KEY,
    cpa_base_url                   TEXT NOT NULL,
    cpa_api_key                    TEXT NOT NULL,
    cpa_api_key_ciphertext         TEXT NOT NULL DEFAULT '',
    cpa_api_key_nonce              TEXT NOT NULL DEFAULT '',
    cpa_api_key_encryption_version INTEGER NOT NULL DEFAULT 0,
    upstream_protocol              TEXT NOT NULL DEFAULT 'responses',
    model                          TEXT NOT NULL,
    timeout_seconds                INTEGER NOT NULL,
    proxy_url                      TEXT NOT NULL DEFAULT '',
    proxy_enabled                  INTEGER NOT NULL DEFAULT 0,
    registration_mode              TEXT NOT NULL DEFAULT 'free',
    debug                          INTEGER NOT NULL DEFAULT 0,
    created_at                     TEXT NOT NULL,
    updated_at                     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS invite_codes (
    id                 TEXT PRIMARY KEY,
    code               TEXT NOT NULL DEFAULT '',
    code_hash          TEXT NOT NULL UNIQUE,
    code_prefix        TEXT NOT NULL,
    registration_limit INTEGER NOT NULL,
    registration_count INTEGER NOT NULL DEFAULT 0,
    enabled            INTEGER NOT NULL DEFAULT 1,
    created_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    CHECK (registration_limit > 0),
    CHECK (registration_count >= 0),
    CHECK (registration_count <= registration_limit)
);

CREATE INDEX IF NOT EXISTS idx_invite_codes_created_at ON invite_codes(created_at);
CREATE INDEX IF NOT EXISTS idx_invite_codes_created_by_user_id ON invite_codes(created_by_user_id);
