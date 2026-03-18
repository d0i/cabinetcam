# Agent Instructions

This is CabinetCam, a Go web app for photographing and tracking box/cabinet contents.

See README.md for architecture, API endpoints, and the photo thinning algorithm.

## Key conventions

- Go module: `srv.exe.dev`
- Entry point: `cmd/srv/main.go`
- All HTTP handlers and business logic: `srv/server.go`
- Templates are self-contained HTML files with inline CSS/JS (no shared layout template)
- Database migrations in `db/migrations/` are numbered `NNN-name.sql` and auto-applied on startup
- Box and photo IDs are 16-char hex strings
- Photos are resized client-side (max 1600px, 85% JPEG quality) before upload
- The `uploads/` directory and `db.sqlite3` are gitignored
- Systemd service: `srv.service` — binary is `cabinetcam`, listens on `:8000`
- Annotation API: `GET /api/annotate/next` + `POST /api/annotate/{id}` for external annotation clients
- Boxes track annotation text, the photo used for annotation, and the annotation timestamp
- Annotation selection: unannotated boxes first (by photo count desc), then stale annotations (by new photo count desc)
- Global `requireAuth` middleware wraps entire mux: redirects unauthenticated browsers to `/__exe.dev/login`, returns 401 JSON for unauthenticated API calls; passes through `/api/annotate/*` (own token auth), `/static/*`, `/uploads/*`
- API auth: Bearer tokens (`Authorization: Bearer <token>`) or exe.dev proxy auth (`X-ExeDev-Email` header)
- Token management: `POST/GET/DELETE /api/tokens` (requires exe.dev proxy auth)
- Site must be made public via `ssh exe.dev share set-public cabinetcam` for external bearer-token clients to connect
- Mac annotation client: `tools/annotate-client/` — fetches photo → Ollama vision → posts annotation
- Mock Ollama server: `tools/mock-ollama/` — deterministic fake annotations from image hashes, port 11434
- Tags: comma-separated in `boxes.tags` column; `TagList()` method returns `[]string`
- Home page search: client-side AND matching across name, memo, tags, and annotation
- Makefile targets: `build`, `clean`, `test`, `mock-ollama`, `client`, `client-mac`
