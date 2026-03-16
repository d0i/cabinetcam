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
