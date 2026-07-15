-- Match the panel's stable keyset ordering so later pages do not scan skipped rows.

DROP INDEX IF EXISTS idx_usage_log_timestamp;
CREATE INDEX idx_usage_log_timestamp
    ON usage_log(timestamp DESC, id DESC);

DROP INDEX IF EXISTS idx_usage_log_key_id_timestamp;
CREATE INDEX idx_usage_log_key_id_timestamp
    ON usage_log(key_id, timestamp DESC, id DESC);

DROP INDEX IF EXISTS idx_apikeys_user_id;
CREATE INDEX idx_apikeys_user_id
    ON apikeys(user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_users_created_id
    ON users(created_at ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_tiers_level_name_id
    ON tiers(level ASC, name ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_invite_codes_created_id
    ON invite_codes(created_at DESC, id DESC);
