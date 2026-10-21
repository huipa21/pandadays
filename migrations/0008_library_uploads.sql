-- rebuild-tables
-- Day attachment rows reference stored content. A NULL day is a library upload.
CREATE TABLE attachments_new (
    id INTEGER PRIMARY KEY,
    day_id INTEGER NULL REFERENCES days(id) ON DELETE CASCADE,
    owner_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    uuid TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL CHECK (kind IN ('image', 'audio', 'video', 'file')),
    filename TEXT NOT NULL,
    mime TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    detached INTEGER NOT NULL DEFAULT 0 CHECK (detached IN (0, 1)),
    sha256 TEXT NULL CHECK (sha256 IS NULL OR (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'))
);
INSERT INTO attachments_new SELECT id, day_id, owner_id, uuid, kind, filename, mime, size, created_at, detached, sha256 FROM attachments;
DROP TABLE attachments;
ALTER TABLE attachments_new RENAME TO attachments;
CREATE INDEX attachments_day_id ON attachments(day_id);
CREATE INDEX attachments_owner_id ON attachments(owner_id);
CREATE INDEX attachments_owner_sha256 ON attachments(owner_id, sha256);
CREATE INDEX attachments_sha256 ON attachments(sha256);
CREATE TRIGGER attachments_decrement_used_bytes AFTER DELETE ON attachments
BEGIN
    UPDATE users SET used_bytes = MAX(0, used_bytes - OLD.size) WHERE id = OLD.owner_id;
END;
CREATE TRIGGER attachments_queue_blob_gc AFTER DELETE ON attachments
WHEN OLD.sha256 IS NOT NULL AND NOT EXISTS (SELECT 1 FROM attachments WHERE sha256 = OLD.sha256)
BEGIN
    INSERT OR IGNORE INTO blob_gc (sha256) VALUES (OLD.sha256);
END;
