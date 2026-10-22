ALTER TABLE days ADD COLUMN is_private INTEGER NOT NULL DEFAULT 0 CHECK (is_private IN (0, 1));
ALTER TABLE sessions ADD COLUMN vault_unlocked INTEGER NOT NULL DEFAULT 0 CHECK (vault_unlocked IN (0, 1));
ALTER TABLE attachments ADD COLUMN is_private INTEGER NOT NULL DEFAULT 0 CHECK (is_private IN (0, 1));

CREATE TRIGGER private_day_revoke_shares AFTER UPDATE OF is_private ON days
WHEN NEW.is_private = 1
BEGIN
    DELETE FROM shares WHERE owner_id = NEW.owner_id AND target_type = 'day' AND target_id = NEW.id;
END;
