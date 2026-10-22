-- Archived days are frozen at archived_at unless the owner resumes live counting.
ALTER TABLE days ADD COLUMN archive_live INTEGER NOT NULL DEFAULT 0;
