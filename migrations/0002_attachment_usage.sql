UPDATE users
SET used_bytes = (
    SELECT COALESCE(SUM(size), 0)
    FROM attachments
    WHERE attachments.owner_id = users.id
);

CREATE TRIGGER attachments_decrement_used_bytes
AFTER DELETE ON attachments
BEGIN
    UPDATE users
    SET used_bytes = MAX(0, used_bytes - OLD.size)
    WHERE id = OLD.owner_id;
END;
