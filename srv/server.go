package srv

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"srv.exe.dev/db"
)

type Server struct {
	DB           *sql.DB
	Hostname     string
	TemplatesDir string
	StaticDir    string
	UploadsDir   string
}

type AppSettings struct {
	DefaultMaxPhotos    int
	DefaultProtectRecent int
}

type Box struct {
	ID                string
	Name              string
	Memo              string
	Tags              string // comma-separated
	MaxPhotosRaw      int    // 0 = use app default
	ProtectRecentRaw  int    // 0 = use app default
	MaxPhotos         int    // resolved (after applying defaults)
	ProtectRecent     int    // resolved
	ExteriorFilename  string
	Annotation        string
	AnnotationPhotoID string
	AnnotationAt      *time.Time
	Archived          bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
	PhotoCount        int
}

// TagList returns tags as a slice, filtering empty strings.
func (b Box) TagList() []string {
	if b.Tags == "" {
		return nil
	}
	var result []string
	for _, t := range strings.Split(b.Tags, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			result = append(result, t)
		}
	}
	return result
}

type Photo struct {
	ID         string
	BoxID      string
	Filename   string
	CapturedAt time.Time
	CreatedAt  time.Time
	BoxName    string
}

func New(dbPath, hostname string) (*Server, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	baseDir := filepath.Dir(thisFile)
	srv := &Server{
		Hostname:     hostname,
		TemplatesDir: filepath.Join(baseDir, "templates"),
		StaticDir:    filepath.Join(baseDir, "static"),
		UploadsDir:   filepath.Join(baseDir, "..", "uploads"),
	}
	if err := os.MkdirAll(srv.UploadsDir, 0755); err != nil {
		return nil, fmt.Errorf("create uploads dir: %w", err)
	}
	if err := srv.setUpDatabase(dbPath); err != nil {
		return nil, err
	}
	return srv, nil
}

func (s *Server) setUpDatabase(dbPath string) error {
	wdb, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	s.DB = wdb
	if err := db.RunMigrations(wdb); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	return nil
}

func (s *Server) Serve(addr string) error {
	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("GET /box/{id}", s.handleBoxDetail)
	mux.HandleFunc("GET /archived", s.handleArchived)
	mux.HandleFunc("GET /roll", s.handleCameraRoll)

	// API
	mux.HandleFunc("POST /api/boxes", s.handleCreateBox)
	mux.HandleFunc("PUT /api/boxes/{id}", s.handleUpdateBox)
	mux.HandleFunc("POST /api/boxes/{id}/archive", s.handleArchiveBox)
	mux.HandleFunc("POST /api/boxes/{id}/restore", s.handleRestoreBox)
	mux.HandleFunc("DELETE /api/boxes/{id}", s.handleDeleteBox)
	mux.HandleFunc("POST /api/boxes/{id}/photos", s.handleUploadPhoto)
	mux.HandleFunc("POST /api/boxes/{id}/exterior", s.handleUploadExterior)
	mux.HandleFunc("DELETE /api/photos/{id}", s.handleDeletePhoto)
	mux.HandleFunc("GET /api/roll", s.handleAPIRoll)
	mux.HandleFunc("GET /api/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/settings", s.handleUpdateSettings)
	mux.HandleFunc("GET /api/export", s.handleExport)
	mux.HandleFunc("POST /api/import", s.handleImport)
	mux.HandleFunc("GET /api/annotate/next", s.requireToken(s.handleAnnotateNext))
	mux.HandleFunc("POST /api/annotate/{id}", s.requireToken(s.handleAnnotateSubmit))
	mux.HandleFunc("GET /annotate", s.handleAnnotatePage)
	mux.HandleFunc("GET /settings", s.handleSettingsPage)

	// Token management (requires exe.dev proxy auth)
	mux.HandleFunc("POST /api/tokens", s.requireExeDevAuth(s.handleCreateToken))
	mux.HandleFunc("GET /api/tokens", s.requireExeDevAuth(s.handleListTokens))
	mux.HandleFunc("DELETE /api/tokens/{token}", s.requireExeDevAuth(s.handleRevokeToken))

	// Static & uploads
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.StaticDir))))
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(s.UploadsDir))))

	slog.Info("starting server", "addr", addr)
	return http.ListenAndServe(addr, s.requireAuth(mux))
}

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Template rendering ---

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) error {
	path := filepath.Join(s.TemplatesDir, name)
	funcMap := template.FuncMap{
		"timeFormat": func(t time.Time) string {
			return t.Format("2006-01-02 15:04")
		},
		"isoFormat": func(t time.Time) string {
			return t.Format(time.RFC3339)
		},
	}
	tmpl, err := template.New(filepath.Base(path)).Funcs(funcMap).ParseFiles(path)
	if err != nil {
		return fmt.Errorf("parse template %q: %w", name, err)
	}
	if err := tmpl.Execute(w, data); err != nil {
		return fmt.Errorf("execute template %q: %w", name, err)
	}
	return nil
}

// --- Page handlers ---

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	boxes, err := s.listBoxes(false)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	settings := s.getAppSettings()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderTemplate(w, "home.html", map[string]any{"Boxes": boxes, "Settings": settings})
}

func (s *Server) handleBoxDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	box, err := s.getBox(id)
	if err != nil {
		http.Error(w, "Box not found", 404)
		return
	}
	photos, err := s.getBoxPhotos(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	settings := s.getAppSettings()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderTemplate(w, "box.html", map[string]any{"Box": box, "Photos": photos, "Settings": settings})
}

func (s *Server) handleArchived(w http.ResponseWriter, r *http.Request) {
	boxes, err := s.listBoxes(true)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderTemplate(w, "archived.html", map[string]any{"Boxes": boxes})
}

func (s *Server) handleCameraRoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderTemplate(w, "roll.html", nil)
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	settings := s.getAppSettings()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderTemplate(w, "settings.html", map[string]any{"Settings": settings})
}

// --- API handlers ---

func (s *Server) handleCreateBox(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		name = "New Box"
	}
	id := generateID()
	_, err := s.DB.Exec(
		"INSERT INTO boxes (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)",
		id, name, time.Now(), time.Now(),
	)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, "/box/"+id, http.StatusSeeOther)
}

func (s *Server) handleUpdateBox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var payload struct {
		Name          *string `json:"name"`
		Memo          *string `json:"memo"`
		Tags          *string `json:"tags"`
		MaxPhotos     *int    `json:"max_photos"`
		ProtectRecent *int    `json:"protect_recent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if payload.Name != nil {
		s.DB.Exec("UPDATE boxes SET name=?, updated_at=? WHERE id=?", *payload.Name, time.Now(), id)
	}
	if payload.Memo != nil {
		s.DB.Exec("UPDATE boxes SET memo=?, updated_at=? WHERE id=?", *payload.Memo, time.Now(), id)
	}
	if payload.Tags != nil {
		s.DB.Exec("UPDATE boxes SET tags=?, updated_at=? WHERE id=?", *payload.Tags, time.Now(), id)
	}
	if payload.MaxPhotos != nil && *payload.MaxPhotos >= 0 {
		s.DB.Exec("UPDATE boxes SET max_photos=?, updated_at=? WHERE id=?", *payload.MaxPhotos, time.Now(), id)
	}
	if payload.ProtectRecent != nil && *payload.ProtectRecent >= 0 {
		s.DB.Exec("UPDATE boxes SET protect_recent=?, updated_at=? WHERE id=?", *payload.ProtectRecent, time.Now(), id)
	}
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleArchiveBox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.DB.Exec("UPDATE boxes SET archived=1, updated_at=? WHERE id=?", time.Now(), id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleRestoreBox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.DB.Exec("UPDATE boxes SET archived=0, updated_at=? WHERE id=?", time.Now(), id)
	http.Redirect(w, r, "/archived", http.StatusSeeOther)
}

func (s *Server) handleDeleteBox(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Delete exterior
	box, _ := s.getBox(id)
	if box != nil && box.ExteriorFilename != "" {
		os.Remove(filepath.Join(s.UploadsDir, box.ExteriorFilename))
	}
	// Delete photo files
	photos, _ := s.getBoxPhotos(id)
	for _, p := range photos {
		os.Remove(filepath.Join(s.UploadsDir, p.Filename))
	}
	s.DB.Exec("DELETE FROM boxes WHERE id=?", id)
	w.WriteHeader(200)
}

func (s *Server) handleUploadPhoto(w http.ResponseWriter, r *http.Request) {
	boxID := r.PathValue("id")
	box, err := s.getBox(boxID)
	if err != nil {
		http.Error(w, "box not found", 404)
		return
	}

	r.ParseMultipartForm(32 << 20) // 32MB
	file, header, err := r.FormFile("photo")
	if err != nil {
		http.Error(w, "no file", 400)
		return
	}
	defer file.Close()

	// Determine extension
	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = ".jpg"
	}

	photoID := generateID()
	filename := photoID + ext
	dstPath := filepath.Join(s.UploadsDir, filename)

	dst, err := os.Create(dstPath)
	if err != nil {
		http.Error(w, "save failed", 500)
		return
	}
	defer dst.Close()
	io.Copy(dst, file)

	now := time.Now()
	_, err = s.DB.Exec(
		"INSERT INTO photos (id, box_id, filename, captured_at, created_at) VALUES (?, ?, ?, ?, ?)",
		photoID, boxID, filename, now, now,
	)
	if err != nil {
		os.Remove(dstPath)
		http.Error(w, "db error", 500)
		return
	}

	// Run thinning algorithm if needed
	s.thinPhotos(boxID, box.MaxPhotos, box.ProtectRecent)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": photoID, "filename": filename})
}

func (s *Server) handleUploadExterior(w http.ResponseWriter, r *http.Request) {
	boxID := r.PathValue("id")
	box, err := s.getBox(boxID)
	if err != nil {
		http.Error(w, "box not found", 404)
		return
	}

	r.ParseMultipartForm(32 << 20)
	file, header, err := r.FormFile("photo")
	if err != nil {
		http.Error(w, "no file", 400)
		return
	}
	defer file.Close()

	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = ".jpg"
	}

	filename := "ext_" + generateID() + ext
	dstPath := filepath.Join(s.UploadsDir, filename)

	dst, err := os.Create(dstPath)
	if err != nil {
		http.Error(w, "save failed", 500)
		return
	}
	defer dst.Close()
	io.Copy(dst, file)

	// Remove old exterior
	if box.ExteriorFilename != "" {
		os.Remove(filepath.Join(s.UploadsDir, box.ExteriorFilename))
	}

	s.DB.Exec("UPDATE boxes SET exterior_filename=?, updated_at=? WHERE id=?", filename, time.Now(), boxID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"filename": filename})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings := s.getAppSettings()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(settings)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		DefaultMaxPhotos     *int `json:"default_max_photos"`
		DefaultProtectRecent *int `json:"default_protect_recent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	if payload.DefaultMaxPhotos != nil && *payload.DefaultMaxPhotos >= 3 {
		s.DB.Exec("UPDATE app_settings SET default_max_photos=? WHERE id=1", *payload.DefaultMaxPhotos)
	}
	if payload.DefaultProtectRecent != nil && *payload.DefaultProtectRecent >= 1 {
		s.DB.Exec("UPDATE app_settings SET default_protect_recent=? WHERE id=1", *payload.DefaultProtectRecent)
	}
	w.WriteHeader(200)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleDeletePhoto(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var filename string
	err := s.DB.QueryRow("SELECT filename FROM photos WHERE id=?", id).Scan(&filename)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	s.DB.Exec("DELETE FROM photos WHERE id=?", id)
	os.Remove(filepath.Join(s.UploadsDir, filename))
	w.WriteHeader(200)
}

func (s *Server) handleAPIRoll(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	photos, err := s.getAllPhotos(query)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(photos)
}

// --- DB helpers ---

func (s *Server) getAppSettings() AppSettings {
	var settings AppSettings
	err := s.DB.QueryRow("SELECT default_max_photos, default_protect_recent FROM app_settings WHERE id=1").Scan(
		&settings.DefaultMaxPhotos, &settings.DefaultProtectRecent,
	)
	if err != nil {
		// Fallback defaults
		settings.DefaultMaxPhotos = 32
		settings.DefaultProtectRecent = 3
	}
	return settings
}

func (s *Server) resolveBox(b *Box) {
	settings := s.getAppSettings()
	b.MaxPhotosRaw = b.MaxPhotos
	b.ProtectRecentRaw = b.ProtectRecent
	if b.MaxPhotos == 0 {
		b.MaxPhotos = settings.DefaultMaxPhotos
	}
	if b.ProtectRecent == 0 {
		b.ProtectRecent = settings.DefaultProtectRecent
	}
}

func (s *Server) listBoxes(archived bool) ([]Box, error) {
	archVal := 0
	if archived {
		archVal = 1
	}
	rows, err := s.DB.Query(`
		SELECT b.id, b.name, b.memo, b.tags, b.max_photos, b.protect_recent, b.archived, b.created_at, b.updated_at,
		       b.exterior_filename, b.annotation, b.annotation_photo_id, b.annotation_at,
		       COALESCE((SELECT COUNT(*) FROM photos WHERE box_id=b.id), 0)
		FROM boxes b WHERE b.archived=? ORDER BY b.updated_at DESC`, archVal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var boxes []Box
	for rows.Next() {
		var b Box
		var arch int
		var annotationAt sql.NullTime
		err := rows.Scan(&b.ID, &b.Name, &b.Memo, &b.Tags, &b.MaxPhotos, &b.ProtectRecent, &arch, &b.CreatedAt, &b.UpdatedAt,
			&b.ExteriorFilename, &b.Annotation, &b.AnnotationPhotoID, &annotationAt, &b.PhotoCount)
		if err != nil {
			return nil, err
		}
		b.Archived = arch == 1
		if annotationAt.Valid {
			b.AnnotationAt = &annotationAt.Time
		}
		s.resolveBox(&b)
		boxes = append(boxes, b)
	}
	return boxes, nil
}

func (s *Server) getBox(id string) (*Box, error) {
	var b Box
	var arch int
	var annotationAt sql.NullTime
	err := s.DB.QueryRow(`
		SELECT b.id, b.name, b.memo, b.tags, b.max_photos, b.protect_recent, b.archived, b.created_at, b.updated_at,
		       b.exterior_filename, b.annotation, b.annotation_photo_id, b.annotation_at,
		       COALESCE((SELECT COUNT(*) FROM photos WHERE box_id=b.id), 0)
		FROM boxes b WHERE b.id=?`, id).Scan(
		&b.ID, &b.Name, &b.Memo, &b.Tags, &b.MaxPhotos, &b.ProtectRecent, &arch, &b.CreatedAt, &b.UpdatedAt,
		&b.ExteriorFilename, &b.Annotation, &b.AnnotationPhotoID, &annotationAt, &b.PhotoCount,
	)
	if err != nil {
		return nil, err
	}
	b.Archived = arch == 1
	if annotationAt.Valid {
		b.AnnotationAt = &annotationAt.Time
	}
	s.resolveBox(&b)
	return &b, nil
}

func (s *Server) getBoxPhotos(id string) ([]Photo, error) {
	rows, err := s.DB.Query(
		"SELECT id, box_id, filename, captured_at, created_at FROM photos WHERE box_id=? ORDER BY captured_at DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var photos []Photo
	for rows.Next() {
		var p Photo
		rows.Scan(&p.ID, &p.BoxID, &p.Filename, &p.CapturedAt, &p.CreatedAt)
		photos = append(photos, p)
	}
	return photos, nil
}

func (s *Server) getAllPhotos(query string) ([]Photo, error) {
	var rows *sql.Rows
	var err error
	if query == "" {
		rows, err = s.DB.Query(`
			SELECT p.id, p.box_id, p.filename, p.captured_at, p.created_at, b.name
			FROM photos p JOIN boxes b ON p.box_id=b.id
			WHERE b.archived=0
			ORDER BY p.captured_at DESC`)
	} else {
		like := "%" + query + "%"
		rows, err = s.DB.Query(`
			SELECT p.id, p.box_id, p.filename, p.captured_at, p.created_at, b.name
			FROM photos p JOIN boxes b ON p.box_id=b.id
			WHERE b.archived=0 AND (b.name LIKE ? OR b.memo LIKE ?)
			ORDER BY p.captured_at DESC`, like, like)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var photos []Photo
	for rows.Next() {
		var p Photo
		rows.Scan(&p.ID, &p.BoxID, &p.Filename, &p.CapturedAt, &p.CreatedAt, &p.BoxName)
		photos = append(photos, p)
	}
	return photos, nil
}

// --- Photo thinning algorithm ---

func (s *Server) thinPhotos(boxID string, maxPhotos, protectRecent int) {
	// Get photos sorted ascending by captured_at
	rows, err := s.DB.Query(
		"SELECT id, filename, captured_at FROM photos WHERE box_id=? ORDER BY captured_at ASC", boxID)
	if err != nil {
		return
	}
	defer rows.Close()

	type photoEntry struct {
		id       string
		filename string
		time     time.Time
	}
	var photos []photoEntry
	for rows.Next() {
		var p photoEntry
		rows.Scan(&p.id, &p.filename, &p.time)
		photos = append(photos, p)
	}

	for len(photos) > maxPhotos {
		if len(photos) < protectRecent+2 {
			break
		}

		now := time.Now()
		warpedPositions := make([]float64, len(photos))
		for i, p := range photos {
			age := now.Sub(p.time).Seconds()
			warpedPositions[i] = math.Log(age + 1.0)
		}

		minGap := math.Inf(1)
		victimIdx := -1
		stopIndex := len(photos) - protectRecent - 1

		for i := 1; i <= stopIndex; i++ {
			gap := warpedPositions[i-1] - warpedPositions[i+1]
			if gap < minGap {
				minGap = gap
				victimIdx = i
			}
		}

		if victimIdx < 0 {
			break
		}

		victim := photos[victimIdx]
		s.DB.Exec("DELETE FROM photos WHERE id=?", victim.id)
		os.Remove(filepath.Join(s.UploadsDir, victim.filename))
		photos = append(photos[:victimIdx], photos[victimIdx+1:]...)
	}
}

// --- Auth middleware ---

// requireAuth wraps the entire mux and protects all routes from unauthenticated
// public access. Routes under /api/annotate/ are exempt because they enforce
// their own Bearer token auth via requireToken. Static assets and uploads are
// also allowed through so that bearer-token clients can download photos.
// All other routes require an authenticated exe.dev session (X-ExeDev-Email header);
// unauthenticated browsers are redirected to the exe.dev login page.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Annotation API endpoints handle their own Bearer token auth
		if strings.HasPrefix(r.URL.Path, "/api/annotate/") {
			next.ServeHTTP(w, r)
			return
		}

		// Static assets and uploads — allow through so bearer-token clients
		// (annotation client) can download photos referenced in API responses
		if strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/uploads/") {
			next.ServeHTTP(w, r)
			return
		}

		// Authenticated exe.dev users pass through
		if r.Header.Get("X-ExeDev-Email") != "" {
			next.ServeHTTP(w, r)
			return
		}

		// API requests get a JSON 401; browsers get redirected to login
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "authentication required; use exe.dev login or Bearer token"})
			return
		}

		// Redirect unauthenticated browsers to exe.dev login
		redirectURL := "/__exe.dev/login?redirect=" + r.URL.RequestURI()
		http.Redirect(w, r, redirectURL, http.StatusFound)
	})
}

// requireToken checks for a valid Bearer token in the Authorization header.
// The annotation API endpoints use this for external client authentication.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Allow requests from exe.dev proxy (already authenticated via X-ExeDev-Email)
		if r.Header.Get("X-ExeDev-Email") != "" {
			next(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "missing or invalid Authorization header; use Bearer <token>"})
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		var name string
		var expiresAt time.Time
		err := s.DB.QueryRow("SELECT name, expires_at FROM api_tokens WHERE token=?", token).Scan(&name, &expiresAt)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid token"})
			return
		}

		if time.Now().After(expiresAt) {
			// Clean up expired token
			s.DB.Exec("DELETE FROM api_tokens WHERE token=?", token)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "token expired; create a new one at /settings"})
			return
		}

		// Update last_used_at
		s.DB.Exec("UPDATE api_tokens SET last_used_at=? WHERE token=?", time.Now(), token)

		next(w, r)
	}
}

// requireExeDevAuth ensures the request comes through the exe.dev proxy with authentication.
// Used for token management endpoints (create/list/revoke tokens).
func (s *Server) requireExeDevAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		email := r.Header.Get("X-ExeDev-Email")
		if email == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			json.NewEncoder(w).Encode(map[string]string{"error": "this endpoint requires exe.dev proxy authentication; access via https://stone-finder.exe.xyz:8000/"})
			return
		}
		slog.Info("exe.dev auth", "email", email, "path", r.URL.Path)
		next(w, r)
	}
}

// --- Token management ---

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		payload.Name = "unnamed"
	}
	if payload.Name == "" {
		payload.Name = "unnamed"
	}

	// Generate a 32-byte random token, hex-encoded (64 chars)
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)

	const tokenTTL = 24 * time.Hour
	now := time.Now()
	expiresAt := now.Add(tokenTTL)

	_, err := s.DB.Exec("INSERT INTO api_tokens (token, name, created_at, expires_at) VALUES (?, ?, ?, ?)",
		token, payload.Name, now, expiresAt)
	if err != nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to create token"})
		return
	}

	// Clean up any expired tokens while we're here
	s.DB.Exec("DELETE FROM api_tokens WHERE expires_at < ?", now)

	slog.Info("token created", "name", payload.Name, "expires", expiresAt.Format(time.RFC3339), "by", r.Header.Get("X-ExeDev-Email"))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token":      token,
		"name":       payload.Name,
		"expires_at": expiresAt.Format(time.RFC3339),
	})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	// Clean up expired tokens
	s.DB.Exec("DELETE FROM api_tokens WHERE expires_at < ?", time.Now())

	rows, err := s.DB.Query("SELECT token, name, created_at, last_used_at, expires_at FROM api_tokens ORDER BY created_at DESC")
	if err != nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to list tokens"})
		return
	}
	defer rows.Close()

	type tokenInfo struct {
		TokenPrefix string  `json:"token_prefix"`
		Name        string  `json:"name"`
		CreatedAt   string  `json:"created_at"`
		LastUsedAt  *string `json:"last_used_at"`
		ExpiresAt   string  `json:"expires_at"`
		Expired     bool    `json:"expired"`
	}
	var tokens []tokenInfo
	for rows.Next() {
		var t tokenInfo
		var fullToken string
		var lastUsed sql.NullTime
		var createdAt, expiresAt time.Time
		rows.Scan(&fullToken, &t.Name, &createdAt, &lastUsed, &expiresAt)
		// Show only first 8 chars of token for security
		if len(fullToken) >= 8 {
			t.TokenPrefix = fullToken[:8] + "..."
		}
		t.CreatedAt = createdAt.Format(time.RFC3339)
		if lastUsed.Valid {
			s := lastUsed.Time.Format(time.RFC3339)
			t.LastUsedAt = &s
		}
		t.ExpiresAt = expiresAt.Format(time.RFC3339)
		t.Expired = time.Now().After(expiresAt)
		tokens = append(tokens, t)
	}
	if tokens == nil {
		tokens = []tokenInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tokens)
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	tokenPrefix := r.PathValue("token")
	// Match by prefix (first 8+ chars)
	result, err := s.DB.Exec("DELETE FROM api_tokens WHERE token LIKE ?", tokenPrefix+"%")
	if err != nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": "failed to revoke token"})
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "token not found"})
		return
	}
	slog.Info("token revoked", "prefix", tokenPrefix, "by", r.Header.Get("X-ExeDev-Email"))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
}

func (s *Server) handleAnnotatePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.renderTemplate(w, "annotate.html", nil)
}

// --- Annotation API ---

type AnnotateNextResponse struct {
	BoxID                string `json:"box_id"`
	BoxName              string `json:"box_name"`
	PhotoID              string `json:"photo_id"`
	PhotoURL             string `json:"photo_url"`
	CurrentAnnotation    string `json:"current_annotation"`
	PhotoCount           int    `json:"photo_count"`
	PhotosSinceAnnotation int   `json:"photos_since_annotation"`
	Reason               string `json:"reason"`
}

func (s *Server) handleAnnotateNext(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Priority 1: Boxes with no annotation, that have photos, not archived.
	// Order by photo count DESC, then updated_at ASC.
	row := s.DB.QueryRow(`
		SELECT b.id, b.name, b.annotation,
		       COALESCE((SELECT COUNT(*) FROM photos WHERE box_id = b.id), 0) as photo_count,
		       COALESCE((SELECT id FROM photos WHERE box_id = b.id ORDER BY created_at DESC LIMIT 1), '') as newest_photo_id,
		       COALESCE((SELECT filename FROM photos WHERE box_id = b.id ORDER BY created_at DESC LIMIT 1), '') as newest_photo_filename
		FROM boxes b
		WHERE b.archived = 0
		  AND (b.annotation IS NULL OR b.annotation = '')
		  AND (SELECT COUNT(*) FROM photos WHERE box_id = b.id) > 0
		ORDER BY photo_count DESC, b.updated_at ASC
		LIMIT 1
	`)

	var boxID, boxName, annotation, newestPhotoID, newestFilename string
	var photoCount int
	err := row.Scan(&boxID, &boxName, &annotation, &photoCount, &newestPhotoID, &newestFilename)
	if err == nil {
		json.NewEncoder(w).Encode(AnnotateNextResponse{
			BoxID:                boxID,
			BoxName:              boxName,
			PhotoID:              newestPhotoID,
			PhotoURL:             "/uploads/" + newestFilename,
			CurrentAnnotation:    "",
			PhotoCount:           photoCount,
			PhotosSinceAnnotation: 0,
			Reason:               "no_annotation",
		})
		return
	}

	// Priority 2: Boxes with stale annotations (photos added after annotation_at).
	// Order by count of new photos DESC, then annotation_at ASC.
	row = s.DB.QueryRow(`
		SELECT b.id, b.name, b.annotation,
		       COALESCE((SELECT COUNT(*) FROM photos WHERE box_id = b.id), 0) as photo_count,
		       COALESCE((SELECT COUNT(*) FROM photos WHERE box_id = b.id AND created_at > b.annotation_at), 0) as new_photos,
		       COALESCE((SELECT id FROM photos WHERE box_id = b.id ORDER BY created_at DESC LIMIT 1), '') as newest_photo_id,
		       COALESCE((SELECT filename FROM photos WHERE box_id = b.id ORDER BY created_at DESC LIMIT 1), '') as newest_photo_filename
		FROM boxes b
		WHERE b.archived = 0
		  AND b.annotation IS NOT NULL AND b.annotation != ''
		  AND b.annotation_at IS NOT NULL
		  AND (SELECT COUNT(*) FROM photos WHERE box_id = b.id AND created_at > b.annotation_at) > 0
		ORDER BY new_photos DESC, b.annotation_at ASC
		LIMIT 1
	`)

	var newPhotos int
	err = row.Scan(&boxID, &boxName, &annotation, &photoCount, &newPhotos, &newestPhotoID, &newestFilename)
	if err == nil {
		json.NewEncoder(w).Encode(AnnotateNextResponse{
			BoxID:                boxID,
			BoxName:              boxName,
			PhotoID:              newestPhotoID,
			PhotoURL:             "/uploads/" + newestFilename,
			CurrentAnnotation:    annotation,
			PhotoCount:           photoCount,
			PhotosSinceAnnotation: newPhotos,
			Reason:               "stale_annotation",
		})
		return
	}

	// No boxes need annotation
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAnnotateSubmit(w http.ResponseWriter, r *http.Request) {
	boxID := r.PathValue("id")
	w.Header().Set("Content-Type", "application/json")

	// Check box exists and is not archived
	box, err := s.getBox(boxID)
	if err != nil || box.Archived {
		w.WriteHeader(404)
		json.NewEncoder(w).Encode(map[string]string{"error": "box not found"})
		return
	}

	var payload struct {
		Annotation string `json:"annotation"`
		PhotoID    string `json:"photo_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid JSON in request body"})
		return
	}

	annotation := strings.TrimSpace(payload.Annotation)
	if annotation == "" {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "annotation field is required and must be non-empty"})
		return
	}
	if payload.PhotoID == "" {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "photo_id field is required"})
		return
	}

	// Verify the photo belongs to this box
	var photoBoxID string
	err = s.DB.QueryRow("SELECT box_id FROM photos WHERE id=?", payload.PhotoID).Scan(&photoBoxID)
	if err != nil || photoBoxID != boxID {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]string{"error": "photo_id does not belong to this box"})
		return
	}

	// Save annotation
	now := time.Now()
	_, err = s.DB.Exec(
		"UPDATE boxes SET annotation=?, annotation_photo_id=?, annotation_at=?, updated_at=? WHERE id=?",
		annotation, payload.PhotoID, now, now, boxID,
	)
	if err != nil {
		w.WriteHeader(500)
		json.NewEncoder(w).Encode(map[string]string{"error": "database error"})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "box_id": boxID})
}

// --- Export/Import ---

type ExportManifest struct {
	Version     int               `json:"version"`
	ExportedAt  time.Time         `json:"exported_at"`
	AppSettings ExportAppSettings `json:"app_settings"`
	Boxes       []ExportBox       `json:"boxes"`
}

type ExportAppSettings struct {
	DefaultMaxPhotos     int `json:"default_max_photos"`
	DefaultProtectRecent int `json:"default_protect_recent"`
}

type ExportBox struct {
	ID                string        `json:"id"`
	Name              string        `json:"name"`
	Memo              string        `json:"memo"`
	Tags              string        `json:"tags"`
	MaxPhotos         int           `json:"max_photos"`
	ProtectRecent     int           `json:"protect_recent"`
	Archived          bool          `json:"archived"`
	ExteriorFilename  string        `json:"exterior_filename"`
	Annotation        string        `json:"annotation"`
	AnnotationPhotoID string        `json:"annotation_photo_id"`
	AnnotationAt      *time.Time    `json:"annotation_at"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	Photos            []ExportPhoto `json:"photos"`
}

type ExportPhoto struct {
	ID         string    `json:"id"`
	Filename   string    `json:"filename"`
	CapturedAt time.Time `json:"captured_at"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	appSettings := s.getAppSettings()

	activeBoxes, err := s.listBoxes(false)
	if err != nil {
		http.Error(w, "failed to list boxes", 500)
		return
	}
	archivedBoxes, err := s.listBoxes(true)
	if err != nil {
		http.Error(w, "failed to list boxes", 500)
		return
	}
	allBoxes := append(activeBoxes, archivedBoxes...)

	manifest := ExportManifest{
		Version:    1,
		ExportedAt: time.Now().UTC(),
		AppSettings: ExportAppSettings{
			DefaultMaxPhotos:     appSettings.DefaultMaxPhotos,
			DefaultProtectRecent: appSettings.DefaultProtectRecent,
		},
		Boxes: make([]ExportBox, 0, len(allBoxes)),
	}

	for _, box := range allBoxes {
		photos, err := s.getBoxPhotos(box.ID)
		if err != nil {
			http.Error(w, "failed to get photos", 500)
			return
		}
		exportPhotos := make([]ExportPhoto, len(photos))
		for i, p := range photos {
			exportPhotos[i] = ExportPhoto{
				ID: p.ID, Filename: p.Filename,
				CapturedAt: p.CapturedAt, CreatedAt: p.CreatedAt,
			}
		}
		manifest.Boxes = append(manifest.Boxes, ExportBox{
			ID: box.ID, Name: box.Name, Memo: box.Memo, Tags: box.Tags,
			MaxPhotos: box.MaxPhotosRaw, ProtectRecent: box.ProtectRecentRaw,
			Archived: box.Archived, ExteriorFilename: box.ExteriorFilename,
			Annotation: box.Annotation, AnnotationPhotoID: box.AnnotationPhotoID,
			AnnotationAt: box.AnnotationAt,
			CreatedAt: box.CreatedAt, UpdatedAt: box.UpdatedAt,
			Photos: exportPhotos,
		})
	}

	fname := fmt.Sprintf("cabinetcam_export_%s.zip", time.Now().Format("20060102_150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fname))

	zw := zip.NewWriter(w)
	defer zw.Close()

	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	if f, err := zw.Create("manifest.json"); err == nil {
		f.Write(manifestData)
	}

	for _, box := range manifest.Boxes {
		if box.ExteriorFilename != "" {
			if data, err := os.ReadFile(filepath.Join(s.UploadsDir, box.ExteriorFilename)); err == nil {
				if f, err := zw.Create("exteriors/" + box.ExteriorFilename); err == nil {
					f.Write(data)
				}
			}
		}
		for _, photo := range box.Photos {
			if data, err := os.ReadFile(filepath.Join(s.UploadsDir, photo.Filename)); err == nil {
				if f, err := zw.Create("photos/" + photo.Filename); err == nil {
					f.Write(data)
				}
			}
		}
	}
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "file too large or invalid form", 400)
		return
	}

	overwrite := r.FormValue("overwrite") == "true"

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file uploaded", 400)
		return
	}
	defer file.Close()

	fileData, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read file", 500)
		return
	}

	zipReader, err := zip.NewReader(bytes.NewReader(fileData), int64(len(fileData)))
	if err != nil {
		http.Error(w, "invalid ZIP file", 400)
		return
	}

	// Find manifest.json
	var manifestFile *zip.File
	for _, f := range zipReader.File {
		if f.Name == "manifest.json" {
			manifestFile = f
			break
		}
	}
	if manifestFile == nil {
		http.Error(w, "manifest.json not found in ZIP", 400)
		return
	}

	manifestReader, err := manifestFile.Open()
	if err != nil {
		http.Error(w, "failed to read manifest", 500)
		return
	}
	defer manifestReader.Close()

	var manifest ExportManifest
	if err := json.NewDecoder(manifestReader).Decode(&manifest); err != nil {
		http.Error(w, "invalid manifest.json", 400)
		return
	}

	// Index ZIP files for lookup
	zipFiles := make(map[string]*zip.File)
	for _, f := range zipReader.File {
		zipFiles[f.Name] = f
	}

	importedBoxes := 0
	skippedBoxes := 0
	importedPhotos := 0

	for _, box := range manifest.Boxes {
		existing, err := s.getBox(box.ID)
		boxExists := err == nil && existing != nil

		if boxExists && !overwrite {
			skippedBoxes++
			continue
		}

		tx, err := s.DB.Begin()
		if err != nil {
			http.Error(w, "db error", 500)
			return
		}

		// Delete existing box data if overwriting
		if boxExists {
			existingPhotos, _ := s.getBoxPhotos(box.ID)
			for _, p := range existingPhotos {
				os.Remove(filepath.Join(s.UploadsDir, p.Filename))
			}
			if existing.ExteriorFilename != "" {
				os.Remove(filepath.Join(s.UploadsDir, existing.ExteriorFilename))
			}
			tx.Exec("DELETE FROM photos WHERE box_id=?", box.ID)
			tx.Exec("DELETE FROM boxes WHERE id=?", box.ID)
		}

		// Insert box
		archivedInt := 0
		if box.Archived {
			archivedInt = 1
		}
		_, err = tx.Exec(
			"INSERT INTO boxes (id, name, memo, tags, max_photos, protect_recent, archived, created_at, updated_at, exterior_filename, annotation, annotation_photo_id, annotation_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)",
			box.ID, box.Name, box.Memo, box.Tags, box.MaxPhotos, box.ProtectRecent, archivedInt, box.CreatedAt, box.UpdatedAt, box.ExteriorFilename,
			box.Annotation, box.AnnotationPhotoID, box.AnnotationAt,
		)
		if err != nil {
			tx.Rollback()
			http.Error(w, "failed to insert box", 500)
			return
		}

		// Copy exterior file
		if box.ExteriorFilename != "" {
			s.extractZipFile(zipFiles, "exteriors/"+box.ExteriorFilename, filepath.Join(s.UploadsDir, box.ExteriorFilename))
		}

		// Insert photos and copy files
		for _, photo := range box.Photos {
			_, err = tx.Exec(
				"INSERT INTO photos (id, box_id, filename, captured_at, created_at) VALUES (?,?,?,?,?)",
				photo.ID, box.ID, photo.Filename, photo.CapturedAt, photo.CreatedAt,
			)
			if err != nil {
				tx.Rollback()
				http.Error(w, "failed to insert photo", 500)
				return
			}
			s.extractZipFile(zipFiles, "photos/"+photo.Filename, filepath.Join(s.UploadsDir, photo.Filename))
			importedPhotos++
		}

		if err := tx.Commit(); err != nil {
			http.Error(w, "db commit error", 500)
			return
		}
		importedBoxes++
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{
		"imported_boxes":  importedBoxes,
		"skipped_boxes":   skippedBoxes,
		"imported_photos": importedPhotos,
	})
}

func (s *Server) extractZipFile(zipFiles map[string]*zip.File, zipPath, destPath string) {
	zf, ok := zipFiles[zipPath]
	if !ok {
		return
	}
	reader, err := zf.Open()
	if err != nil {
		return
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return
	}
	os.WriteFile(destPath, data, 0644)
}

// Ensure sort is used (needed for template FuncMap)
var _ = sort.Strings
