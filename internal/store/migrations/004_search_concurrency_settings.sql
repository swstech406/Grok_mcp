ALTER TABLE server_settings
    ADD COLUMN mcp_global_search_concurrency INTEGER NOT NULL DEFAULT 0;

ALTER TABLE server_settings
    ADD COLUMN mcp_user_search_concurrency INTEGER NOT NULL DEFAULT 0;
