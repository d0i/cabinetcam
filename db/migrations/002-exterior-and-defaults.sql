-- Add exterior photo to boxes
ALTER TABLE boxes ADD COLUMN exterior_filename TEXT NOT NULL DEFAULT '';

-- Change box max_photos/protect_recent defaults to 0 meaning "use app default"
-- 0 = inherit from app_settings

-- App-wide settings (single-row table)
CREATE TABLE IF NOT EXISTS app_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    default_max_photos INTEGER NOT NULL DEFAULT 32,
    default_protect_recent INTEGER NOT NULL DEFAULT 3
);

INSERT OR IGNORE INTO app_settings (id) VALUES (1);

-- Migrate existing boxes: set to 0 (inherit) if they had the old defaults
UPDATE boxes SET max_photos = 0 WHERE max_photos = 20;
UPDATE boxes SET protect_recent = 0 WHERE protect_recent = 3;

INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (002, '002-exterior-and-defaults');
