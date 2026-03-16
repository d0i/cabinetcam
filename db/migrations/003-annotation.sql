-- Add annotation columns to boxes
ALTER TABLE boxes ADD COLUMN annotation TEXT NOT NULL DEFAULT '';
ALTER TABLE boxes ADD COLUMN annotation_photo_id TEXT NOT NULL DEFAULT '';
ALTER TABLE boxes ADD COLUMN annotation_at TIMESTAMP;

INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (003, '003-annotation');
