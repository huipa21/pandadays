# PandaDays

Self-hosted countdowns and anniversaries for meaningful days, with private
multi-user spaces, collections, tags, attachments, and read-only sharing.
English and Simplified Chinese are included. No self-registration, external
database, or frontend build step is needed.

## Install with Docker Compose

Build the image on your development laptop, then transfer it to the cloud
server. The server only runs the prebuilt image; it does not need Go, the
source tree, or enough memory to compile the application.

### Build on the laptop

Requirements: Git, PowerShell (Windows PowerShell 5.1 or PowerShell 7+),
Docker Desktop in Linux-container mode (or Docker Engine with Buildx, or
Podman), and a local Git checkout of this source tree. Check the **server's**
architecture with `uname -m`: use `linux/amd64` for `x86_64`, or `linux/arm64`
for `aarch64`. The commands below target an x86-64 Lighthouse server, even
if the laptop uses a different architecture.

Run from the project root in PowerShell:

```powershell
.\scripts\build.ps1
scp .\dist\pandadays-image.tar compose.yaml user@server:/opt/pandadays/
```

The script automatically detects Docker, then Podman; use `-Engine podman`
to select Podman explicitly. For an ARM server, use
`.\scripts\build.ps1 -Platform linux/arm64`. On macOS/Linux with PowerShell 7,
run `pwsh -File ./scripts/build.ps1`.

Replace `user@server` with your SSH destination and create `/opt/pandadays`
there first, with write access for that user. The existing multi-stage
Dockerfile builds the CGO-free binary and packages it with a non-root Alpine
runtime; no server-side build is required.

The script generates `YYYYMMDDTHHmmssZ-g<12-character-SHA>`
from the commit's **committer timestamp in UTC**, not the build time. The same
commit always produces the same version. It embeds that version in the binary
(including backup manifests), tags the image as both `localhost/pandadays:<version>` and
`localhost/pandadays:latest`, and saves **both tags** in `dist/pandadays-image.tar`.
No manual version or tag is needed. Compose runs `latest` by default.

Builds require a commit and a clean working tree, including untracked files.
Commit your changes before building. For an intentional development build,
set `$env:ALLOW_DIRTY = "1"`; its generated version gets a `.dirty`
suffix. Remove that override afterward with `Remove-Item Env:ALLOW_DIRTY`.
No Git metadata or Git failure ever falls back to a made-up version.

### Run on the server

Requirements: Docker Engine with the Compose v2 plugin. In `/opt/pandadays`,
load the transferred image and configure the deployment:

```sh
docker load --input pandadays-image.tar
mkdir -p data
sudo chown 10001:10001 data
sudo chmod 750 data
```

Edit `compose.yaml` for the server, especially `BASE_URL`, and connect your
reverse proxy to the service's Docker network as described below. All deployment configuration lives
in this file; no `.env` file is needed. Preserve your edits on updates.
Then start the service:

```sh
docker compose up -d --no-build
docker compose ps
docker compose logs pandadays
```

Compose uses `localhost/pandadays:latest` with `pull_policy: never`:
the image must already be loaded locally, and a missing image is an error.
For a local laptop deployment, skip the transfer and load steps after building.
Open `/setup` at your configured public HTTPS URL. Compose exposes port 8080
to containers on its network but does not publish a host port. Configure the
reverse proxy as described below before accessing the service.

### First-run setup and accounts

1. Find the one-time setup token in `docker compose logs pandadays`.
2. Enter it at `/setup`, choose an admin username (for example, `admin`), email,
   and password. Language follows your browser by default; you can choose
   English or Chinese. Timezone is detected from your browser and can be edited
   before submitting. Both preferences can be changed later in **Settings**.
   Setup signs you in automatically.
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

Edit [`compose.yaml`](compose.yaml) directly before starting; do not commit
deployment secrets. After editing, use `docker compose up -d` to recreate the service with the new
configuration (a restart alone does not reload environment changes).

| Variable | Binary/image default | Compose default / notes |
|---|---|---|
| `BASE_URL` | Empty | `https://days.example.com`; replace with the actual public address users visit, not the internal proxy upstream. |
| `COOKIE_SECURE` | `true` | `true` for HTTPS; use `false` only for plain-HTTP local development. |

The image sets `ADDR=:8080` (listen on all container interfaces on port 8080)
and `DATA_DIR=/data` (the container's data directory). These stay fixed in the
container and are not repeated in Compose. The host `./data` folder is mounted
at `/data`. Native, non-container runs can override `ADDR` and `DATA_DIR`.

Language and timezone need no environment variables. Language follows the
browser's `Accept-Language` header (falling back to English) unless a user or
admin site preference overrides it. The browser timezone is detected during
setup and, for other users without a saved timezone, after login. Users can
change either preference in **Settings**. `TZ` is optional for the server's
fallback timezone only (UTC in the image); it does not set a user's timezone.
Timezone data is embedded.

The `expose: ["8080"]` entry documents the container port; it does not publish
it on the host or restrict access by other containers on the same network.
A containerized reverse proxy must share that network and connect to
`pandadays:8080`. Set `BASE_URL` to the public HTTPS address users visit,
including a port only when it is part of that public address.

Existing deployments must rename the old prefixed environment variables;
they are no longer read. There is no language environment override.

The build script supplies the Dockerfile's required `VERSION` argument
automatically. Setting an environment variable on the server does not change
the binary version. Use `docker compose exec pandadays panda version` to see
the deployed version.

The image runs as UID/GID `10001:10001`. Pre-create the host `./data` directory
and grant this UID/GID read/write access as shown above; the image does not
start as root to repair bind-mount permissions. Keep the container data path
and port unchanged.

If migrating an existing named-volume deployment, stop the service and copy
or restore its data into `./data` before changing the mount. An empty bind
mount does not automatically import the old volume.

### Storage, health, and updates

The host `./data` folder contains `pandadays.db`, SQLite sidecars,
`media/`, `backups/`, and `tmp/`. Keep it on local storage suitable for SQLite,
not a shared network filesystem. Both `docker compose down` and
`docker compose down -v` preserve this bind-mounted folder. Back it up before
changing or removing it.

`GET /healthz` is unauthenticated. The image and `compose.yaml` probe it every
30 seconds. The build script uses Docker image format, which retains the
Dockerfile's `HEALTHCHECK`. OCI image format does not retain that instruction;
the explicit Compose health check also works if you build an OCI-format image
separately. Neither check publishes a port on the host. The image runs
the static, CGO-free Go binary as a non-root user. Health checks return 503
during restore maintenance; this is expected. Docker's restart policy handles
process exits, not an unhealthy status by itself.

To update, first download a site backup. Update the source checkout **on the
laptop**, rebuild and save the image using the commands above, and transfer
the archive to the server. On the server, load it and recreate the service:

```sh
docker load --input pandadays-image.tar
docker compose up -d --no-build --force-recreate
docker compose logs --tail=100 pandadays
```

Leave `image: localhost/pandadays:latest` for updates without editing tags.
`--force-recreate` ensures an update when reusing `latest`. Both tags are loaded,
so you can optionally pin `image` to `localhost/pandadays:<version>` for rollback
(subject to schema compatibility). Keep the same deployment directory so
Compose reuses the existing `./data` folder.

Migrations run automatically at startup. Do not assume an older binary can
read a newer schema; keep the previous source/image and a pre-upgrade backup.
The service handles termination gracefully. Never stop or upgrade it during
a restore.

## Backups and restore

### Recommended: admin Web UI

Admins manage the site only; use a regular user account for personal days.
The admin navigation contains Users, Backup/Restore, and Site Settings.
Users has a username/email search, a separate Create user form, and per-user
management pages. Site Settings controls keep-login days, default quotas
(storage in M, where 1 M = 1,048,576 bytes), and backup retention.
The avatar/username menu opens Profile or Sign out; change your password
from Profile.

Use **Create backup** on `/admin/backup` to create a retained full-site
`.tar.gz` archive and return to the backup list. Set retention above zero in
Site Settings first. Each backup's three-dot menu offers **Download**,
**Restore**, and confirmed **Delete**; ordinary retention pruning excludes
pre-restore recovery snapshots. Keep an off-host copy using Download.

To restore, choose Restore from a retained backup's menu, or use the top
Restore button to upload an archive. Both require re-entering your current
admin password.
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
transfer the deployment files and load the prebuilt image as described above,
then run `docker compose create --no-build pandadays` (do not start it before
restoring). Prepare the bind-mounted `./data` directory as above.
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

Terminate TLS at a trusted reverse proxy. Set these under `environment` in
`compose.yaml`:

```yaml
BASE_URL: https://days.example.com
COOKIE_SECURE: "true"
```

For Caddy in the same Compose project, use this upstream in its Caddyfile:

```caddyfile
days.example.com {
    reverse_proxy pandadays:8080
}
```

Replace `days.example.com` with your domain. Both services must share a Docker
network. Services in the same Compose project use its default network unless
configured otherwise. If Caddy is in another Compose project, attach both
services to the same external network. Publish only the proxy's HTTP/HTTPS
ports; PandaDays needs no host port.

Ready-to-adapt **host-installed** proxy configurations are in
[`deploy/nginx.conf`](deploy/nginx.conf) and [`deploy/Caddyfile`](deploy/Caddyfile).
These use `127.0.0.1:8080` and require adding a loopback host mapping to the
PandaDays service (the default Compose file intentionally has none):

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

You can use another host port, such as `127.0.0.1:9000:8080`, and update the
host proxy's upstream accordingly. The container port remains 8080.

For Nginx, install the config in your distribution's site configuration
directory and provide a certificate at the paths referenced in the file (for
example, with Certbot). For Caddy, install the Caddyfile and ensure DNS points
to the server and inbound ports 80 and 443 are open; Caddy obtains and renews
HTTPS certificates automatically.

`localhost` inside a proxy container is not the PandaDays container. Use the
service name as above, and avoid publishing the application directly to the
Internet.

The Nginx example sets `client_max_body_size 0`, which allows unlimited request
bodies. Caddy also has no request-body size limit by default; the Caddyfile
intentionally does not set `request_body max_size`. These settings allow large
full-site restore uploads. Choose a sufficiently large explicit limit if your
policy requires one, and check any other proxy/CDN limits as well. Nginx proxy
timeouts are set to one hour for long restore operations; adjust them as
needed. Application attachment and import quotas still apply.

HTTPS is also required for browser microphone recording outside localhost.

## Develop or run without Docker

Use Go 1.23 or newer (see `go.mod` for the module requirement). The Docker
builder uses Go 1.26. Templates, vendored htmx, translations, migrations, and
timezone data are embedded; no Node.js, CGO, or external SQLite is needed.

```sh
go build -o panda ./cmd/panda
DATA_DIR=./data COOKIE_SECURE=false ./panda serve
```

PowerShell equivalent:

```powershell
go build -o panda.exe .\cmd\panda
$env:DATA_DIR = ".\data"
$env:COOKIE_SECURE = "false"
.\panda.exe serve
```

Use `panda help` for CLI commands. To validate changes:

```sh
go test ./...
go vet ./...
```

Web tests use independent temporary storage, never the default runtime data
directory. Native Windows runs default to `\data` on the current drive when
`DATA_DIR` is unset; the PowerShell example above explicitly uses the project's
local data directory. Keep using the directory containing your existing database
and media when restarting; changing `DATA_DIR` does not move existing data.

If an attachment reports that its stored file is missing, its database entry alone
cannot restore it. Restore the file from backup, or remove the stale attachment
and upload the original file or record it again. Copying the same missing item
from the media library does not recover its content. File access errors are
logged server-side and shown as localized diagnostics in audio players.

Test automatic container versioning and build command wiring without a running
container engine (PowerShell):

```powershell
.\scripts\build_test.ps1
```

See [the specification](docs/SPEC.md) and [task list](docs/TASKS.md).
