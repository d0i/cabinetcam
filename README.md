# CabinetCam

A mobile-first web app for photographing and tracking the contents of cabinets, boxes, and storage containers. Take photos of what's inside, attach them to named boxes, and find things later.

Designed to be used with NFC tags — stick a tag on each box, tap your phone to it, and instantly see (or update) what's inside.

## Features

- **Box management** — Create, rename, archive, and delete boxes
- **Photo capture** — Take photos directly from phone camera or browse files
- **Client-side image resizing** — Photos are resized to 1600px max before upload for fast, reliable transfers
- **Exterior photos** — Photograph the outside of a box for easy identification
- **Memos** — Free-text notes per box, auto-saved as you type
- **Smart photo thinning** — Logarithmic time-warping algorithm automatically removes redundant photos when the limit is reached, keeping a good spread of old and recent shots
- **Camera roll** — Browse all photos across non-archived boxes, with search
- **NFC tag writing** — Write box URLs to NFC tags via Web NFC API (Chrome on Android)
- **Archive/restore** — Archive boxes you're done with; photos are excluded from the camera roll
- **Per-box settings** — Override max photos and protect-recent counts, or inherit app defaults
- **Lightbox viewer** — Tap any photo to view full-size with navigation and delete
- **Export/Import** — Download all data as a ZIP file, or restore from a previous export with optional overwrite
- **Annotation API** — REST API for external clients to annotate box contents from photos, with smart queue prioritization
- **API token authentication** — Bearer tokens for external clients, managed via settings page; exe.dev proxy auth for browser access
- **Ollama integration** — Local annotation client sends photos to Ollama vision models (llava, etc.) and posts results back
- **Mock Ollama server** — Test the annotation pipeline without a real LLM; generates deterministic fake annotations from image hashes

## Tech Stack

- **Backend**: Go (`net/http` router, no framework)
- **Database**: SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- **Frontend**: Server-rendered Go `html/template`, vanilla JS, no build step
- **Deployment**: Single binary + systemd service

## Project Structure

```
cmd/srv/main.go          Entry point — parses flags, starts server
srv/server.go            HTTP handlers, DB helpers, photo thinning algorithm
srv/templates/           Go HTML templates
  home.html              Home page — active boxes list, create box
  box.html               Box detail — photos, exterior, memo, settings, NFC
  archived.html          Archived boxes list
  roll.html              Camera roll — all photos across boxes
  settings.html          App-wide default settings
  annotate.html          Annotation mockup client
srv/static/
  style.css              Shared styles (home page uses this)
  script.js              Shared scripts (minimal)
db/db.go                 Database open + migration runner
db/migrations/
  001-base.sql           boxes, photos, migrations tables
  002-exterior-and-defaults.sql  exterior_filename, app_settings table
  003-annotation.sql     annotation, annotation_photo_id, annotation_at columns
  004-api-tokens.sql     api_tokens table for bearer token auth
tools/
  mock-ollama/main.go    Mock Ollama API server for testing
  annotate-client/main.go  Mac client for Ollama-based annotation
uploads/                 Photo storage (gitignored)
db.sqlite3               SQLite database (gitignored)
srv.service              systemd unit file
```

## Building and Running

```bash
# Build
make build

# Run directly
./cabinetcam -listen :8000

# Or run as a systemd service
sudo cp srv.service /etc/systemd/system/srv.service
sudo systemctl daemon-reload
sudo systemctl enable --now srv

# Check status / logs
systemctl status srv
journalctl -u srv -f

# Restart after code changes
make build && sudo systemctl restart srv
```

## API Endpoints

### Pages
| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Home — active boxes |
| GET | `/box/{id}` | Box detail page |
| GET | `/archived` | Archived boxes |
| GET | `/roll` | Camera roll |
| GET | `/settings` | App settings page |

### REST API
| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/boxes` | Create a new box |
| PUT | `/api/boxes/{id}` | Update box name/memo/settings |
| POST | `/api/boxes/{id}/photos` | Upload a content photo |
| POST | `/api/boxes/{id}/exterior` | Upload/replace exterior photo |
| DELETE | `/api/boxes/{id}` | Delete box and all photos |
| POST | `/api/boxes/{id}/archive` | Archive a box |
| POST | `/api/boxes/{id}/restore` | Restore an archived box |
| DELETE | `/api/photos/{id}` | Delete a single photo |
| GET | `/api/roll?q=search` | Get all photos as JSON |
| GET | `/api/settings` | Get app settings |
| PUT | `/api/settings` | Update app settings |
| GET | `/api/export` | Download ZIP of all data |
| POST | `/api/import` | Upload ZIP to import (form: `file`, `overwrite`) |
| GET | `/api/annotate/next` | Get next box needing annotation + photo ¹ |
| POST | `/api/annotate/{id}` | Submit annotation text for a box ¹ |
| POST | `/api/tokens` | Create an API token ² |
| GET | `/api/tokens` | List tokens (prefix only) ² |
| DELETE | `/api/tokens/{prefix}` | Revoke a token by prefix ² |

¹ Requires `Authorization: Bearer <token>` header or exe.dev proxy auth ² Requires exe.dev proxy auth (`X-ExeDev-Email` header)

## Photo Thinning Algorithm

When a box exceeds its max photo count, the thinning algorithm removes the least valuable photo:

1. Map each photo's age to **warped position**: `W = ln(age_seconds + 1)`
2. For each non-protected photo (excluding the oldest and the M most recent), compute the gap between its neighbors in warped space
3. Delete the photo whose neighbors are **closest together** — i.e., it contributes the least unique temporal coverage

This naturally preserves a logarithmic spread: many recent photos and fewer old ones, matching how memory works.

## Configuration

**App-wide defaults** (at `/settings`):
- `default_max_photos` — Max photos per box (default: 32)
- `default_protect_recent` — Number of newest photos immune to thinning (default: 3)

**Per-box overrides** (on box detail page):
- Set to "Custom" to override, or "App default" to inherit

## Export/Import

The export produces a self-contained ZIP file:

```
cabinetcam_export_20260316_123456.zip
├── manifest.json          # all box/photo metadata + app settings
├── photos/                # content photos
│   ├── abc123.jpg
│   └── ...
└── exteriors/             # exterior photos
    └── ext_xyz789.jpg
```

Import reads this ZIP and restores all boxes and photos. If a box with the same ID already exists, it can be skipped (default) or overwritten (optional checkbox).

## Annotation API

The annotation system enables external clients (e.g., a local MacBook app with vision AI) to annotate box contents by examining photos. Annotations are per-box, not per-photo.

### Selection Algorithm

`GET /api/annotate/next` picks the next box to annotate:

1. **Priority 1: No annotation** — Boxes with photos but no annotation. Ordered by photo count (descending), then oldest `updated_at`.
2. **Priority 2: Stale annotation** — Boxes where photos were added after the last annotation. Ordered by count of new photos (descending), then oldest `annotation_at`.
3. **204 No Content** — All boxes are up-to-date.

Archived boxes and boxes with zero photos are always skipped.

### GET /api/annotate/next

Returns:
```json
{
  "box_id": "f8fe2e1bc824a894",
  "box_name": "Kitchen Cabinet #1",
  "photo_id": "a36674704d2c4d3c",
  "photo_url": "/uploads/a36674704d2c4d3c.jpg",
  "current_annotation": "",
  "photo_count": 9,
  "photos_since_annotation": 0,
  "reason": "no_annotation"
}
```

Or `204 No Content` if all boxes are annotated.

### POST /api/annotate/{box_id}

Request:
```json
{
  "annotation": "Plates, bowls, 3 coffee mugs",
  "photo_id": "a36674704d2c4d3c"
}
```

The `photo_id` must belong to the box (prevents stale submissions). Returns `{"status":"ok","box_id":"..."}`.

### Example workflow (curl)

```bash
# Get next box to annotate
curl -s http://localhost:8000/api/annotate/next | jq .

# Download the photo for inspection
curl -s http://localhost:8000/uploads/a36674704d2c4d3c.jpg -o photo.jpg

# Submit annotation
curl -X POST http://localhost:8000/api/annotate/f8fe2e1bc824a894 \
  -H 'Content-Type: application/json' \
  -d '{"annotation":"Nescafe coffee, mugs, bowls","photo_id":"a36674704d2c4d3c"}'
```

A mockup annotation client is available at `/annotate` for testing.

## Authentication

CabinetCam uses two layers of authentication:

1. **exe.dev proxy auth** — The site is private by default. Accessing `https://stone-finder.exe.xyz:8000/` requires logging into exe.dev. The proxy injects `X-ExeDev-Email` and `X-ExeDev-UserID` headers. Browser-based access (including the annotation mockup page) uses this.

2. **Bearer tokens** — For external clients (like the Mac annotation client) that can't go through the exe.dev browser login flow. Tokens are managed at `/settings` (API Tokens section) or via the token API:

```bash
# Create a token (must be done through exe.dev proxy, or simulate the header)
curl -X POST https://stone-finder.exe.xyz:8000/api/tokens \
  -H 'Content-Type: application/json' \
  -d '{"name":"my-macbook"}'
# Returns: {"token":"abc123...","name":"my-macbook"}

# Use the token
curl https://stone-finder.exe.xyz:8000/api/annotate/next \
  -H 'Authorization: Bearer abc123...'
```

Token management (`POST/GET/DELETE /api/tokens`) requires exe.dev proxy auth. Annotation endpoints accept either method.

## Mac Annotation Client

The annotation client (`tools/annotate-client/`) runs on your Mac and automates the annotation workflow:

1. Fetches the next unannotated box from CabinetCam
2. Downloads the representative photo
3. Sends it to a local Ollama vision model for description
4. Posts the annotation back to the server

### Setup

```bash
# Cross-compile for Mac (from the server)
make client-mac
# Then copy to your Mac:
scp exedev@stone-finder.exe.xyz:cabinetcam/tools/annotate-client/annotate-client-darwin-arm64 ./annotate-client

# Or build locally on Mac if you have Go:
go build -o annotate-client ./tools/annotate-client

# Ensure Ollama is running with a vision model:
ollama pull llava
ollama serve  # if not already running
```

### Usage

```bash
# Create a token first (via browser at /settings, or curl through exe.dev proxy)

# Annotate one box:
./annotate-client \
  -server https://stone-finder.exe.xyz:8000 \
  -token <your-token> \
  -model llava

# Annotate all boxes in a loop:
./annotate-client \
  -server https://stone-finder.exe.xyz:8000 \
  -token <your-token> \
  -model llava \
  -loop

# Preview without submitting:
./annotate-client \
  -server https://stone-finder.exe.xyz:8000 \
  -token <your-token> \
  -model llava \
  -dry-run

# Custom prompt:
./annotate-client \
  -server https://stone-finder.exe.xyz:8000 \
  -token <your-token> \
  -model llava \
  -prompt "List every item visible in this cabinet photo. Be specific."
```

### Mock Ollama Server

For testing without a real Ollama installation:

```bash
# Build and run the mock
make mock-ollama
./tools/mock-ollama/mock-ollama  # listens on :11434

# In another terminal, run the client against localhost
./tools/annotate-client/annotate-client \
  -server http://localhost:8000 \
  -token <token> \
  -ollama http://127.0.0.1:11434 \
  -loop
```

The mock generates deterministic fake annotations from image SHA-256 hashes.

## Design

- Material Blue (`#1976d2`) accent color
- Card-based layout, max-width 600px
- Mobile-first, system font stack
- Emoji icons throughout
