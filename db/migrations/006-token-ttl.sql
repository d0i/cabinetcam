-- Add TTL (time-to-live) to API tokens; max 24 hours
ALTER TABLE api_tokens ADD COLUMN expires_at TIMESTAMP;

-- Backfill: existing tokens get a short grace period (1 hour from migration)
-- Go's time.Time format in created_at is not parseable by SQLite datetime(),
-- so we use the current time as the reference point instead.
UPDATE api_tokens SET expires_at = datetime('now', '+1 hour') WHERE expires_at IS NULL;

INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (006, '006-token-ttl');
