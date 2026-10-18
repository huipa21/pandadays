# PandaDays

Self-hosted countdowns and anniversaries for meaningful days, with private
multi-user spaces, collections, tags, attachments, and read-only sharing.
English and Simplified Chinese are included. No self-registration, external
database, or frontend build step is needed.

## Install with Docker Compose

Requirements: Docker Engine with the Compose v2 plugin (or Docker Desktop),
and a local checkout of this source tree. Run from the project root:

```sh
docker compose up -d --build
docker compose ps
docker compose logs pandadays
```

Open <http://localhost:8080/setup>. The default Compose configuration binds only
to localhost and allows plain HTTP cookies for local use. For remote access,
configure HTTPS as described below before exposing the service.

### First-run setup and accounts

1. Find the one-time setup token in `docker compose logs pandadays`.
2. Enter it at `/setup`, choose an admin username (for example, `admin`), email,
   and password. Setup signs you in automatically.
3. Open **Admin users** (`/admin/users`), create a user, and securely deliver
   their displayed temporary password. It is displayed only in that response.
4. Sign out, then sign in at `/login` as the new user. On first login they must
   change the temporary password.

The setup token expires after one hour and can be used only once. An unexpired
token is not reprinted on restart; use the earlier logs. If it has expired,
run `docker compose restart pandadays`, then read the logs for a new token.
Once an admin exists, setup is disabled. Treat container logs containing the
token as sensitive.

### Reset an admin password

With the service running:

```sh
docker compose exec pandadays panda reset-admin-password --username admin
```

Substitute the actual admin username if different. The command prints a new
random password once, signs that account out everywhere, and requires a
password change at next login. It does not reactivate a deactivated account.
Do not run it during a restore.

## Configuration

Optionally copy [`.env.example`](.env.example) to `.env` and edit it before
starting. Compose reads `.env` automatically; do not commit deployment secrets.
After editing, use `docker compose up -d` to recreate the service with the new
configuration (a restart alone does not reload environment changes).

| Variable | Binary/image default | Compose default / notes |
|---|---|---|
| `PANDA_ADDR` | `:8080` | Fixed to `:8080` inside the container. |
| `PANDA_DATA_DIR` | `/data` | Fixed to `/data`, backed by the named volume. |
| `PANDA_BASE_URL` | Empty | `http://localhost:8080`; set to the public URL. |
| `PANDA_LANG` | `en` | Blank: use browser language, then English. Explicit `en` or `zh` sets the site default; user/site preferences can override it. |
| `PANDA_COOKIE_SECURE` | `true` | `false` for local HTTP only; **set `true` for HTTPS deployments**. |
| `TZ` | `UTC` | `UTC`; IANA timezone fallback, e.g. `Asia/Shanghai`. User timezones take precedence for date calculations. Timezone data is embedded. |
| `PANDA_BIND` | Compose only | `127.0.0.1`; host address for the published port. |
| `PANDA_PORT` | Compose only | `8080`; host port. Update the base URL if changing it. |
| `PANDA_VERSION` | Build argument only | `dev`; binary version and backup manifest metadata. Rebuild after changing it. |

The image runs as UID/GID `10001:10001`. The default named volume is initialized
with writable permissions. If replacing it with a bind mount, pre-create that
directory and grant this UID/GID read/write access; the image does not start as
root to repair permissions. Keep the container data path and port unchanged
unless also updating mounts, port mappings, and any proxy configuration.

### Storage, health, and updates

The single `panda-data` volume contains `pandadays.db`, SQLite sidecars,
`media/`, `backups/`, and `tmp/`. Keep it on local storage suitable for SQLite,
not a shared network filesystem. `docker compose down` preserves it;
**`docker compose down -v` deletes the volume and all site data**.

`GET /healthz` is unauthenticated. The image and `compose.yaml` probe it every
30 seconds (the Compose copy keeps health checks working with Podman, whose
OCI-format builds drop the image `HEALTHCHECK`). The image runs
the static, CGO-free Go binary as a non-root user. Health checks return 503
during restore maintenance; this is expected. Docker's restart policy handles
process exits, not an unhealthy status by itself.

To update, first download a site backup, update the source checkout, then run:

```sh
docker compose up -d --build
docker compose logs --tail=100 pandadays
```

Migrations run automatically at startup. Do not assume an older binary can
read a newer schema; keep the previous source/image and a pre-upgrade backup.
The service handles termination gracefully. Never stop or upgrade it during
a restore.

## Backups and restore

### Recommended: admin Web UI

Use `/admin/backup` to create and download a full-site `.tar.gz` archive,
optionally retaining it on the server. The same page lists retained backups
for download and confirmed deletion; ordinary retention pruning excludes
pre-restore recovery snapshots.

To restore, upload an archive and re-enter your current admin password.
Validation completes before maintenance starts. The server then drains active
requests, takes a recovery snapshot, replaces the database and media, and
clears all sessions. Wait for completion and sign in with the **restored**
credentials. Other requests receive 503 during maintenance. Do not interrupt
the restore, even if the browser disconnects.

### Offline CLI in Compose

Both CLI backup and CLI restore require the web service to be **stopped**.
Do not use `docker compose exec` for these operations on a running server.
One-off containers below share the configured data volume and non-root user.

Create a backup using a new filename (existing output files are never overwritten):

```sh
docker compose stop pandadays
docker compose run --rm --no-deps pandadays backup --out /data/manual-backup.tar.gz
docker compose cp pandadays:/data/manual-backup.tar.gz ./pandadays-backup.tar.gz
docker compose start pandadays
```

Keep an off-host copy. The CLI output path above is outside the retained
`backups/` list and is not automatically pruned.

To restore that downloaded archive to an existing deployment:

```sh
docker compose stop pandadays
docker compose cp ./pandadays-backup.tar.gz pandadays:/data/restore-input.tar.gz
docker compose run --rm --no-deps pandadays restore --file /data/restore-input.tar.gz
docker compose start pandadays
```

`docker compose cp` requires an existing service container. On a fresh host,
build and create it first with `docker compose build` and
`docker compose create pandadays` (do not start it before restoring).
Ensure copied restore files are readable by UID 10001. If a command fails,
inspect the error and recovery files before starting the service.

Backups include all users, settings, password hashes, session records, and
media. Store and transport them securely. Per-user Settings export/import is
available for moving one user's days and attachments; it is not a site backup.

Allow free space for archive staging, the existing data, restored data, and
the pre-restore snapshot. Existing databases get a recovery archive under
`backups/pre-restore-*.tar.gz`; delete these explicitly when no longer needed.
If rollback/reopen fails, leave the service stopped or in maintenance and
follow the recovery directory reported in the server logs. Restore uses
same-volume renames but is not a crash-atomic transaction across all files.
See [the backup specification](docs/SPEC.md#63-admin-site-backup--restore-admin--backup-web-ui) for details.

## Reverse proxy and HTTPS

Terminate TLS at a trusted reverse proxy. Set these in `.env`:

```dotenv
PANDA_BASE_URL=https://days.example.com
PANDA_COOKIE_SECURE=true
```

Keep the default localhost binding when the proxy runs on the host. A proxy
in another container must instead share a Docker network with PandaDays and
connect to `http://pandadays:8080`; `localhost` inside that proxy is not the
PandaDays container. Avoid publishing the application directly to the Internet.

Example Nginx location inside an HTTPS virtual host, with Nginx on the host:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    client_max_body_size 0;
    proxy_request_buffering off;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
}
```

Configure certificates and the HTTP-to-HTTPS redirect in your proxy. The body
size above is unlimited because full-site restore uploads can be much larger
than a single attachment; choose a sufficiently large explicit limit if your
policy requires one. Adjust timeouts for your largest restore, and check any
other proxy/CDN limits. Application attachment and import quotas still apply.
HTTPS is also required for browser microphone recording outside localhost.

## Develop or run without Docker

Use Go 1.23 or newer (see `go.mod` for the module requirement). The Docker
builder uses Go 1.26. Templates, vendored htmx, translations, migrations, and
timezone data are embedded; no Node.js, CGO, or external SQLite is needed.

```sh
go build -o panda ./cmd/panda
PANDA_DATA_DIR=./data PANDA_COOKIE_SECURE=false ./panda serve
```

PowerShell equivalent:

```powershell
go build -o panda.exe .\cmd\panda
$env:PANDA_DATA_DIR = ".\data"
$env:PANDA_COOKIE_SECURE = "false"
.\panda.exe serve
```

Use `panda help` for CLI commands. To validate changes:

```sh
go test ./...
go vet ./...
```

See [the specification](docs/SPEC.md) and [task list](docs/TASKS.md).
