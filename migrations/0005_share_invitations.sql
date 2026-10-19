ALTER TABLE share_recipients ADD COLUMN status TEXT NOT NULL DEFAULT 'accepted'
    CHECK (status IN ('pending', 'accepted', 'rejected'));
CREATE INDEX share_recipients_status ON share_recipients(recipient_id, status, share_id);
