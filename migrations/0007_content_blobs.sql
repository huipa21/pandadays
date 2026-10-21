-- Attachment bytes are stored once per SHA-256 under media/blobs and shared
-- by every attachment row (of any owner) with the same content. Rows without a
-- hash still use the legacy media/{owner_id}/{uuid} file until the server
-- moves it into the blob store.
ALTER TABLE attachments ADD COLUMN sha256 TEXT NULL
    CHECK (sha256 IS NULL OR (length(sha256) = 64 AND sha256 NOT GLOB '*[^0-9a-f]*'));
CREATE INDEX attachments_owner_sha256 ON attachments(owner_id, sha256);
CREATE INDEX attachments_sha256 ON attachments(sha256);

-- Blobs that may have lost their last reference; the server removes the file
-- if no attachment row still points to it.
CREATE TABLE blob_gc (
    sha256 TEXT PRIMARY KEY
) WITHOUT ROWID;

CREATE TRIGGER attachments_queue_blob_gc
AFTER DELETE ON attachments
WHEN OLD.sha256 IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM attachments WHERE sha256 = OLD.sha256)
BEGIN
    INSERT OR IGNORE INTO blob_gc (sha256) VALUES (OLD.sha256);
END;
