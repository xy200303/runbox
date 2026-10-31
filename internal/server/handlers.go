package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xy200303/runbox/sandbox"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, sandbox.Health{
		Status:        "healthy",
		Version:       s.cfg.Version,
		UptimeSeconds: int64(time.Since(s.started).Seconds()),
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if err := s.readiness(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status":  "not_ready",
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
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

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	s.metrics.write(w, s.cfg.Version, time.Since(s.started))
}

func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	osName, arch := platform()
	writeJSON(w, http.StatusOK, sandbox.Capabilities{
		Version:  s.cfg.Version,
		OS:       osName,
		Arch:     arch,
		Limits:   s.cfg.Limits(),
		Features: s.features,
	})
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	identity, err := s.identity(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.metrics.execTotal.Add(1)
	s.metrics.activeExecs.Add(1)
	defer s.metrics.activeExecs.Add(-1)
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBytes)

	var command string
	var inputs []string
	var timeout time.Duration
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mediaType {
	case "multipart/form-data":
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			s.failExec(w, r, bodyError(err, "invalid multipart body"))
			return
		}
		defer r.MultipartForm.RemoveAll()
		if len(r.MultipartForm.Value["inputs"])+len(r.MultipartForm.File["files"]) > s.cfg.MaxInputs {
			s.failExec(w, r, sandbox.BadRequest(fmt.Sprintf("too many inputs (limit %d)", s.cfg.MaxInputs)))
			return
		}
		command = r.FormValue("command")
		timeout = parseTimeoutSeconds(r.FormValue("timeout_seconds"))
		inputs = append(inputs, r.MultipartForm.Value["inputs"]...)
		for _, header := range r.MultipartForm.File["files"] {
			file, err := header.Open()
			if err != nil {
				s.failExec(w, r, sandbox.BadRequest("cannot read uploaded file: "+err.Error()))
				return
			}
			counting := &countingReader{reader: file}
			fileID, saveErr := s.runner.SaveFile(r.Context(), identity, header.Filename, counting, header.Size)
			s.metrics.uploadBytes.Add(counting.n)
			file.Close()
			if saveErr != nil {
				s.failExec(w, r, saveErr)
				return
			}
			inputs = append(inputs, fileID)
		}
	case "application/json", "":
		var payload struct {
			Command        string   `json:"command"`
			TimeoutSeconds int      `json:"timeout_seconds"`
			Inputs         []string `json:"inputs"`
		}
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
			s.failExec(w, r, bodyError(err, "invalid JSON body"))
			return
		}
		command = payload.Command
		if payload.TimeoutSeconds > 0 {
			timeout = time.Duration(payload.TimeoutSeconds) * time.Second
		}
		inputs = payload.Inputs
		if len(inputs) > s.cfg.MaxInputs {
			s.failExec(w, r, sandbox.BadRequest(fmt.Sprintf("too many inputs (limit %d)", s.cfg.MaxInputs)))
			return
		}
	default:
		s.failExec(w, r, sandbox.UnsupportedMediaType("Content-Type must be application/json or multipart/form-data"))
		return
	}

	result, err := s.runner.Exec(r.Context(), identity, command, inputs, timeout)
	if err != nil {
		s.failExec(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	identity, err := s.identity(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBytes)
	file, header, err := r.FormFile("file")
	if err != nil {
		file, header, err = r.FormFile("files")
	}
	if err != nil {
		s.fail(w, r, bodyError(err, "missing file field"))
		return
	}
	defer file.Close()
	counting := &countingReader{reader: file}
	fileID, err := s.runner.SaveFile(r.Context(), identity, header.Filename, counting, header.Size)
	s.metrics.uploadBytes.Add(counting.n)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sandbox.UploadedFile{FileID: fileID, Name: header.Filename, Size: header.Size})
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	identity, err := s.identity(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reader, name, err := s.runner.OpenFile(r.Context(), identity, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer reader.Close()
	s.metrics.downloadBytes.Add(streamFile(w, r, name, reader))
}

func (s *Server) handleKeep(w http.ResponseWriter, r *http.Request) {
	identity, err := s.identity(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	output, err := s.runner.Archive(r.Context(), identity, r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.metrics.archiveTotal.Add(1)
	writeJSON(w, http.StatusOK, output)
}

func (s *Server) handlePath(w http.ResponseWriter, r *http.Request) {
	identity, err := s.identity(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		s.fail(w, r, sandbox.BadRequest("path query parameter is required"))
		return
	}
	reader, name, err := s.runner.OpenPath(r.Context(), identity, path)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer reader.Close()
	s.metrics.downloadBytes.Add(streamFile(w, r, name, reader))
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	identity, err := s.identity(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	entries, err := s.runner.List(r.Context(), identity, r.URL.Query().Get("path"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if entries == nil {
		entries = []sandbox.ListEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (s *Server) handleDeleteWorkspace(w http.ResponseWriter, r *http.Request) {
	identity, err := s.identity(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	purge := false
	if value := strings.TrimSpace(r.URL.Query().Get("purge")); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			s.fail(w, r, sandbox.BadRequest("purge must be a boolean"))
			return
		}
		purge = parsed
	}
	if err := s.runner.DeleteWorkspace(r.Context(), identity, purge); err != nil {
		s.fail(w, r, err)
		return
	}
	s.metrics.workspaceDeletes.Add(1)
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) failExec(w http.ResponseWriter, r *http.Request, err error) {
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
	s.fail(w, r, err)
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
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
		s.log.Error("request failed", "path", r.URL.Path, "error", apiErr.Message)
	}
	writeJSON(w, apiErr.Status, apiErr)
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

func streamFile(w http.ResponseWriter, r *http.Request, name string, reader io.Reader) int64 {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentHeader(name))
	w.WriteHeader(http.StatusOK)
	written, err := io.Copy(w, reader)
	if err != nil {
		slog.Warn("stream interrupted", "path", r.URL.Path, "error", err.Error())
	}
	return written
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
