package srv

import (
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
	ID               string
	Name             string
	Memo             string
	MaxPhotosRaw     int // 0 = use app default
	ProtectRecentRaw int // 0 = use app default
	MaxPhotos        int // resolved (after applying defaults)
	ProtectRecent    int // resolved
	ExteriorFilename string
	Archived         bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	PhotoCount       int
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
	mux.HandleFunc("GET /settings", s.handleSettingsPage)

	// Static & uploads
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.StaticDir))))
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(s.UploadsDir))))

	slog.Info("starting server", "addr", addr)
	return http.ListenAndServe(addr, mux)
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
		SELECT b.id, b.name, b.memo, b.max_photos, b.protect_recent, b.archived, b.created_at, b.updated_at,
		       b.exterior_filename,
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
		err := rows.Scan(&b.ID, &b.Name, &b.Memo, &b.MaxPhotos, &b.ProtectRecent, &arch, &b.CreatedAt, &b.UpdatedAt, &b.ExteriorFilename, &b.PhotoCount)
		if err != nil {
			return nil, err
		}
		b.Archived = arch == 1
		s.resolveBox(&b)
		boxes = append(boxes, b)
	}
	return boxes, nil
}

func (s *Server) getBox(id string) (*Box, error) {
	var b Box
	var arch int
	err := s.DB.QueryRow(`
		SELECT b.id, b.name, b.memo, b.max_photos, b.protect_recent, b.archived, b.created_at, b.updated_at,
		       b.exterior_filename,
		       COALESCE((SELECT COUNT(*) FROM photos WHERE box_id=b.id), 0)
		FROM boxes b WHERE b.id=?`, id).Scan(
		&b.ID, &b.Name, &b.Memo, &b.MaxPhotos, &b.ProtectRecent, &arch, &b.CreatedAt, &b.UpdatedAt, &b.ExteriorFilename, &b.PhotoCount,
	)
	if err != nil {
		return nil, err
	}
	b.Archived = arch == 1
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

// Ensure sort is used (needed for template FuncMap)
var _ = sort.Strings
