// Package server exposes a Runner over HTTP. The wire format is deliberately
// small: file endpoints, one exec endpoint, health/readiness and metrics.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xy200303/runbox/internal/config"
	"github.com/xy200303/runbox/internal/runner"
	"github.com/xy200303/runbox/sdk/go"
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

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.RedirectTrailingSlash = false
	engine.RedirectFixedPath = false
	engine.HandleMethodNotAllowed = true
	_ = engine.SetTrustedProxies(nil)

	engine.Use(s.recovery(), s.securityHeaders(), s.accessLog())

	engine.GET("/health", s.handleHealth)
	engine.GET("/ready", s.handleReady)
	engine.GET("/metrics", s.requireAuth(), s.handleMetrics)
	engine.GET("/v1/capabilities", s.requireAuth(), s.handleCapabilities)
	engine.POST("/v1/exec", s.requireAuth(), s.handleExec)
	engine.POST("/v1/files", s.requireAuth(), s.handleUpload)
	engine.POST("/v1/files/batch", s.requireAuth(), s.handleUploadBatch)
	engine.GET("/v1/path", s.requireAuth(), s.handlePath)
	engine.HEAD("/v1/path", s.requireAuth(), s.handlePath)
	engine.GET("/v1/list", s.requireAuth(), s.handleList)
	engine.GET("/v1/archive", s.requireAuth(), s.handleBatchDownload)
	engine.DELETE("/v1/workspace", s.requireAuth(), s.handleDeleteWorkspace)

	engine.NoRoute(func(c *gin.Context) {
		s.fail(c, sandbox.NotFound("not found"))
	})
	engine.NoMethod(func(c *gin.Context) {
		s.fail(c, sandbox.NewError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed"))
	})

	s.handler = engine
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) requireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.cfg.AllowAnonymous {
			c.Next()
			return
		}
		if s.cfg.APIKey == "" {
			s.fail(c, sandbox.Unauthorized("runner API key is not configured"))
			c.Abort()
			return
		}
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			s.fail(c, sandbox.Unauthorized("missing or invalid Authorization: Bearer token"))
			c.Abort()
			return
		}
		if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.cfg.APIKey)) != 1 {
			s.fail(c, sandbox.Unauthorized("invalid bearer token"))
			c.Abort()
			return
		}
		c.Next()
	}
}

// identity reads the two supported identity forms. Supplying both forms, or
// only half of namespace+principal, is rejected instead of merged.
func (s *Server) identity(c *gin.Context) (sandbox.Identity, error) {
	token := strings.TrimSpace(c.GetHeader("X-Runbox-Identity"))
	namespace := strings.TrimSpace(c.GetHeader("X-Runbox-Namespace"))
	principal := strings.TrimSpace(c.GetHeader("X-Runbox-Principal"))

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

func (s *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func (s *Server) accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		requestID := newRequestID()
		c.Header("X-Request-Id", requestID)
		c.Next()

		status := c.Writer.Status()
		size := int64(c.Writer.Size())
		if size < 0 {
			size = 0
		}
		s.metrics.observeStatus(status)
		s.log.Info("request",
			"request_id", requestID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", status,
			"bytes", size,
			"duration_ms", time.Since(started).Milliseconds(),
			"remote", c.Request.RemoteAddr,
		)
	}
}

func (s *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("panic while handling request", "path", c.Request.URL.Path, "panic", recovered)
				if !c.Writer.Written() {
					s.fail(c, sandbox.Internal("internal server error"))
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

func writeJSON(c *gin.Context, status int, body any) {
	c.JSON(status, body)
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
