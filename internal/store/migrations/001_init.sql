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
    tier_id        TEXT NOT NULL REFERENCES tiers(id) ON DELETE RESTRICT,
    success_calls  INTEGER NOT NULL DEFAULT 0,
    success_period TEXT NOT NULL DEFAULT '1970-01',
    token_version  INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);
CREATE INDEX IF NOT EXISTS idx_users_tier_id ON users(tier_id);

CREATE TABLE IF NOT EXISTS apikeys (
    id                     TEXT PRIMARY KEY,
    user_id                TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                   TEXT NOT NULL,
    key_hash               TEXT NOT NULL UNIQUE,
    key_prefix             TEXT NOT NULL,
    key_ciphertext         TEXT NOT NULL CHECK (key_ciphertext <> ''),
    key_nonce              TEXT NOT NULL CHECK (key_nonce <> ''),
    key_encryption_version INTEGER NOT NULL CHECK (key_encryption_version > 0),
    enabled                INTEGER NOT NULL DEFAULT 1,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL,
    last_used_at           TEXT,
    total_calls            INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_apikeys_key_hash ON apikeys(key_hash);
CREATE INDEX IF NOT EXISTS idx_apikeys_user_id ON apikeys(user_id);

CREATE TABLE IF NOT EXISTS usage_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key_id      TEXT NOT NULL,
    tool_name   TEXT NOT NULL,
    timestamp   TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    success     INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (key_id) REFERENCES apikeys(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_usage_log_key_id_timestamp ON usage_log(key_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_usage_log_timestamp ON usage_log(timestamp);

CREATE TABLE IF NOT EXISTS server_settings (
    id                             TEXT PRIMARY KEY,
    cpa_base_url                   TEXT NOT NULL,
    cpa_api_key_ciphertext         TEXT NOT NULL CHECK (cpa_api_key_ciphertext <> ''),
    cpa_api_key_nonce              TEXT NOT NULL CHECK (cpa_api_key_nonce <> ''),
    cpa_api_key_encryption_version INTEGER NOT NULL CHECK (cpa_api_key_encryption_version > 0),
    upstream_protocol              TEXT NOT NULL CHECK (upstream_protocol IN ('responses', 'chat_completions', 'anthropic_messages')),
    model                          TEXT NOT NULL,
    timeout_seconds                INTEGER NOT NULL CHECK (timeout_seconds > 0),
    proxy_url                      TEXT NOT NULL DEFAULT '',
    proxy_enabled                  INTEGER NOT NULL DEFAULT 0 CHECK (proxy_enabled IN (0, 1)),
    registration_mode              TEXT NOT NULL CHECK (registration_mode IN ('free', 'invite', 'disabled')),
    debug                          INTEGER NOT NULL DEFAULT 0 CHECK (debug IN (0, 1)),
    created_at                     TEXT NOT NULL,
    updated_at                     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS invite_codes (
    id                 TEXT PRIMARY KEY,
    -- Legacy compatibility column. New code stores only an empty value here;
    -- code_hash is the authoritative redemption credential.
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
