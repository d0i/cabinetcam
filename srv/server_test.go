package srv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"srv.exe.dev/db"
)

func TestRequireAuth(t *testing.T) {
	s := &Server{}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	handler := s.requireAuth(inner)

	tests := []struct {
		name       string
		path       string
		headers    map[string]string
		wantCode   int
		wantPassTo bool // true if inner handler should be reached
	}{
		{
			name:       "annotation API passes through (no auth)",
			path:       "/api/annotate/next",
			wantCode:   200,
			wantPassTo: true,
		},
		{
			name:       "annotation submit passes through",
			path:       "/api/annotate/abc123",
			wantCode:   200,
			wantPassTo: true,
		},
		{
			name:       "static files pass through",
			path:       "/static/style.css",
			wantCode:   200,
			wantPassTo: true,
		},
		{
			name:       "uploads pass through",
			path:       "/uploads/photo.jpg",
			wantCode:   200,
			wantPassTo: true,
		},
		{
			name:       "exe.dev authenticated user passes through",
			path:       "/",
			headers:    map[string]string{"X-ExeDev-Email": "user@example.com"},
			wantCode:   200,
			wantPassTo: true,
		},
		{
			name:       "exe.dev user can access API",
			path:       "/api/boxes",
			headers:    map[string]string{"X-ExeDev-Email": "user@example.com"},
			wantCode:   200,
			wantPassTo: true,
		},
		{
			name:       "unauthenticated browser gets redirected",
			path:       "/",
			wantCode:   302,
			wantPassTo: false,
		},
		{
			name:       "unauthenticated box page gets redirected",
			path:       "/box/abc123",
			wantCode:   302,
			wantPassTo: false,
		},
		{
			name:       "unauthenticated API gets 401 JSON",
			path:       "/api/boxes",
			wantCode:   401,
			wantPassTo: false,
		},
		{
			name:       "unauthenticated settings page gets redirected",
			path:       "/settings",
			wantCode:   302,
			wantPassTo: false,
		},
		{
			name:       "unauthenticated token API gets 401",
			path:       "/api/tokens",
			wantCode:   401,
			wantPassTo: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Errorf("got status %d, want %d", rr.Code, tt.wantCode)
			}
			reached := rr.Body.String() == "ok"
			if reached != tt.wantPassTo {
				t.Errorf("inner handler reached=%v, want %v (body=%q)", reached, tt.wantPassTo, rr.Body.String())
			}
			if tt.wantCode == 302 {
				loc := rr.Header().Get("Location")
				if loc == "" {
					t.Error("expected Location header for redirect")
				}
				expectedPrefix := "/__exe.dev/login?redirect="
				if len(loc) < len(expectedPrefix) || loc[:len(expectedPrefix)] != expectedPrefix {
					t.Errorf("Location=%q, want prefix %q", loc, expectedPrefix)
				}
			}
		})
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	wdb, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RunMigrations(wdb); err != nil {
		t.Fatal(err)
	}
	uploadsDir := filepath.Join(dir, "uploads")
	os.MkdirAll(uploadsDir, 0755)
	return &Server{DB: wdb, UploadsDir: uploadsDir}
}

func TestTokenExpiry(t *testing.T) {
	s := newTestServer(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	handler := s.requireToken(inner)

	// Insert a valid token (expires in 1 hour)
	validToken := "valid_token_123"
	_, err := s.DB.Exec("INSERT INTO api_tokens (token, name, created_at, expires_at) VALUES (?, ?, ?, ?)",
		validToken, "test", time.Now(), time.Now().Add(1*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	// Insert an expired token
	expiredToken := "expired_token_456"
	_, err = s.DB.Exec("INSERT INTO api_tokens (token, name, created_at, expires_at) VALUES (?, ?, ?, ?)",
		expiredToken, "old", time.Now().Add(-25*time.Hour), time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("valid token works", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/annotate/next", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Errorf("got %d, want 200", rr.Code)
		}
	})

	t.Run("expired token rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/annotate/next", nil)
		req.Header.Set("Authorization", "Bearer "+expiredToken)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != 401 {
			t.Errorf("got %d, want 401", rr.Code)
		}
		var resp map[string]string
		json.NewDecoder(rr.Body).Decode(&resp)
		if resp["error"] == "" || resp["error"] == "invalid token" {
			t.Errorf("expected expiry error message, got %q", resp["error"])
		}
	})

	t.Run("expired token is deleted from DB", func(t *testing.T) {
		var count int
		s.DB.QueryRow("SELECT COUNT(*) FROM api_tokens WHERE token=?", expiredToken).Scan(&count)
		if count != 0 {
			t.Errorf("expired token still in DB, count=%d", count)
		}
	})

	t.Run("no token gives 401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/annotate/next", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != 401 {
			t.Errorf("got %d, want 401", rr.Code)
		}
	})

	t.Run("bogus token gives 401", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/annotate/next", nil)
		req.Header.Set("Authorization", "Bearer nonexistent")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != 401 {
			t.Errorf("got %d, want 401", rr.Code)
		}
	})

	t.Run("exe.dev header bypasses token check", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/annotate/next", nil)
		req.Header.Set("X-ExeDev-Email", "user@example.com")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != 200 {
			t.Errorf("got %d, want 200", rr.Code)
		}
	})
}
