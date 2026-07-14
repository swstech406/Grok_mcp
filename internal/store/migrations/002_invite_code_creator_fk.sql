-- Rebuild invite_codes because SQLite cannot add a foreign key to an existing
-- table. Legacy empty or orphaned creator IDs are normalized to NULL.

CREATE TABLE invite_codes_with_creator_fk (
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

INSERT INTO invite_codes_with_creator_fk (
    id,
    code,
    code_hash,
    code_prefix,
    registration_limit,
    registration_count,
    enabled,
    created_by_user_id,
    created_at,
    updated_at
)
SELECT
    invite_codes.id,
    invite_codes.code,
    invite_codes.code_hash,
    invite_codes.code_prefix,
    invite_codes.registration_limit,
    invite_codes.registration_count,
    invite_codes.enabled,
    CASE
        WHEN EXISTS (
            SELECT 1
            FROM users
            WHERE users.id = invite_codes.created_by_user_id
        ) THEN invite_codes.created_by_user_id
        ELSE NULL
    END,
    invite_codes.created_at,
    invite_codes.updated_at
FROM invite_codes;

DROP TABLE invite_codes;
ALTER TABLE invite_codes_with_creator_fk RENAME TO invite_codes;

CREATE INDEX idx_invite_codes_created_at ON invite_codes(created_at);
CREATE INDEX idx_invite_codes_created_by_user_id ON invite_codes(created_by_user_id);
