// Package server exposes the manager over a token-protected local HTTP API
// and serves the web dashboard.
package server

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"godm/internal/engine"
	"godm/internal/manager"
)

//go:embed web
var webFS embed.FS

type AddRequest struct {
	engine.Request
	URLs    []string  `json:"urls,omitempty"` // batch add, shares the other fields
	Paused  bool      `json:"paused,omitempty"`
	StartAt time.Time `json:"startAt,omitempty"`
	Source  string    `json:"source,omitempty"` // "browser" when sent by the extension
}

type Server struct {
	m     *manager.Manager
	token string

	// OnShow, when set, is called for POST /api/show (bring the app window up).
	OnShow func()
	// OnBrowserAdd, when set, receives downloads sent by the browser
	// extension. They are added paused; the hook decides when to start them.
	OnBrowserAdd func(engine.Info)
}

func New(m *manager.Manager, token string) *Server { return &Server{m: m, token: token} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServer(http.FS(static)))

	mux.HandleFunc("GET /api/downloads", s.list)
	mux.HandleFunc("POST /api/downloads", s.add)
	mux.HandleFunc("GET /api/downloads/{id}", s.get)
	mux.HandleFunc("DELETE /api/downloads/{id}", s.remove)
	mux.HandleFunc("POST /api/downloads/{id}/pause", s.action(s.m.Pause))
	mux.HandleFunc("POST /api/downloads/{id}/resume", s.action(s.m.Resume))
	mux.HandleFunc("POST /api/pause-all", func(w http.ResponseWriter, r *http.Request) { s.m.PauseAll(); writeJSON(w, 200, ok()) })
	mux.HandleFunc("POST /api/resume-all", func(w http.ResponseWriter, r *http.Request) { s.m.ResumeAll(); writeJSON(w, 200, ok()) })
	mux.HandleFunc("POST /api/show", func(w http.ResponseWriter, r *http.Request) {
		if s.OnShow != nil {
			s.OnShow()
		}
		writeJSON(w, 200, map[string]bool{"ok": true, "window": s.OnShow != nil})
	})
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	return s.secure(mux)
}

func ok() map[string]bool { return map[string]bool{"ok": true} }

// secure requires the API token on every /api call and adds safe headers.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			tok := r.Header.Get("X-GoDM-Token")
			if tok == "" {
				tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			}
			if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or wrong token"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, manager.ErrNotFound) {
		code = http.StatusNotFound
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func public(list []engine.Info) []engine.Info {
	out := make([]engine.Info, len(list))
	for i, d := range list {
		d = d.Public()
		d.Segments = compactSegments(d.Segments)
		out[i] = d
	}
	return out
}

// compactSegments keeps at most 32 segments for the progress bar drawing.
func compactSegments(s []engine.Segment) []engine.Segment {
	if len(s) <= 32 {
		return s
	}
	return s[:32]
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, public(s.m.List()))
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	i, err := s.m.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, public([]engine.Info{i})[0])
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) {
	var req AddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, errors.New("invalid JSON body"))
		return
	}
	urls := req.URLs
	if req.URL != "" {
		urls = append([]string{req.URL}, urls...)
	}
	if len(urls) == 0 {
		writeErr(w, errors.New("url is required"))
		return
	}
	if len(urls) > 1000 {
		writeErr(w, errors.New("too many URLs (max 1000)"))
		return
	}
	fromBrowser := req.Source == "browser" && s.OnBrowserAdd != nil
	if fromBrowser {
		req.Paused = true
	}
	var added []engine.Info
	for _, u := range urls {
		one := req.Request
		one.URL = strings.TrimSpace(u)
		if len(urls) > 1 {
			one.FileName = ""
		}
		info, err := s.m.Add(one, manager.AddOptions{Paused: req.Paused, StartAt: req.StartAt})
		if err != nil {
			writeErr(w, err)
			return
		}
		added = append(added, info)
		if fromBrowser {
			s.OnBrowserAdd(info)
		}
	}
	writeJSON(w, http.StatusCreated, public(added))
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request) {
	del := r.URL.Query().Get("deleteFile") == "1" || r.URL.Query().Get("deleteFile") == "true"
	if err := s.m.Remove(r.PathValue("id"), del); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, ok())
}

func (s *Server) action(fn func(string) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(r.PathValue("id")); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, ok())
	}
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	c := s.m.Config()
	writeJSON(w, 200, map[string]any{
		"downloadDir":   c.DownloadDir,
		"maxConcurrent": c.MaxConcurrent,
		"connections":   c.Connections,
		"speedLimit":    c.SpeedLimit,
		"autoRetry":     c.AutoRetry,
		"categorize":    c.Categorize,
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var st manager.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&st); err != nil {
		writeErr(w, errors.New("invalid JSON body"))
		return
	}
	if err := s.m.UpdateSettings(st); err != nil {
		writeErr(w, err)
		return
	}
	s.getSettings(w, r)
}
