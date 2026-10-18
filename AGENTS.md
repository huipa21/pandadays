# Agent Guide — PandaDays

Spec: [docs/SPEC.md](docs/SPEC.md). Tasks: [docs/TASKS.md](docs/TASKS.md). Implement one task at a time and tick its checkbox when done.

## Conventions
- Go 1.23+, stdlib first. Allowed deps: `modernc.org/sqlite`, `golang.org/x/crypto`. Ask before adding others.
- No CGO. No frontend build step; htmx vendored in `web/static`; templates and static files embedded.
- Layout:
  - `cmd/panda/` — entrypoint and CLI subcommands
  - `internal/config`, `internal/db`, `internal/i18n`, `internal/auth`, `internal/days`, `internal/media`, `internal/share`, `internal/portability`, `internal/backup`, `internal/web` (handlers, middleware)
  - `web/templates`, `web/static`, `locales/`, `migrations/`
- Schema changes go in a new numbered migration; never edit an applied one.
- Every data query is scoped by `owner_id` or goes through the share access resolver. No access → 404.
- All user-facing strings go through i18n; add keys to both `en.json` and `zh.json`.
- Date logic must take `now` and `*time.Location` as parameters (no `time.Now()` inside domain code).
- Use `log/slog`. Never log passwords, tokens, or session IDs (setup token printed once by design).
- Mobile-first CSS; test at 375px and 1280px.

## Commands
- Run: `go run ./cmd/panda serve` (set `DATA_DIR=./data COOKIE_SECURE=false` locally)
- Test: `go test ./...` (run the package you changed first)
- Lint: `go vet ./...`

## Definition of done (per task)
- Acceptance criteria in TASKS.md met.
- Tests added for business rules and authorization.
- `go vet ./...` and `go test ./...` pass.
