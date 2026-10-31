// Package server exposes a Runner over HTTP. The wire format is deliberately
// small: file endpoints, one exec endpoint, health/readiness and metrics.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/xy200303/runbox/internal/config"
	"github.com/xy200303/runbox/internal/runner"
	"github.com/xy200303/runbox/sandbox"
)

// Server binds a runner and configuration to HTTP handlers.
type Server struct {
	cfg      config.Config
	runner   *runner.Runner
	log      *slog.Logger
	handler  http.Handler
	started  time.Time
	features map[string]bool
	metrics  *metrics

	readyMu  sync.Mutex
	readyAt  time.Time
	readyErr error
}

// New builds the HTTP handler. The returned Server is safe for concurrent use.
func New(cfg config.Config, r *runner.Runner, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		cfg:      cfg,
		runner:   r,
		log:      logger,
		started:  time.Now(),
		features: detectFeatures(),
		metrics:  &metrics{},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.HandleFunc("GET /metrics", s.authenticated(s.handleMetrics))
	mux.HandleFunc("GET /v1/capabilities", s.authenticated(s.handleCapabilities))
	mux.HandleFunc("POST /v1/exec", s.authenticated(s.handleExec))
	mux.HandleFunc("POST /v1/files", s.authenticated(s.handleUpload))
	mux.HandleFunc("GET /v1/files/{id}", s.authenticated(s.handleFile))
	mux.HandleFunc("POST /v1/files/{id}/keep", s.authenticated(s.handleKeep))
	mux.HandleFunc("GET /v1/path", s.authenticated(s.handlePath))
	mux.HandleFunc("GET /v1/list", s.authenticated(s.handleList))
	mux.HandleFunc("DELETE /v1/workspace", s.authenticated(s.handleDeleteWorkspace))

	s.handler = s.recoverer(s.accessLog(s.securityHeaders(mux)))
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.AllowAnonymous {
			next(w, r)
			return
		}
		if s.cfg.APIKey == "" {
			s.fail(w, r, sandbox.Unauthorized("runner API key is not configured"))
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			s.fail(w, r, sandbox.Unauthorized("missing or invalid Authorization: Bearer token"))
			return
		}
		if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.cfg.APIKey)) != 1 {
			s.fail(w, r, sandbox.Unauthorized("invalid bearer token"))
			return
		}
		next(w, r)
	}
}

// identity reads the two supported identity forms. Supplying both forms, or
// only half of namespace+principal, is rejected instead of merged.
func (s *Server) identity(r *http.Request) (sandbox.Identity, error) {
	token := strings.TrimSpace(r.Header.Get("X-Runbox-Identity"))
	namespace := strings.TrimSpace(r.Header.Get("X-Runbox-Namespace"))
	principal := strings.TrimSpace(r.Header.Get("X-Runbox-Principal"))

	if token != "" && (namespace != "" || principal != "") {
		return sandbox.Identity{}, sandbox.BadRequest("ambiguous identity: send either X-Runbox-Identity or X-Runbox-Namespace + X-Runbox-Principal")
	}
	if token != "" {
		identity := sandbox.TokenIdentity(token)
		if err := identity.Validate(); err != nil {
			return sandbox.Identity{}, err
		}
		return identity, nil
	}
	if namespace == "" || principal == "" {
		return sandbox.Identity{}, sandbox.BadRequest("missing identity: send X-Runbox-Identity or X-Runbox-Namespace + X-Runbox-Principal")
	}
	identity := sandbox.ScopedIdentity(namespace, principal)
	if err := identity.Validate(); err != nil {
		return sandbox.Identity{}, err
	}
	return identity, nil
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := newRequestID()
		w.Header().Set("X-Request-Id", requestID)
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.metrics.observeStatus(recorder.status)
		s.log.Info("request",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"bytes", recorder.bytes,
			"duration_ms", time.Since(started).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("panic while handling request", "path", r.URL.Path, "panic", recovered)
				s.fail(w, r, sandbox.Internal("internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	written, err := w.ResponseWriter.Write(p)
	w.bytes += int64(written)
	return written, err
}

func (w *statusRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func newRequestID() string {
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer[:])
}

// rfc5987 encodes a filename for the filename* parameter. Unlike
// url.PathEscape it also escapes characters such as apostrophes that would
// otherwise terminate the ext-value.
func rfc5987(value string) string {
	var builder strings.Builder
	for _, char := range []byte(value) {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			strings.ContainsRune("!#$&+-.^_`|~", rune(char)):
			builder.WriteByte(char)
		default:
			fmt.Fprintf(&builder, "%%%02X", char)
		}
	}
	return builder.String()
}

func attachmentHeader(name string) string {
	fallback := strings.Map(func(char rune) rune {
		if char < 0x20 || char > 0x7e || char == '"' || char == '\\' {
			return '_'
		}
		return char
	}, name)
	if strings.TrimSpace(fallback) == "" {
		fallback = "file"
	}
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", fallback, rfc5987(name))
}

func detectFeatures() map[string]bool {
	candidates := map[string][]string{
		"python":      {"python3", "python"},
		"node":        {"node"},
		"npm":         {"npm"},
		"libreoffice": {"soffice", "libreoffice"},
		"pdftotext":   {"pdftotext"},
		"git":         {"git"},
	}
	features := make(map[string]bool, len(candidates))
	for feature, binaries := range candidates {
		for _, binary := range binaries {
			if _, err := exec.LookPath(binary); err == nil {
				features[feature] = true
				break
			}
		}
	}
	return features
}

// Compile-time guard: the runner must keep satisfying the public contract.
var _ sandbox.Backend = (*runner.Runner)(nil)

func platform() (string, string) { return runtime.GOOS, runtime.GOARCH }
