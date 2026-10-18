# PandaDays — Product & Technical Specification

PandaDays tracks meaningful days: a future target shows how much time is left; a past day shows how much time has passed. Mobile-first, works on laptop. Multi-user, self-hosted via Docker Compose.

---

## 1. Tech Stack

| Concern | Choice |
|---|---|
| Language | Go 1.23+, single binary named `panda` (web server + CLI) |
| HTTP | stdlib `net/http` (Go 1.22+ routing patterns), `html/template` |
| DB | SQLite via `modernc.org/sqlite` (pure Go, no CGO), WAL mode, `foreign_keys=ON`, `busy_timeout=5000` |
| Migrations | Numbered `.sql` files embedded with `embed`, applied at startup, tracked in `schema_migrations` |
| Frontend | Server-rendered HTML + htmx (vendored, embedded) + hand-written mobile-first CSS; minimal vanilla JS (voice recording, timezone detection, toggles) |
| Auth | bcrypt password hashes; server-side sessions in SQLite; cookie `HttpOnly`, `Secure` (configurable for local dev), `SameSite=Lax`; CSRF token on all state-changing requests |
| i18n | `locales/en.json`, `locales/zh.json` embedded; template func `t "key"` |
| Storage | `/data/pandadays.db`, `/data/media/{user_id}/{uuid}`, `/data/backups/`, `/data/tmp/` |
| Deploy | Multi-stage Dockerfile (distroless/alpine runtime), `compose.yaml`, single volume mounted at `/data` |

Dependencies are kept minimal: SQLite driver, `golang.org/x/crypto/bcrypt`. Everything else is stdlib.

### Environment variables

| Var | Default | Meaning |
|---|---|---|
| `PANDA_ADDR` | `:8080` | Listen address |
| `PANDA_DATA_DIR` | `/data` | Data root |
| `PANDA_BASE_URL` | — | Public URL (used for cookie/redirect correctness) |
| `PANDA_LANG` | `en` | Site default language (`en` or `zh`) |
| `PANDA_COOKIE_SECURE` | `true` | Set `false` for plain-HTTP local dev |
| `TZ` | `UTC` | Server timezone (fallback only) |

---

## 2. Roles

- **Admin**: manages users, quotas, site settings, backup/restore. Also a normal user with own space.
- **User**: manages own days, collections, tags, attachments, shares; views days shared with them.
- **No self-registration.** Only admin creates users.

---

## 3. Domain Concepts

### 3.1 Day
A meaningful date owned by one user.

| Field | Rule |
|---|---|
| title | required, 1–200 chars |
| date | required (`YYYY-MM-DD`) |
| time | optional (`HH:MM`); if absent the day is all-day |
| repeat | `none` (default), `weekly`, `monthly`, `yearly` |
| collection | exactly one; defaults to the user's reserved **Default** collection |
| tags | zero or more; if none given, the reserved **Default** tag is applied |
| comment | optional, free text (max 10,000 chars); not shown in list view, shown on card view |
| style | optional accent color, icon/emoji, background image (from the day's own image attachments) |
| archived_at | null = active; set = archived |
| attachments | images, audio (incl. in-browser voice recording), video, generic files |

### 3.2 Collection
A "book" of days. Each day belongs to exactly one collection. Each user has a reserved **Default** collection (cannot be renamed or deleted). Deleting a non-default collection moves all its days to Default (single transaction).

### 3.3 Tag
Describes the nature of a day; many-to-many. Each user has a reserved **Default** tag (cannot be renamed or deleted). Deleting a tag removes it from days; any day left with zero tags gets Default.

Reserved names are displayed localized ("Default" / "默认") but stored with `is_reserved=1`; users cannot create another collection/tag literally named the reserved name.

### 3.4 Countdown / elapsed calculation
- All calculations happen in the **user's timezone**.
- Effective target:
  - `repeat = none`: the day's own date(+time).
  - repeating: the **next occurrence** at or after "now" (today counts as today, not past). Repeating days are always treated as **future**.
- Monthly repeat on day 29–31: clamp to the last day of shorter months.
- Yearly repeat on Feb 29: Feb 28 in non-leap years.
- Display:
  - List: `N days left` / `N days ago` / `Today`. With time: also hours/minutes when < 1 day.
  - Card: large number + breakdown in years/months/days (+h/m if time set). For repeating days, also show occurrence ordinal (e.g. "3rd anniversary") and time since the original date.
- Classification: **future** (target > now or repeating), **today**, **past** (target < now, non-repeating).
- Date-engine conventions:
  - All-day comparisons and day counts use local civil dates, not elapsed 24-hour periods. An all-day repeat remains on today until local midnight. A timed repeat advances as soon as its scheduled instant has passed; exact equality still selects that occurrence.
  - Repeat schedules never select an occurrence before the original date. Monthly/yearly clamping always uses the original day, so a shorter month does not permanently shift later occurrences.
  - A nonexistent local clock time shifts forward by the DST gap; an ambiguous clock time uses its first occurrence. Later repeats return to the originally requested clock time.
  - Card breakdowns count whole clamped calendar years, then months, then local calendar days; remaining hours/minutes use elapsed time, truncated to whole minutes. All-day breakdowns omit hours/minutes. Timed list labels use elapsed hours/minutes below 24 hours and civil day counts otherwise.
  - The original occurrence has ordinal 0; the first repeat/anniversary has ordinal 1. Time since the original is absent until the original arrives. Repeating days classify as future even when their list label is Today.

### 3.5 Visual states (defaults; user style overrides color/icon/bg)
| State | Default look | Always-on marker |
|---|---|---|
| Future | warm accent, ⏳ icon | — |
| Past | cool accent, ✓ icon | — |
| Archived | greyed/desaturated, 📦 icon | "Archived" badge |
| Shared (received) | dashed border, owner avatar | "from @owner" badge |

Badges are never removed by custom styles so states remain distinguishable without relying on color.

### 3.6 Archive
- Archiving sets `archived_at`; unarchiving clears it.
- Archived days are hidden from Home.
- Archived days are visible in: Archive page, Collection detail, Tag detail (with a filter to hide them).

### 3.7 Sharing
- Targets: a single **day** or a whole **collection**.
- Recipients: one or more users, identified by **username or email** (case-insensitive). Unknown identifiers return a generic error (no user enumeration). Cannot share to self.
- One share has many recipients (`share_recipients` table).
- Per-share `show_comment` toggle (default off). Recipients see title, date, time, repeat, style, attachments; they see the comment only if `show_comment=1`. Owner's collection and tags are never exposed.
- Collection share is live: recipients see all current non-archived days of that collection.
- Shared items are **read-only** for recipients.
- Recipients may hide a shared day from their Home (`share_hidden`); still listed under "Shared with me".
- Owner can edit recipients, toggle comment visibility, or revoke shares.
- If the same day is reachable through multiple shares, it appears once; comment visible if any applicable share has `show_comment=1`.

### 3.8 Quotas & limits (admin-adjustable)
| Setting | Default | Scope |
|---|---|---|
| `quota_days` | 1000 | max days per user (archived count) |
| `quota_bytes` | 1 GiB | total attachment storage per user |
| `max_card_bytes` | 100 MiB | total attachment size per day |
| `session_days` | 30 | "Keep me signed in" duration |

Site-wide defaults live in `settings`; each user row may override (`NULL` = use site default). Enforcement on create day, upload, and import. Server uses `http.MaxBytesReader` sized to remaining card allowance.

Admins manage accounts and usage at `/admin/users` and defaults at `/admin/settings`.
Blank user quota fields restore inheritance; zero is an explicit limit that blocks new
days or uploads. Lowering a limit preserves existing data. Quotas and backup retention
must be nonnegative integers; session duration must be 1–106751 days. Site language
may be cleared to return to the environment/browser fallback.

User creation and password reset show a random temporary password only in the
non-cacheable POST response; it is never stored in plaintext. Resets and deactivation
revoke all of the affected user's sessions. Deletion requires explicit confirmation,
removes the account and owned data, and stages its media directory until the database
transaction commits (restoring files on rollback). An admin cannot deactivate/delete
themselves or the last active admin; these checks run inside the write transaction.

---

## 4. Authentication & Accounts

### 4.1 Initial setup
- On startup, if no admin exists and no valid setup token exists: generate a random 32-byte token, store only its SHA-256 hash with `expires_at = now + 1h`, print the token to stdout (container logs) once.
- `/setup` accepts the token + admin username, email, password. Token is **one-time**: deleted on success. Expired → rejected; restart the container to issue a new one.
- After an admin exists, `/setup` returns 404.

### 4.2 Login
- Username or email + password.
- "Keep me signed in" checkbox: session lifetime = `session_days` (admin setting); cookie persistent. Unchecked: session cookie (no Max-Age), server-side expiry 12h.
- Rate-limit failed logins per IP+account (e.g. exponential backoff, in-memory).
- Inactive users cannot log in.

### 4.3 Passwords
- Length 8–32 characters (Unicode allowed; counted in runes, bcrypt input ≤ 72 bytes enforced).
- Admin-created users and all resets get a **random 16-char password**, shown once, with `must_change_password=1`.
- While `must_change_password=1`, every route except change-password and logout redirects to change-password.
- Changing password invalidates all other sessions of that user.

### 4.4 CLI — `panda`
```
docker compose exec pandadays panda reset-admin-password --username admin
```
- Generates a random password, prints it, sets `must_change_password=1`, deletes that user's sessions. Fails if user is not admin.

Other subcommands:
- `panda serve` (default container command)
- `panda backup --out <file>`
- `panda restore --file <file>` (requires server stopped or uses the same maintenance procedure)
- `panda version`

### 4.5 Timezone & language
- Timezone: user setting. If empty, the browser's `Intl.DateTimeFormat().resolvedOptions().timeZone` is posted on first page load after login and saved. Validated with `time.LoadLocation` (embed `time/tzdata`).
- Language: `en` or `zh`. Resolution order: user setting → `PANDA_LANG`/site setting → `Accept-Language` → `en`.

---

## 5. Attachments

- Kinds: `image`, `audio`, `video`, `file`. Kind is derived from sniffed content type (`http.DetectContentType` on first 512 bytes), not the client header.
- Voice: in-browser recording via `MediaRecorder`, uploaded as audio.
- Stored at `/data/media/{user_id}/{uuid}` (no original filename on disk). Original filename kept in DB for download.
- Served only via `/attachments/{id}` with authorization (owner or valid share recipient). Headers: `X-Content-Type-Options: nosniff`; images/audio/video inline with sniffed type; everything else (and any HTML/SVG/XML) as `Content-Disposition: attachment` with `application/octet-stream`. Support HTTP range requests (`http.ServeContent`) for media playback.
- Limits: per-card total ≤ `max_card_bytes`; per-user total ≤ `quota_bytes`. `users.used_bytes` maintained transactionally.
- Deleting an attachment/day deletes files and decrements `used_bytes`. If the deleted attachment was the card background, style falls back to default.

---

## 6. Import / Export / Backup

### 6.1 User export (Settings)
Zip download:
```
pandadays-export-<username>-<date>.zip
  days.csv
  media/<attachment_uuid>_<sanitized_filename>
```
`days.csv` columns (UTF-8 with BOM for Excel, RFC 4180):
```
title,date,time,repeat,collection,tags,comment,archived,style_color,style_icon,style_bg,attachments
```
- `tags` and `attachments` are `|`-separated; attachments refer to paths inside `media/`; `style_bg` refers to one of them.
- Cells beginning with `= + - @ \t \r` are prefixed with `'` (CSV injection guard); importer strips that prefix.
- Only the user's own days (not shared-with-me).

### 6.2 User import
- Accepts the same zip (or a bare `days.csv` without attachments).
- Mode: **merge**; rows matching an existing day by `(title, date, collection)` are skipped.
- Missing collections/tags are created.
- Two-step: upload → preview (new / skipped / error counts, quota check) → confirm.
- Single transaction; files written to temp then moved; on failure everything rolls back.
- Guards: zip-slip (reject `..`, absolute paths, symlinks), max uncompressed size, max entries, per-card and per-user limits.

### 6.3 Admin site backup / restore (Admin → Backup, Web UI)
**Backup**
- Produces `pandadays-backup-<timestamp>.tar.gz` containing:
  - `pandadays.db` (consistent snapshot via `VACUUM INTO`)
  - `media/…`
  - `manifest.json` (app version, schema version, created_at, file list with SHA-256)
- Streamed as a download; optionally retained in `/data/backups` (retention count setting, default 5). Admin can list/download/delete retained backups.

**Restore**
1. Admin uploads archive (streamed to `/data/tmp`, no size cap at app level) and re-enters password.
2. Validate: manifest present, checksums match, schema version ≤ current, no unsafe paths/links, free disk space sufficient.
3. Enter maintenance mode: new requests get localized 503; drain in-flight requests; close DB.
4. Auto-create pre-restore snapshot `/data/backups/pre-restore-<ts>-<unique>.tar.gz`.
5. Atomic swap via `rename` on the same volume (`pandadays.db`, `media/`); old copies kept as `*.old` until success.
6. Reopen DB, run migrations, delete all sessions, exit maintenance.
7. Admin redirected to login (credentials from restored data).
8. On failure: rename old files back, keep snapshot, reopen the original database, exit maintenance, show error. If rollback or reopening fails, remain in maintenance and log recovery details rather than serve potentially inconsistent data.

The admin navigation links to `/admin/backup`. Creation is a CSRF-protected
POST, with an optional retained server copy. The completed archive is staged
on the data volume before streaming the download so creation errors can be
reported without a partial archive. Allow disk space for the database snapshot
and compressed archive. Maintenance also pauses requests during creation and
download, keeping database and media consistent.

Retained ordinary backups are pruned newest-first to `backup_retention` after
creation and when site settings are saved. Zero disables ordinary retention.
Pre-restore recovery snapshots are listed and downloadable but excluded from
automatic pruning; delete them explicitly using the confirmation checkbox.
Retained archive names are validated and only regular files are served.

Restore uploads stream to `<data>/tmp` without a payload byte limit; small text
fields are bounded. Both form-field and header CSRF tokens are supported.
Password re-entry uses the existing authentication backoff. Archive validation
finishes before maintenance begins. All requests, including health checks and
static files, receive a non-cacheable localized 503 with `Retry-After` during
maintenance; those responses never access the database. Once admitted requests
have drained, the engine takes its recovery snapshot and performs the swap.
The server replaces all database-backed services and routes, clears the session
cookie, and redirects to login. A disconnected browser does not cancel a swap
that has started. Do not stop the server during restore.

CLI `panda backup` / `panda restore` share the same `internal/backup` package.

#### Offline CLI (T18)

Stop the server before running either command so the database snapshot and media
remain consistent, and so no open handles interfere with restore. Set
`PANDA_DATA_DIR` to the site's data directory, then run:

```
panda backup --out pandadays-backup.tar.gz
panda restore --file pandadays-backup.tar.gz
```

Backup refuses to overwrite an existing output file or write into live media.
It includes all users, settings, sharing data, and media; treat archives as
sensitive, since they contain password hashes and session records.
Restore fully stages the archive under `<data>/tmp` on the same volume before
changing live data. The manifest records `app_version`, `schema_version`,
`created_at` (UTC RFC 3339), and `files` (`path`, `size`, `sha256`).
Validation requires an exact file list, SHA-256 checksums, a supported schema,
SQLite integrity and foreign-key checks, regular files only, and safe paths.
The archive uses file entries only (no directory or link entries); an empty
media directory is recreated on restore. Manifest metadata is capped at 16 MiB
and archives at 100,000 payload files; there is no application-level payload
byte limit. Staging and the pre-restore backup must finish successfully
(including disk writes) before swapping; allow space for both, plus migrations.

If a database already exists, restore retains
`<data>/backups/pre-restore-<timestamp>-<unique>.tar.gz`.
Database, SQLite sidecars, and media are saved in the staging directory as
`*.old`; same-volume renames install the staged database and media. The engine
reopens the database, applies migrations, and clears all sessions before
discarding old data. A swap or reopen failure rolls back the renames and keeps
the pre-restore archive. If rollback itself fails, the error names the recovery
directory, which is preserved for manual recovery. Individual renames are
atomic; the pair is not a crash-recovery transaction, so do not interrupt a
restore. Existing retained backups and unrelated temporary files are preserved.
Restart the server and sign in using the restored credentials.
The Web UI supplies maintenance mode, request draining, and live DB handle
replacement; the offline CLI still requires the server to be stopped.

---

## 7. Data Model (SQLite)

```sql
users(
  id INTEGER PK, username TEXT UNIQUE COLLATE NOCASE NOT NULL,
  email TEXT UNIQUE COLLATE NOCASE NOT NULL, password_hash TEXT NOT NULL,
  is_admin INTEGER NOT NULL DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1,
  must_change_password INTEGER NOT NULL DEFAULT 0,
  lang TEXT NULL, timezone TEXT NULL,
  quota_days INTEGER NULL, quota_bytes INTEGER NULL, max_card_bytes INTEGER NULL,
  used_bytes INTEGER NOT NULL DEFAULT 0,
  created_at, updated_at)

sessions(token_hash TEXT PK, user_id FK CASCADE, persistent INTEGER,
  expires_at, created_at, last_seen_at)

settings(key TEXT PK, value TEXT)
  -- site_lang, session_days, default_quota_days, default_quota_bytes,
  -- default_max_card_bytes, backup_retention, setup_token_hash, setup_token_expires_at

collections(id PK, owner_id FK CASCADE, name, color, sort INTEGER,
  is_reserved INTEGER DEFAULT 0, created_at, UNIQUE(owner_id, name))

tags(id PK, owner_id FK CASCADE, name, color, is_reserved INTEGER DEFAULT 0,
  created_at, UNIQUE(owner_id, name))

days(id PK, owner_id FK CASCADE, collection_id FK RESTRICT,
  title, date TEXT, time TEXT NULL,
  repeat TEXT CHECK(repeat IN('none','weekly','monthly','yearly')) DEFAULT 'none',
  comment TEXT NULL, style_color TEXT NULL, style_icon TEXT NULL,
  style_bg_attachment_id INTEGER NULL, archived_at NULL, created_at, updated_at)
  INDEX(owner_id, archived_at, date)

day_tags(day_id FK CASCADE, tag_id FK CASCADE, PK(day_id, tag_id))

attachments(id PK, day_id FK CASCADE, owner_id FK CASCADE, uuid TEXT UNIQUE,
  kind TEXT, filename TEXT, mime TEXT, size INTEGER, created_at)

shares(id PK, owner_id FK CASCADE, target_type TEXT CHECK(IN('day','collection')),
  target_id INTEGER, show_comment INTEGER DEFAULT 0, created_at)

share_recipients(share_id FK CASCADE, recipient_id FK users CASCADE,
  PK(share_id, recipient_id))

share_hidden(user_id FK CASCADE, day_id FK CASCADE, PK(user_id, day_id))
```

Deleting a day/collection also deletes shares targeting it (application-level, since `target_id` is polymorphic). Every query on user data is scoped by `owner_id` or verified share access.

---

## 8. Pages & Routes (summary)

| Route | Purpose |
|---|---|
| `GET/POST /setup` | Initial admin creation with token |
| `GET/POST /login`, `POST /logout` | Auth |
| `GET/POST /password` | Change password (forced when flagged) |
| `GET /` | Home: list of active own + shared days, toggle **Future / Past** (htmx partial `?when=future|past`) |
| `GET /days/new`, `POST /days` | Create |
| `GET /days/{id}` | Calendar card view (countdown, comment, attachments, style) |
| `GET/POST /days/{id}/edit`, `POST /days/{id}/delete` | Edit / delete |
| `POST /days/{id}/archive`, `/unarchive` | Archive toggle |
| `POST /days/{id}/attachments`, `POST /attachments/{id}/delete`, `GET /attachments/{id}` | Attachments |
| `GET /archive` | Archived days |
| `GET /collections`, `/collections/{id}` (+CRUD) | Collections |
| `GET /tags`, `/tags/{id}` (+CRUD) | Tags |
| `GET /shared` | Shared with me |
| `GET /shares`, share CRUD on days/collections | My shares |
| `POST /shared/{day_id}/hide`, `/unhide` | Recipient hide |
| `GET/POST /settings` | Language, timezone, password link |
| `GET /settings/export`, `POST /settings/import`, `POST /settings/import/confirm` | User zip |
| `GET/POST /admin/users` | List usage / create account (optional admin role) |
| `POST /admin/users/{id}/reset-password`, `/activate`, `/deactivate`, `/delete`, `/quotas` | Admin account actions; delete requires confirmation |
| `GET/POST /admin/settings` | Site settings (lang, session_days, default limits, retention) |
| `/admin/backup` (+create, download, delete, restore) | Backup/restore |
| `GET /healthz` | Health check |

Home list ordering: Future ascending by effective target (soonest first); Past descending (most recent first).

---

## 9. Security Checklist
- Authorization check on every resource fetch (owner or share recipient); return 404 for no access.
- CSRF token on every POST; `SameSite=Lax` cookies.
- bcrypt cost ≥ 12; session tokens 32 random bytes, stored hashed.
- Setup token hashed, 1h TTL, one-time.
- Templates auto-escape; never use `template.HTML` on user input.
- Security headers: CSP (`default-src 'self'`; media from self), `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`, `nosniff`.
- Upload: size limits, content sniffing, random storage names, no inline HTML/SVG.
- Archive handling: zip-slip/tar-slip and symlink rejection, size caps on user imports.
- CSV formula-injection escaping.
- No user enumeration via share or login errors.

---

## 10. Out of Scope (v1)
- Notifications / reminders (email, push).
- Self-registration, OAuth/SSO.
- Editing shared days by recipients.
