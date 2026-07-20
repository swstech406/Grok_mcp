ALTER TABLE server_settings
ADD COLUMN revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0);
