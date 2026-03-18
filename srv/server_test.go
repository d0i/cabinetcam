package srv

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
