-- Invite redemption is hash-based. Keep the legacy code column for schema
-- compatibility, but irreversibly remove all retained plaintext material.

UPDATE invite_codes
SET code = ''
WHERE code <> '';
