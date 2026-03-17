-- API tokens for external client authentication
CREATE TABLE IF NOT EXISTS api_tokens (
    token TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at TIMESTAMP
);

INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (004, '004-api-tokens');
