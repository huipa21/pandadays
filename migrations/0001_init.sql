-- Initial schema (SPEC §7). Timestamps are Unix seconds (UTC).

CREATE TABLE users (
    id                   INTEGER PRIMARY KEY,
    username             TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    email                TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash        TEXT    NOT NULL,
    is_admin             INTEGER NOT NULL DEFAULT 0 CHECK (is_admin IN (0, 1)),
    is_active            INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
    must_change_password INTEGER NOT NULL DEFAULT 0 CHECK (must_change_password IN (0, 1)),
    lang                 TEXT    NULL,
    timezone             TEXT    NULL,
    quota_days           INTEGER NULL,
    quota_bytes          INTEGER NULL,
    max_card_bytes       INTEGER NULL,
    used_bytes           INTEGER NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    created_at           INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at           INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE sessions (
    token_hash   TEXT    PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    persistent   INTEGER NOT NULL DEFAULT 0 CHECK (persistent IN (0, 1)),
    expires_at   INTEGER NOT NULL,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    last_seen_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX sessions_user_id ON sessions(user_id);
CREATE INDEX sessions_expires_at ON sessions(expires_at);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE collections (
    id          INTEGER PRIMARY KEY,
    owner_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL COLLATE NOCASE,
    color       TEXT    NULL,
    sort        INTEGER NOT NULL DEFAULT 0,
    is_reserved INTEGER NOT NULL DEFAULT 0 CHECK (is_reserved IN (0, 1)),
    created_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (owner_id, name)
);
CREATE UNIQUE INDEX collections_one_reserved ON collections(owner_id) WHERE is_reserved = 1;

CREATE TABLE tags (
    id          INTEGER PRIMARY KEY,
    owner_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL COLLATE NOCASE,
    color       TEXT    NULL,
    is_reserved INTEGER NOT NULL DEFAULT 0 CHECK (is_reserved IN (0, 1)),
    created_at  INTEGER NOT NULL DEFAULT (unixepoch()),
    UNIQUE (owner_id, name)
);
CREATE UNIQUE INDEX tags_one_reserved ON tags(owner_id) WHERE is_reserved = 1;

CREATE TABLE days (
    id                     INTEGER PRIMARY KEY,
    owner_id               INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    collection_id          INTEGER NOT NULL REFERENCES collections(id) ON DELETE RESTRICT,
    title                  TEXT    NOT NULL,
    date                   TEXT    NOT NULL,
    time                   TEXT    NULL,
    repeat                 TEXT    NOT NULL DEFAULT 'none'
                           CHECK (repeat IN ('none', 'weekly', 'monthly', 'yearly')),
    comment                TEXT    NULL,
    style_color            TEXT    NULL,
    style_icon             TEXT    NULL,
    style_bg_attachment_id INTEGER NULL REFERENCES attachments(id) ON DELETE SET NULL,
    archived_at            INTEGER NULL,
    created_at             INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at             INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX days_owner_archived_date ON days(owner_id, archived_at, date);
CREATE INDEX days_collection_id ON days(collection_id);
CREATE INDEX days_style_bg_attachment_id ON days(style_bg_attachment_id);

CREATE TABLE day_tags (
    day_id INTEGER NOT NULL REFERENCES days(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (day_id, tag_id)
) WITHOUT ROWID;
CREATE INDEX day_tags_tag_id ON day_tags(tag_id);

CREATE TABLE attachments (
    id         INTEGER PRIMARY KEY,
    day_id     INTEGER NOT NULL REFERENCES days(id) ON DELETE CASCADE,
    owner_id   INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    uuid       TEXT    NOT NULL UNIQUE,
    kind       TEXT    NOT NULL CHECK (kind IN ('image', 'audio', 'video', 'file')),
    filename   TEXT    NOT NULL,
    mime       TEXT    NOT NULL,
    size       INTEGER NOT NULL CHECK (size >= 0),
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX attachments_day_id ON attachments(day_id);
CREATE INDEX attachments_owner_id ON attachments(owner_id);

CREATE TABLE shares (
    id           INTEGER PRIMARY KEY,
    owner_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_type  TEXT    NOT NULL CHECK (target_type IN ('day', 'collection')),
    target_id    INTEGER NOT NULL,
    show_comment INTEGER NOT NULL DEFAULT 0 CHECK (show_comment IN (0, 1)),
    created_at   INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX shares_owner_id ON shares(owner_id);
CREATE INDEX shares_target ON shares(target_type, target_id);

CREATE TABLE share_recipients (
    share_id     INTEGER NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    recipient_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (share_id, recipient_id)
) WITHOUT ROWID;
CREATE INDEX share_recipients_recipient_id ON share_recipients(recipient_id);

CREATE TABLE share_hidden (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day_id  INTEGER NOT NULL REFERENCES days(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, day_id)
) WITHOUT ROWID;
CREATE INDEX share_hidden_day_id ON share_hidden(day_id);

-- Site defaults (SPEC §3.8, §6.3). site_lang is absent until an admin sets it
-- (falls back to PANDA_LANG); setup_token_* rows are written by the setup flow.
INSERT INTO settings (key, value) VALUES
    ('session_days',           '30'),
    ('default_quota_days',     '1000'),
    ('default_quota_bytes',    '1073741824'),
    ('default_max_card_bytes', '104857600'),
    ('backup_retention',       '5');
