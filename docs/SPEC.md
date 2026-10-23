# PandaDays — Product & Technical Specification

PandaDays tracks meaningful days: a future target shows how much time is left; a past day shows how much time has passed. Mobile-first, works on laptop. Multi-user, self-hosted via Docker Compose.

Each day detail card defaults to calendar years/months/days and has its own
Calendar / Total days toggle (`?display=days`, also usable without JavaScript).
Total days are counted between the actual dates in the viewer's timezone, not
converted using fixed month/year lengths. Timed days retain remaining hours and
minutes. The toggle also applies to repeating days' time since the original date,
and is available on authorized shared and archived cards. It does not change
other days or the compact list labels.

The main content fills the available viewport width with responsive page padding,
without a fixed desktop width cap. Login, setup, and password forms remain
centered and width-limited for readability.

All pages use the header's panda icon as an embedded SVG favicon, served through
the same content-versioned static asset URLs as the styles and scripts.

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
| Storage | `/data/pandadays.db`, `/data/media/blobs/{sha256[0:2]}/{sha256}`, `/data/backups/`, `/data/tmp/` |
| Deploy | Multi-stage Dockerfile (distroless/alpine runtime), `compose.yaml`, single volume mounted at `/data` |

Dependencies are kept minimal: SQLite driver, `golang.org/x/crypto/bcrypt`,
`golang.org/x/image` for image processing, and `github.com/6tail/lunar-go/calendar`
for Chinese lunar conversion. All are pure Go; no frontend build step is needed.

### Environment variables

| Var | Default | Meaning |
|---|---|---|
| `ADDR` | `:8080` | Listen address; set by the image, not repeated in Compose |
| `DATA_DIR` | `/data` | Data root; set by the image, bind-mounted from `./data` |
| `BASE_URL` | — | Actual public address users visit, including scheme and any public port |
| `COOKIE_SECURE` | `true` | Set `false` for plain-HTTP local dev |
| `TZ` | `UTC` | Server timezone (fallback only) |

Deployment settings live directly in `compose.yaml`, with no `.env.example`
or variable interpolation. Compose runs the prebuilt local
`localhost/pandadays:latest` image and never pulls it. Compose exposes port
8080 without publishing a host port; a containerized reverse proxy shares
the network and connects to `pandadays:8080`. A host-installed proxy requires
an explicit loopback host mapping. Keep the container port (8080) and data
directory (`/data`) unchanged. Language
and user timezone come from the browser and can be chosen in setup/settings;
neither needs a deployment environment setting.

---

## 2. Roles

- **Admin**: site administration only: Users, Backup/Restore, and Site Settings. The avatar/username menu contains Profile, Settings, and Sign out. Admins land on Users and cannot access personal days, archive, sharing, collections, tags, attachments, or import/export routes. Existing owned data is preserved.
- **User**: manages own days, collections, tags, attachments (including the media library), shares; views days shared with them.
- **No self-registration.** Only admin creates users.

---

## 3. Domain Concepts

### 3.1 Day
A meaningful date owned by one user.

| Field | Rule |
|---|---|
| title | required, 1–200 chars |
| date | required (`YYYY-MM-DD`) |
| calendar | `gregorian` (default) or `lunar`; date remains the corresponding Gregorian civil date |
| time | optional (`HH:MM`); if absent the day is all-day |
| repeat | `none` (default), `weekly`, `monthly`, `yearly` |
| collection | exactly one; defaults to the user's reserved **Default** collection |
| tags | zero or more; if none given, the reserved **Default** tag is applied |
| comment | optional, free text (max 10,000 chars); not shown in list view, shown on card view |
| style | optional accent color, icon/emoji, background image (from the day's own image attachments) |
| archived_at | null = active; set = archived |
| archive_live | archived day keeps counting (default 0 = frozen at `archived_at`) |
| attachments | images, audio (incl. in-browser voice recording), video, generic files |
| is_private | defaults to false; private days are accessible only in the password-gated Vault |

The header's **Vault** icon sits between search and the light/dark switch, not
in the avatar dropdown. Toggling on opens a password confirmation dialog with
an X and Escape to cancel without changing visibility. After verification,
home, future/past views, title/comment search, archives, collections, and tags
include normal and private days together; shared days remain visible as before.
Private list rows have a distinct border and localized Private day badge.
The pressed icon toggles off without another password prompt and hides private
rows and their collection/tag counts again, preserving the current normal view.
Private day CRUD, attachments, media, and export/import retain their authorized
`/private` routes. New days outside private routes default to normal (unchecked Private
day); new days inside Vault default to private. Editing the checkbox moves a
day between namespaces. "Normal/public" does not grant anonymous access.
While locked, private days never appear in home, search, archive, collection/tag
contents or counts. Normal media listings and exports always exclude them.
They retain their collection and tags. Private attachments
also require unlocked Vault access. Both namespaces count toward account quotas.
Private days cannot be shared, including through collection shares; making a
day private revokes direct shares. Making it normal again does not restore those
direct shares (normal collection-sharing rules apply again).
Unlock is scoped to the current login session and persists across navigation
until the Vault icon is toggled off or the session ends. The legacy
`/private/unlock` entry still requires the password again. Password confirmation uses
the existing login rate limiter and CSRF protection. Private and unlocked pages disable htmx
history snapshots and use non-cacheable responses.
Vault export/import routes use the same `/private` prefix. The optional
`is_private` CSV column preserves privacy across imports; legacy CSV is still
accepted, and importing within Vault always creates private days. Site backups
still include all data and must be treated as sensitive.

Create and edit forms warn through the browser's native exit confirmation when
unsaved fields would be lost by navigation, reload, or closing the tab. Unchanged
forms and valid save submissions do not prompt; validation errors remain unsaved.
Draft contents are not stored in browser storage.
Day forms place title and privacy on the first row, date/time and recurrence on
the second, collections and tags on the third, and comments below on desktop.
Mobile fields stack in a single column. New-day time defaults to 00:00; clearing
it still creates an all-day date, and editing preserves the saved time.
An unchecked-by-default Chinese lunar checkbox switches Gregorian / Chinese lunar
entry without changing the selected civil day; editing preserves the saved choice.
With JavaScript, date and 24-hour time use one bordered control containing
separate editable year, month, day, hour, and minute segments and an automatically
updated weekday in the selected language. There are no scrollers or calendar
popups. Numeric lunar entries format as Chinese year/month/day text on commit;
an explicit leap-month checkbox selects leap months. Calendar conversion uses
compact server-generated calendar data included in the form, without network
requests during editing. Invalid dates remain editable with localized validation
instead of silently rolling over or clamping.
The privacy control is a borderless icon-only toggle: unlocked means public,
locked means private. It preserves saved privacy and Vault-route defaults.
Lunar picker years, months, and days use Chinese characters in both languages;
year/month/day fields accept Chinese text or numbers while storing numeric values.
Time can still be cleared for an all-day date. Lunar mode includes explicit
leap-month options and supports lunar years 1900-2100. Native fields remain
available without JavaScript. Create and edit preserve the
calendar choice. Native calendar and date controls remain usable without JavaScript.
Cards and lists display localized lunar labels alongside their Gregorian dates.
Successfully creating a private day returns to the normal homepage without
unlocking Vault or opening its password screen. The header privacy icon remains
the entry point for the password dialog.
With JavaScript, all file uploads show transferred-byte progress and remain busy
while the server processes the uploaded files. Failures are shown explicitly and
allow retry; validation pages and import/restore previews are preserved. Without
JavaScript, forms retain native submissions.
New-day forms include optional multiple-file attachments, uploaded on creation
under the existing per-day and account quotas. Invalid fields or oversized files
do not create a day; files must be selected again after validation errors. If a
storage failure occurs after the day is saved, the error explicitly identifies
the saved day and links to its attachment manager rather than inviting duplicate
creation. Selected files participate in unsaved-change warnings.
Create and the card menu's attachment manager share Media library, Upload file,
and Voice recording tabs. Recordings can be previewed before saving/uploading;
unsupported browsers show an explicit microphone/recording availability message.
While recording, a live microphone-level bar and elapsed minutes/seconds show
input activity and recording duration. A play icon to the right starts or resumes
recording; Stop pauses without finalizing and Finish produces an audio preview.
Paused time is excluded from the duration, and unfinished recordings block saving.
Local blob URLs are allowed for image thumbnails and media previews in the content
security policy. Uploaded audio attachments are playable directly
beneath the day card's comment, including on authorized shared cards. When any
attachments exist, a separator and collapsed Attachments disclosure follow the
comment. Expanding it lists every attachment type without individual item borders,
with image/video previews and original-file links for other files.
The edit form contains day fields and Save only; attachments and deletion remain
in the card menu. An image background can be cleared without deleting its attachment.
The media library uses a compact thumbnail/name grid. Opening an item shows its
preview with details, source day, original-file link, and Delete in a side column
(stacked below the preview on mobile), without repeating the Media page heading.
The filename remains visible in the details.
The media library is a newest-first timeline grouped by upload day (default) or
month in the user's saved timezone (server timezone when unset). A media-type filter selects
all media, images, videos, voice/audio, or generic files. Grouping and filtering
are URL-backed and work without JavaScript. Each group retains compact preview
tiles, filenames, types, and sizes; videos show inline previews. Empty filtered
results are distinguished from an empty library.
The library shows each owned content hash once, including existing day references.
Non-image files use file-type document icons labeled by extension, with distinct
format pictograms in upload selections, library tiles, and the day media picker.
Selecting library files uses Select, not Upload, and reuses stored bytes; selecting
an already attached file does not add another attachment. Day references retain
independent metadata, removal, and existing per-day/account quota accounting.
A plus button beside the media filters opens an upload dialog that works before any day exists. Standalone
uploads count toward the account quota, remain owner-only until selected for a
day, and can be renamed or deleted from media details. All file selectors list
selected filenames and image thumbnails before submission, with a remove button
on each selected file. Multi-file selections append across repeated chooser visits
and drag-and-drop, including files from different folders. File upload fields share
an app-themed dashed drop area with document/upload and close SVG icons; avatar,
import, and restore fields retain their single-file constraints. Without JavaScript,
native file fields and an inline library upload form remain available. Full site backups
include standalone library files; the day CSV/ZIP export includes day attachments.
Cards and attachment managers show audio as a compact filename/size row with
Play, rename, and remove controls (Play only for shared read-only cards).
Play reveals and starts only the native player below that row, without a repeated
filename or nested bordered panel; playback errors appear inline below the player.
Only one expandable audio player stays open at a time. Switching recordings,
clicking outside the player/Play button, closing its attachment disclosure or
dialog, or pressing Escape pauses and collapses it.
Media details and recording previews retain their named audio panels.
Missing stored files return an authorized, localized HTTP 410 explaining
backup restoration or replacement; storage permission errors identify the access
problem without exposing server paths. Playback failures show these diagnostics
inline instead of leaving only a disabled player. Valid media can be selected from
the library for multiple days; missing source files cannot be recovered by copying their
database entries.

The attachment manager shows only this day's used space, without quota progress
bars or account storage. An Add button expands the three attachment-source tabs.
Attachment rows show an ellipsized filename (up to 28ch, with the full name in its
tooltip), human-readable size, and trailing rename/remove controls. Audio rows
include a Play button that reveals and starts the player below the row. Removal
keeps the file in the media library. All uploaded attachment types can be renamed
from the day or media details, including removed library items; this changes only
their display/download name, not file contents, type, storage ID, or quota usage.
Stopped recordings have an editable filename before saving/uploading. Renames
require 1–255 characters without paths or control characters.

With JavaScript, tags use a single-line, 44px-high editor aligned with other form
controls. Selected chips scroll horizontally within the field. Typing `#` filters
the owner's existing tags in an overlay dropdown that does not move other fields.
The first match is selected initially; hovering or Up/Down arrows changes the
selection, and click, Tab, or Enter accepts it. Shift+Tab retains normal backward
navigation; Escape, outside clicks, and leaving the field dismiss suggestions.
The picker supports removable selected chips that do not display the trigger `#`. A nonmatching name
offers a create option. The automatic reserved Default tag is omitted from the
picker; no explanatory text is shown beneath the tag editor. New tag names and any remaining `#name` input are resolved
case-insensitively against the owner's tags or created atomically with a successful
day save; failed validation or authorization never leaves orphan tags. Without
JavaScript, existing tag checkboxes and a `#name` text field remain available.
The style editor offers 84 icon presets while retaining custom emoji/text and a
reset to the default icon. The icon grid uses the same available field width as
the color palette and other form controls.
Day, collection, and tag color controls offer 20 presets followed by a rainbow
swatch opening the native custom color picker; choosing a color clears "No color".

### 3.2 Collection
A "book" of days. Each day belongs to exactly one collection. Each user has a reserved **Default** collection (cannot be renamed or deleted). Deleting a non-default collection moves all its days to Default (single transaction).

Collections appear as notebook-style cards containing the name, background-color
tint, optional comment (maximum 10,000 characters), and day count. Opening a card
shows the days in that subgroup. A `+` button opens the create dialog with name,
color, and comment; comments and colors can also be edited in collection detail.

### 3.3 Tag
Describes the nature of a day; many-to-many. Each user has a reserved **Default** tag (cannot be renamed or deleted). Deleting a tag removes it from days; any day left with zero tags gets Default.

The Tags page shows wrapping, content-width tag-shaped links with day counts,
rather than full-width rows. A `+` button opens the create dialog. Both tag and
collection dialogs retain validation errors and entered fields, support Escape
and Cancel, and have a link-based creation fallback without JavaScript.

Tag and collection detail pages show their day list without inline management
forms. Icon-only actions beside the name toggle archived entries and open Edit
and Delete dialogs; collections also have a Share settings icon. Reserved groups
retain color editing but cannot be renamed or deleted. Dialogs support Escape,
Cancel, and link-based opening without JavaScript; edit validation reopens the
dialog with the submitted fields and error.

Reserved names are displayed localized ("Default" / "默认") but stored with `is_reserved=1`; users cannot create another collection/tag literally named the reserved name.

### 3.4 Countdown / elapsed calculation
- All calculations happen in the **user's timezone**.
- Effective target:
  - `repeat = none`: the day's own date(+time).
  - repeating: the **next occurrence** at or after "now" (today counts as today, not past). Repeating days are always treated as **future**.
- Monthly repeat on day 29–31: clamp to the last day of shorter months.
- Yearly repeat on Feb 29: Feb 28 in non-leap years.
- Lunar schedules keep weekly repeats at seven local calendar days. Monthly
  repeats visit each lunar month, including leap months. Yearly repeats use the
  original lunar month and day; a leap-month anchor falls back to the regular
  month when that leap month does not exist in the target year. Day 30 clamps to
  day 29 in shorter months, always retaining the original day for later repeats.
  Lunar occurrences outside lunar years 1900-2100 are rejected explicitly.
  Countdown breakdowns remain Gregorian elapsed years/months or actual total days.
- Display:
  - List: `N days left` / `N days ago` / `Today`. Timed differences under 24 hours use zero-padded `HH:MM left` / `HH:MM ago`, without seconds. List entries show the icon, then the highlighted effective date (time below it) in a fixed-width column so titles align vertically, then title, recurrence, and badges.
  - Card: centered square calendar (up to 32rem wide, growing vertically for long content), calendar-style icon/title row, large date row, then calendar years/months/days and `HH:MM` (if time set), labeled as remaining or elapsed. For repeating days, also show the next occurrence, occurrence ordinal (e.g. "3rd anniversary"), and time since the original date.
  - Below a separator, the created date/time (owner only, user timezone), recurrence, non-default collection, and non-default tags share a wrapping row without collection/tag labels. Owners have a right-aligned three-dot menu for Edit, Archive/Unarchive, Delete (with confirmation), Share, and Attachment. Shared recipients remain read-only.
  - The optional visible comment follows the metadata row. Owner Share and Attachment menu links open modal dialogs outside the calendar, with Close and Escape support. Without JavaScript, the links open fixed overlays with Close links. Attachment and share actions redirect back to the appropriate open dialog. Shared attachments remain read-only and expandable.
  - Day list entries omit reserved Default collection and tag badges, even when no other groups are assigned. Non-default group badges remain visible in home, search, subgroup, and archive lists. Shared recipients retain an expandable read-only attachments section. Menu actions use consistent plain styling, with Delete distinguished by its danger color.
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
- A newly archived day is **paused**: every list and card calculates it at `archived_at` instead of now, as a snapshot. Owners can press the play button (shown as a pause icon while paused, after the display-mode toggle on the card) to resume live counting, and press it again to return to the snapshot. The play state is saved per day, is shown to share recipients read-only, and resets to paused on unarchive.
- Archived days are hidden from Home.
- Archived days are visible in: Archive page, Collection detail, Tag detail (with a filter to hide them).

### 3.7 Sharing
- Regular users have Home, Collections, Tags, and Media in the header. Archive and Sharing live in the avatar dropdown alongside Messages, Profile, Settings, and Sign out. Owned day detail pages do not repeat a Home link.
- Sharing opens `/sharing` with **Shared to Others** and **Shared to Me** tabs. Shared to Others lists each day or collection the current user shares once, including pending/rejected invitations and archived owned days, linking to its share settings. Revoked shares and deleted targets are omitted. Shared to Me keeps `/shared` and the existing accepted-day list, including hidden days.
- Regular users have a borderless magnifier immediately left of the light/dark toggle. It expands inline in the same header row into a borderless search field with a magnifier submit button on its right, not a dropdown below the header. Search covers active owned day titles/comments and accepted, non-hidden shared day titles/comments (comments only when the owner permits viewing them). Matching is case-insensitive and literal (including Chinese), across future and past. Archived owned days, hidden shared days, and unaccepted shares are excluded; clearing search restores normal Home date tabs. Search opens without JavaScript; with JavaScript it focuses the field and closes on Escape or outside click.
- Targets: a single **day** or a whole **collection**.
- Recipients: one or more users, identified by **username or email** (case-insensitive). Unknown identifiers return a generic error (no user enumeration). Cannot share to self.
- One share has many recipients (`share_recipients` table).
- New recipients receive a pending invitation and have no shared day or attachment access until accepting it. Existing recipients at upgrade remain accepted.
- The profile avatar shows the number of pending invitations; its Messages menu opens `/messages`, a newest-first queue with 20 invitations per page and Load more (with a JavaScript-free next-page fallback).
- Accept/reject decisions are per recipient. Rejected invitations remain in Messages and can be accepted later; accepted invitations can also be rejected. Revoked/deleted shares are no longer actionable. Editing a share preserves retained recipients' decisions; newly added recipients start pending.
- Per-share `show_comment` toggle (default off). Recipients see title, date, time, repeat, style, attachments; they see the comment only if `show_comment=1`. Owner's collection and tags are never exposed.
- Collection share is live: recipients see all current non-archived days of that collection.
- Shared items are **read-only** for recipients.
- Recipients may hide a shared day from their Home (`share_hidden`); still listed under "Shared to Me".
- Owner can edit recipients, toggle comment visibility, or revoke shares.
- If the same day is reachable through multiple accepted shares, it appears once; comment visible if any applicable accepted share has `show_comment=1`.

### 3.8 Quotas & limits (admin-adjustable)
| Setting | Default | Scope |
|---|---|---|
| `quota_days` | 1000 | max days per user (archived count) |
| `quota_bytes` | 1 GiB | total attachment storage per user |
| `max_card_bytes` | 100 MiB | total attachment size per day |
| `session_days` | 30 | "Keep me signed in" duration |

Site-wide defaults live in `settings`; each user row may override (`NULL` = use site default). Enforcement on create day, upload, and import. Server uses `http.MaxBytesReader` sized to remaining card allowance.

Admins manage accounts and usage at `/admin/users` and defaults at `/admin/settings`.
The users page is a searchable list (username/display name/email); Create user opens a separate
form, and each user has a separate management page. Personal password changes
are linked from Profile and the administrator's own action menu, not the main navigation.
Storage quota inputs and user usage use M (1 M = 1,048,576 bytes); fractional
values are accepted when they represent an exact whole number of bytes.
Storage remains integer bytes internally, including inherited and zero limits.
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
- Usernames contain 4–32 characters and may use only lowercase ASCII letters, numbers, and hyphens.
- After an admin exists, `/setup` returns 404.

### 4.2 Login
- Username or email + password.
- "Keep me signed in" checkbox: session lifetime = `session_days` (admin setting); cookie persistent. Unchecked: session cookie (no Max-Age), server-side expiry 12h.
- Rate-limit failed logins per IP+account (e.g. exponential backoff, in-memory).
- Inactive users cannot log in.

### 4.3 Passwords
- Length 8–32 characters (Unicode allowed; counted in runes, bcrypt input ≤ 72 bytes enforced).
- Admin-created users and all resets get a **random 16-char password**, shown once, with `must_change_password=1`.
- While `must_change_password=1`, application routes redirect to change-password except password change, logout, language switching, and authorized avatar retrieval; static assets and health checks remain available.
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
- Timezone: per-user setting, detected from the browser's `Intl.DateTimeFormat().resolvedOptions().timeZone` during setup, editable before submission and later in Settings. If empty, the browser timezone is posted on first page load after login and saved. Validated with `time.LoadLocation` (embed `time/tzdata`). Server timezone (UTC in the image) is only a fallback until capture succeeds.
- Language: `en` or `zh`, selectable during setup and later with the header toggle. Resolution order: user setting → site setting → `Accept-Language` → `en`. Setup defaults to browser language; there is no language environment variable.

### 4.6 Profile and account controls
- The header has borderless light/dark and globe-icon Chinese/English toggles of the same size, placed to the left of the navigation links for every user. Theme defaults to the browser preference and persists in a browser cookie; switching language saves the signed-in user's preference and stays on the current page, preserving filters. Language is not a profile dropdown.
- Profile (`/profile`) contains only identity. A square container holds the circular avatar, without an overlaid X. Clicking the avatar opens a dialog with upload/replacement and removal controls; the upload area and selected filename stay inside the dialog, never below the avatar. Selected photos preview in the circle; changes and removal apply on saving the form. Admin user creation and editing share this avatar editor. Without JavaScript, clicking the avatar expands the native file input and removal checkbox inline. Username (read-only), display name, and email are three rows beside the avatar on wide screens and below it on phones. Usernames are unique and fixed after creation. Changing one's own email requires the current password, sharing the password-change backoff. The password is a fixed mask with a reset button on the right that opens the authenticated password-change form. Actual passwords and hashes are never rendered.
- Settings (`/settings`, in the avatar menu) holds timezone, storage usage, and backup & restore (personal export/import). Admins see timezone only.
- Display names are at most 100 characters; optional administrative comments are at most 10,000. Avatars are optional validated PNG/JPEG uploads, at most 1 MiB and 2048 × 2048 pixels, stored in SQLite and included in full backups. They are separate from day-attachment quotas and accessible only to the account owner or an administrator.
- Users has a reusable, fixed-style search field with an inset magnifier, Enter-to-search, and an adjacent plus-icon create link. Search matches username, display name, or email case-insensitively.
- Creation fields are username (4–32 lowercase ASCII letters, numbers, and hyphens), display name, optional avatar, email, and optional comment. The compact list shows username, display name, created time, days used, and active/inactive status. Times display in the viewing admin's saved timezone, or the browser timezone when none is saved. Clicking a row opens its detail page.
- List rows and the detail page share a three-dot menu with Edit, Enable/Disable, Reset password, and Delete. Delete opens a modal dialog (loaded from `GET /admin/users/{id}/delete`, which is also a no-JS confirmation page) showing all of the user's details and usage before the confirmed delete; self-disable/delete remain unavailable. Detail shows identity, avatar, timezone, language, role, status, creation time, comment, password-change requirement, and effective quota/usage with editable quota overrides for regular users. A back-arrow icon returns to Users.

---

## 5. Attachments

- Kinds: `image`, `audio`, `video`, `file`. Kind is derived from sniffed content type, not the client header: `http.DetectContentType` on the first 512 bytes, refined from container metadata in the first 64 KiB (audio-only WebM/MP4/Ogg MediaRecorder voice notes → audio; QuickTime `.mov` → `video/quicktime`).
- Voice: in-browser recording via `MediaRecorder`, uploaded as audio.
- Stored content-addressed at `/data/media/blobs/{sha256[0:2]}/{sha256}` (no original filename on disk). Each attachment row keeps its own owner, day, filename, type, and timestamps, and points to the blob by `sha256`; identical bytes uploaded by different users are stored once on disk but remain independent attachments, each charged to its owner's quota and visible only through that owner's access rules. A blob is deleted only when no attachment row references it (deletions queue the hash in `blob_gc`; the server collects after deleting and at startup). Files from the legacy `/data/media/{user_id}/{uuid}` layout are hashed and moved into the blob store at startup; rows whose legacy file is missing keep a null hash and report the missing file.
- Duplicate uploads: a user cannot upload content that is already in their own media library (including items removed from a day). Uploads and new-day attachments are checked before anything is saved, including the same file selected twice; the request fails with HTTP 409 and a localized message pointing to the **Media library** tab. Picking the existing library item is the supported way to attach it to another day. Imports are not rejected; their content is shared with existing blobs.
- Served only via `/attachments/{id}` with authorization (owner or valid share recipient). Headers: `X-Content-Type-Options: nosniff`; images/audio/video inline with sniffed type; everything else (and any HTML/SVG/XML) as `Content-Disposition: attachment` with `application/octet-stream`. Support HTTP range requests (`http.ServeContent`) for media playback.
- Limits: per-card total ≤ `max_card_bytes`; per-user total ≤ `quota_bytes`. `users.used_bytes` maintained transactionally.
- Removing an attachment from a Day keeps its file in the media library for reuse and keeps it counted toward account storage, but removes it from per-card usage, the card background, and shared access. The original source-day link is retained. Permanent deletion from the media library, or deletion of the source day, deletes files and decrements `used_bytes`; backgrounds fall back to default.
- Media library: a header **Media** link (users only; admins get 404) opens `/media`, listing every attachment the user owns across all days, newest first, as thumbnails and filenames. Opening `/media/{id}` shows a larger preview with size, upload time, type, source day, original-file link, and delete (returning to the library). Shared-with-me attachments are not included.
- Attachment picker: **Add attachments** opens a modal dialog on creation; the card attachment manager uses the same **Media library**, **Upload file**, and **Voice recording** tabs inline within its dialog. Without JavaScript all sections render inline, with recording explicitly unavailable. Picked library items (`library` form values) become independent attachments of the target day sharing the same stored blob, so deleting either one leaves the other intact. Copies, uploads, and recordings are checked together against the per-card and account quotas before saving attachments (and before creating a new day). Library IDs not owned by the user return 404 and create nothing. New-day library picks are retained after validation or quota errors; files must be selected again.
- With JavaScript, the existing-day Upload button is disabled until at least one library item, local file, or completed recording is selected. Creating a Day still allows no attachments. Day exports include only current attachments, not removed library-only media; site backups retain all media.

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
title,date,time,repeat,collection,tags,comment,archived,style_color,style_icon,style_bg,attachments,archive_live,is_private,calendar
```
- `archive_live` is `true`/`false` for archived days (resumed vs. frozen counter) and empty otherwise. Imports accept files without this column; missing or empty values import archived days paused, and the value is ignored for unarchived rows.
- `is_private` is `true`/`false`; missing or empty means normal, except Vault imports always create private days. Normal exports include only normal days; Vault exports include only private days. Staged import tokens are bound to both the owner and privacy namespace.
- `calendar` is `gregorian` or `lunar`; missing or empty defaults to Gregorian for legacy CSV files. `date` always contains the equivalent Gregorian date, preserving leap-month anchors unambiguously.
- `tags` and `attachments` are `|`-separated; attachments refer to paths inside `media/`; `style_bg` refers to one of them.
- Cells beginning with `= + - @ \t \r` are prefixed with `'` (CSV injection guard); importer strips that prefix.
- Only the user's own days (not shared-with-me).

### 6.2 User import
- Accepts the same zip (or a bare `days.csv` without attachments).
- Mode: **merge**; rows matching an existing day by `(title, date, collection, privacy, calendar)` are skipped.
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
- Created into `/data/backups` and listed (retention count setting, default 5). Admin can download, restore, or delete listed backups. The legacy create-and-download API can optionally retain a copy.

**Restore**
1. Admin selects a retained archive or uploads one (streamed to `/data/tmp`, no size cap at app level) and re-enters password.
2. Validate: manifest present, checksums match, schema version ≤ current, no unsafe paths/links, free disk space sufficient.
3. Enter maintenance mode: new requests get localized 503; drain in-flight requests; close DB.
4. Auto-create pre-restore snapshot `/data/backups/pre-restore-<ts>-<unique>.tar.gz`.
5. Atomic swap via `rename` on the same volume (`pandadays.db`, `media/`); old copies kept as `*.old` until success.
6. Reopen DB, run migrations, delete all sessions, exit maintenance.
7. Admin redirected to login (credentials from restored data).
8. On failure: rename old files back, keep snapshot, reopen the original database, exit maintenance, show error. If rollback or reopening fails, remain in maintenance and log recovery details rather than serve potentially inconsistent data.

The admin navigation links to `/admin/backup`, a list with Create backup and
Restore buttons. Create backup retains the archive and returns to the list
(requires nonzero retention). Each entry has a three-dot menu with Download,
Restore, and confirmed Delete actions. Restore opens a separate confirmation
form and always requires the current admin password, for both a retained archive
and a new upload. The existing download-only POST API remains supported.
Creation is a CSRF-protected POST. The completed archive is staged
on the data volume before streaming the download so creation errors can be
reported without a partial archive. Allow disk space for the database snapshot
and compressed archive. Maintenance also pauses requests during creation and
download, keeping database and media consistent.

Retained ordinary backups are pruned newest-first to `backup_retention` after
creation and when site settings are saved. Zero disables ordinary retention.
Pre-restore recovery snapshots are listed and downloadable but excluded from
automatic pruning; delete them explicitly using the menu's Delete confirmation.
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
`DATA_DIR` to the site's data directory, then run:

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
  display_name TEXT NOT NULL DEFAULT '', comment TEXT NOT NULL DEFAULT '', avatar BLOB NULL,
  quota_days INTEGER NULL, quota_bytes INTEGER NULL, max_card_bytes INTEGER NULL,
  used_bytes INTEGER NOT NULL DEFAULT 0,
  created_at, updated_at)

sessions(token_hash TEXT PK, user_id FK CASCADE, persistent INTEGER,
  expires_at, created_at, last_seen_at, vault_unlocked INTEGER DEFAULT 0)

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
  calendar TEXT NOT NULL CHECK(calendar IN('gregorian','lunar')) DEFAULT 'gregorian',
  comment TEXT NULL, style_color TEXT NULL, style_icon TEXT NULL,
  style_bg_attachment_id INTEGER NULL, archived_at NULL, archive_live INTEGER DEFAULT 0,
  is_private INTEGER DEFAULT 0,
  created_at, updated_at)
  INDEX(owner_id, archived_at, date)

day_tags(day_id FK CASCADE, tag_id FK CASCADE, PK(day_id, tag_id))

attachments(id PK, day_id NULL FK CASCADE, owner_id FK CASCADE, uuid TEXT UNIQUE,
  kind TEXT, filename TEXT, mime TEXT, size INTEGER, created_at, detached INTEGER,
  sha256 TEXT NULL, is_private INTEGER DEFAULT 0)  -- indexed by (owner_id, sha256) and (sha256)
blob_gc(sha256 PK)  -- filled by an AFTER DELETE trigger on attachments

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
| `GET /` | Home: list of active own + accepted shared days, toggle **Future / Past** (htmx partial `?when=future|past`); `?q=...` searches visible day titles and authorized comments across both |
| `GET/POST /private/unlock` | Legacy password entry for Vault |
| `POST /vault/unlock`, `POST /vault/lock` | Password-confirmed session visibility toggle; both require CSRF |
| `/private/`, `/private/days/...`, `/private/archive`, `/private/collections/...`, `/private/tags/...`, `/private/media/...`, `/private/attachments/...`, `/private/settings/...` | Password-gated private details/actions/media/export; lists include all days while unlocked; private sharing is unavailable |
| `GET /days/new`, `POST /days` | Create |
| `GET /days/{id}` | Calendar card view (countdown, comment, attachments, style) |
| `GET/POST /days/{id}/edit`, `POST /days/{id}/delete` | Edit / delete |
| `POST /days/{id}/archive`, `/unarchive` | Archive toggle |
| `POST /days/{id}/play`, `/pause` | Resume / freeze an archived day's counter |
| `POST /days/{id}/attachments`, `POST /attachments/{id}/delete`, `GET /attachments/{id}` | Attachments |
| `GET /media` | Media library: all own attachments across days |
| `GET /media/{id}` | Owner-only media preview, details, and source |
| `GET /archive` | Archived days |
| `GET /collections`, `/collections/{id}` (+CRUD) | Collections |
| `GET /tags`, `/tags/{id}` (+CRUD) | Tags |
| `GET /shared` | Shared with me |
| `GET /messages` | Received share invitations; `?before=share_id` loads the next 20, with an htmx append fragment |
| `POST /messages/{id}/accept` | Accept or re-accept a share invitation (recipient only) |
| `POST /messages/{id}/reject` | Reject a share invitation, retaining it for later acceptance (recipient only) |
| `GET /shares`, share CRUD on days/collections | My shares |
| `POST /shared/{day_id}/hide`, `/unhide` | Recipient hide |
| `GET/POST /profile`, `POST /profile/avatar/delete` | Display name, email (current password required to change), avatar, password link |
| `GET/POST /settings` | Timezone, storage usage, backup & restore |
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
- Security headers: CSP (`default-src 'self'`; media from self and local `blob:` recording previews), `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`, `nosniff`.
- Upload: size limits, content sniffing, random storage names, no inline HTML/SVG.
- Archive handling: zip-slip/tar-slip and symlink rejection, size caps on user imports.
- CSV formula-injection escaping.
- No user enumeration via share or login errors.

---

## 10. Out of Scope (v1)
- Notifications / reminders (email, push).
- Self-registration, OAuth/SSO.
- Editing shared days by recipients.
