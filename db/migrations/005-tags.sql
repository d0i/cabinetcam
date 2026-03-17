-- Add tags column to boxes (comma-separated tag strings)
ALTER TABLE boxes ADD COLUMN tags TEXT NOT NULL DEFAULT '';

INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (005, '005-tags');
