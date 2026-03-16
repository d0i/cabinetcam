-- Base schema
--
-- Migrations tracking table
CREATE TABLE IF NOT EXISTS migrations (
    migration_number INTEGER PRIMARY KEY,
    migration_name TEXT NOT NULL,
    executed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Boxes table
CREATE TABLE IF NOT EXISTS boxes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    memo TEXT NOT NULL DEFAULT '',
    max_photos INTEGER NOT NULL DEFAULT 20,
    protect_recent INTEGER NOT NULL DEFAULT 3,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Photos table
CREATE TABLE IF NOT EXISTS photos (
    id TEXT PRIMARY KEY,
    box_id TEXT NOT NULL REFERENCES boxes(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    captured_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_photos_box_id ON photos(box_id);
CREATE INDEX IF NOT EXISTS idx_photos_captured_at ON photos(captured_at);

-- Record execution of this migration
INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (001, '001-base');
