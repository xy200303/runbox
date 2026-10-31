package server

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/xy200303/runbox/sdk/go"
)

func (s *Server) handleHealth(c *gin.Context) {
	writeJSON(c, http.StatusOK, sandbox.Health{
		Status:        "healthy",
		Version:       s.cfg.Version,
		UptimeSeconds: int64(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleReady(c *gin.Context) {
	if err := s.readiness(c.Request.Context()); err != nil {
		writeJSON(c, http.StatusServiceUnavailable, map[string]string{
			"status":  "not_ready",
			"message": err.Error(),
		})
		return
	}
	writeJSON(c, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) readiness(ctx context.Context) error {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	if !s.readyAt.IsZero() && time.Since(s.readyAt) < 2*time.Second {
		return s.readyErr
	}
	s.readyErr = s.runner.Ready(ctx)
	s.readyAt = time.Now()
	return s.readyErr
}

func (s *Server) handleMetrics(c *gin.Context) {
	s.metrics.write(c.Writer, s.cfg.Version, time.Since(s.started))
}

func (s *Server) handleCapabilities(c *gin.Context) {
	osName, arch := platform()
	writeJSON(c, http.StatusOK, sandbox.Capabilities{
		Version:  s.cfg.Version,
		OS:       osName,
		Arch:     arch,
		Limits:   s.cfg.Limits(),
		Features: s.features,
	})
}

func (s *Server) handleExec(c *gin.Context) {
	identity, err := s.identity(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.metrics.execTotal.Add(1)
	s.metrics.activeExecs.Add(1)
	defer s.metrics.activeExecs.Add(-1)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.cfg.MaxRequestBytes)

	var command string
	var timeout time.Duration
	mediaType, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
	switch mediaType {
	case "multipart/form-data":
		command, timeout, err = s.parseExecMultipart(c, identity)
		if err != nil {
			s.failExec(c, err)
			return
		}
	case "application/json", "":
		var payload struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		decoder := json.NewDecoder(c.Request.Body)
		if decodeErr := decoder.Decode(&payload); decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
			s.failExec(c, bodyError(decodeErr, "invalid JSON body"))
			return
		}
		command = payload.Command
		if payload.TimeoutSeconds > 0 {
			timeout = time.Duration(payload.TimeoutSeconds) * time.Second
		}
	default:
		s.failExec(c, sandbox.UnsupportedMediaType("Content-Type must be application/json or multipart/form-data"))
		return
	}

	result, err := s.runner.Exec(c.Request.Context(), identity, command, timeout)
	if err != nil {
		s.failExec(c, err)
		return
	}
	writeJSON(c, http.StatusOK, result)
}

// parseExecMultipart reads a multipart exec request without buffering file
// bodies in memory. Values are bounded per field; files stream into the
// identity workspace as they are read.
func (s *Server) parseExecMultipart(c *gin.Context, identity sandbox.Identity) (string, time.Duration, error) {
	reader, err := c.Request.MultipartReader()
	if err != nil {
		return "", 0, bodyError(err, "invalid multipart body")
	}
	var command string
	var timeout time.Duration
	files := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", 0, bodyError(err, "invalid multipart body")
		}
		name := part.FormName()
		filename := part.FileName()
		if filename == "" {
			value, valueErr := s.readPartValue(part, name)
			part.Close()
			if valueErr != nil {
				return "", 0, valueErr
			}
			switch name {
			case "command":
				command = value
			case "timeout_seconds":
				timeout = parseTimeoutSeconds(value)
			}
			continue
		}
		if name != "files" {
			_, _ = io.Copy(io.Discard, part)
			part.Close()
			continue
		}
		if files >= s.cfg.MaxAttachments {
			part.Close()
			return "", 0, sandbox.BadRequest(fmt.Sprintf("too many attachments (limit %d)", s.cfg.MaxAttachments))
		}
		files++
		counting := &countingReader{reader: part}
		_, saveErr := s.runner.SaveFile(c.Request.Context(), identity, filename, counting, -1)
		part.Close()
		s.metrics.uploadBytes.Add(counting.n)
		if saveErr != nil {
			return "", 0, saveErr
		}
	}
	return command, timeout, nil
}

func (s *Server) readPartValue(part *multipart.Part, name string) (string, error) {
	limit := int64(4096)
	switch name {
	case "command":
		limit = s.cfg.MaxCommandBytes
		if limit <= 0 {
			limit = 64 << 10
		}
	case "timeout_seconds":
		limit = 32
	}
	data, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		return "", bodyError(err, "cannot read multipart field")
	}
	if int64(len(data)) > limit {
		return "", sandbox.BadRequest(fmt.Sprintf("multipart field %q is too large", name))
	}
	return string(data), nil
}

func (s *Server) handleUpload(c *gin.Context) {
	identity, err := s.identity(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.cfg.MaxRequestBytes)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		s.fail(c, bodyError(err, "invalid multipart body"))
		return
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.fail(c, bodyError(err, "invalid multipart body"))
			return
		}
		name := part.FormName()
		filename := part.FileName()
		if filename != "" && name == "files" {
			part.Close()
			s.fail(c, sandbox.BadRequest("use /v1/files/batch for multiple files"))
			return
		}
		if filename == "" || name != "file" {
			_, _ = io.Copy(io.Discard, part)
			part.Close()
			continue
		}
		counting := &countingReader{reader: part}
		filePath, saveErr := s.runner.SaveFile(c.Request.Context(), identity, filename, counting, -1)
		part.Close()
		s.metrics.uploadBytes.Add(counting.n)
		if saveErr != nil {
			s.fail(c, saveErr)
			return
		}
		writeJSON(c, http.StatusOK, sandbox.UploadedFile{
			Path: filePath,
			Name: sandbox.SanitizeFileName(filename),
			Size: counting.n,
		})
		return
	}
	s.fail(c, sandbox.BadRequest("missing file field"))
}

func (s *Server) handleUploadBatch(c *gin.Context) {
	identity, err := s.identity(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, s.cfg.MaxRequestBytes)
	reader, err := c.Request.MultipartReader()
	if err != nil {
		s.fail(c, bodyError(err, "invalid multipart body"))
		return
	}
	files := make([]sandbox.UploadedFile, 0)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.fail(c, bodyError(err, "invalid multipart body"))
			return
		}
		name := part.FormName()
		filename := part.FileName()
		if filename == "" || name != "files" {
			_, _ = io.Copy(io.Discard, part)
			part.Close()
			continue
		}
		if len(files) >= s.cfg.MaxBatchFiles {
			part.Close()
			s.fail(c, sandbox.BadRequest(fmt.Sprintf("batch exceeds %d files", s.cfg.MaxBatchFiles)))
			return
		}
		counting := &countingReader{reader: part}
		filePath, saveErr := s.runner.SaveFile(c.Request.Context(), identity, filename, counting, -1)
		part.Close()
		s.metrics.uploadBytes.Add(counting.n)
		if saveErr != nil {
			s.fail(c, saveErr)
			return
		}
		files = append(files, sandbox.UploadedFile{
			Path: filePath,
			Name: sandbox.SanitizeFileName(filename),
			Size: counting.n,
		})
	}
	if len(files) == 0 {
		s.fail(c, sandbox.BadRequest("missing files field"))
		return
	}
	writeJSON(c, http.StatusOK, map[string]any{"files": files})
}

// handleBatchDownload streams the requested workspace files as a tar archive.
// Paths are relative to the caller's workspace and may name files or
// directories.
func (s *Server) handleBatchDownload(c *gin.Context) {
	identity, err := s.identity(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	files, err := s.runner.BatchFiles(c.Request.Context(), identity, c.QueryArray("path"))
	if err != nil {
		s.fail(c, err)
		return
	}

	c.Header("Content-Type", "application/x-tar")
	c.Header("Content-Disposition", attachmentHeader("runbox-files.tar"))
	c.Status(http.StatusOK)

	writer := tar.NewWriter(c.Writer)
	var written int64
	for _, file := range files {
		reader, openErr := os.Open(file.Full)
		if openErr != nil {
			slog.Warn("batch download skipped file", "path", file.Path, "error", openErr.Error())
			break
		}
		header := &tar.Header{
			Name:    file.Path,
			Mode:    0o644,
			Size:    file.Size,
			ModTime: file.ModTime,
		}
		if writeErr := writer.WriteHeader(header); writeErr != nil {
			reader.Close()
			slog.Warn("batch download header failed", "path", file.Path, "error", writeErr.Error())
			break
		}
		copied, copyErr := io.Copy(writer, reader)
		reader.Close()
		written += copied
		if copyErr != nil {
			slog.Warn("batch download interrupted", "path", file.Path, "error", copyErr.Error())
			break
		}
	}
	if closeErr := writer.Close(); closeErr != nil {
		slog.Warn("batch download close failed", "error", closeErr.Error())
	}
	s.metrics.downloadBytes.Add(written)
}

func (s *Server) handlePath(c *gin.Context) {
	identity, err := s.identity(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	path := c.Query("path")
	if strings.TrimSpace(path) == "" {
		s.fail(c, sandbox.BadRequest("path query parameter is required"))
		return
	}
	reader, name, err := s.runner.OpenPath(c.Request.Context(), identity, path)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.serveDownload(c, name, reader)
}

func (s *Server) handleList(c *gin.Context) {
	identity, err := s.identity(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	entries, err := s.runner.List(c.Request.Context(), identity, c.Query("path"))
	if err != nil {
		s.fail(c, err)
		return
	}
	if entries == nil {
		entries = []sandbox.ListEntry{}
	}
	writeJSON(c, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleDeleteWorkspace(c *gin.Context) {
	identity, err := s.identity(c)
	if err != nil {
		s.fail(c, err)
		return
	}
	if err := s.runner.DeleteWorkspace(c.Request.Context(), identity); err != nil {
		s.fail(c, err)
		return
	}
	s.metrics.workspaceDeletes.Add(1)
	writeJSON(c, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) failExec(c *gin.Context, err error) {
	reason := "other"
	var apiErr *sandbox.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "timeout":
			reason = "timeout"
		case "busy":
			reason = "busy"
		}
	}
	s.metrics.observeExecError(reason)
	s.fail(c, err)
}

func (s *Server) fail(c *gin.Context, err error) {
	apiErr := &sandbox.Error{Status: http.StatusInternalServerError, Code: "internal", Message: "internal server error"}
	var typed *sandbox.Error
	if errors.As(err, &typed) {
		copied := *typed
		apiErr = &copied
	}
	if apiErr.Status == 0 {
		apiErr.Status = http.StatusInternalServerError
	}
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(apiErr.Status)
	}
	if apiErr.Status >= 500 {
		s.log.Error("request failed", "path", c.Request.URL.Path, "error", apiErr.Message)
	}
	if c.Writer.Written() {
		return
	}
	writeJSON(c, apiErr.Status, apiErr)
}

func bodyError(err error, fallback string) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return sandbox.TooLarge("request body is too large")
	}
	if errors.Is(err, http.ErrMissingFile) {
		return sandbox.BadRequest("missing file field")
	}
	return sandbox.BadRequest(fallback + ": " + err.Error())
}

// serveDownload streams a file and supports Range and HEAD when the runner
// returns an *os.File. The fallback path remains a plain stream.
func (s *Server) serveDownload(c *gin.Context, name string, reader io.ReadCloser) {
	defer reader.Close()
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", attachmentHeader(name))

	if file, ok := reader.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
			if size := c.Writer.Size(); size > 0 {
				s.metrics.downloadBytes.Add(int64(size))
			}
			return
		}
	}

	c.Status(http.StatusOK)
	written, err := io.Copy(c.Writer, reader)
	if err != nil {
		slog.Warn("stream interrupted", "path", c.Request.URL.Path, "error", err.Error())
	}
	s.metrics.downloadBytes.Add(written)
}

func parseTimeoutSeconds(value string) time.Duration {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
