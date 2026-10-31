// Package sandbox is the public Go SDK for Runbox, a self-hosted code
// execution sandbox for AI agents.
//
// It contains the shared wire types, the file-id codec, validation helpers
// and an HTTP client. The server-side implementation lives under internal/
// and is not imported by SDK users.
package sandbox

import (
	"context"
	"io"
	"time"
)

const (
	// Version is the SDK version reported in the User-Agent header.
	Version = "0.1.0"

	// FileIDPrefix identifies Runbox file references.
	FileIDPrefix = "rbx_"

	// KeepDir is the kind used for archived deliverables.
	KeepDir = "keep"

	// Kinds accepted in a file reference.
	KindFiles = "files"
	KindOut   = "out"

	DefaultMaxUploadBytes = 20 << 20
	DefaultMaxStdoutBytes = 200 << 10
	DefaultMaxOutputFiles = 20
	DefaultMaxOutputBytes = 200 << 20
	DefaultQuotaBytes     = 1 << 30
	DefaultKeepBytes      = 1 << 30
	DefaultRetention      = 7 * 24 * time.Hour
	DefaultTimeout        = 60 * time.Second
	DefaultMaxTimeout     = 300 * time.Second
)

// Result is the outcome of one command execution. A non-zero ExitCode is a
// normal result, not an error: the command ran and chose to exit non-zero.
type Result struct {
	ExitCode   int      `json:"exit_code"`
	Stdout     string   `json:"stdout"`
	Files      []Output `json:"files"`
	DurationMS int64    `json:"duration_ms,omitempty"`
}

// Output is a file produced by a command and stored under out/.
type Output struct {
	Name   string `json:"name"`
	FileID string `json:"file_id"`
	Size   int64  `json:"size"`
}

// FileRef is a decoded file id.
type FileRef struct {
	Key  string
	Kind string
	Path string
}

// UploadedFile is the response of a file upload.
type UploadedFile struct {
	FileID string `json:"file_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
}

// FileUpload is one in-memory attachment sent with ExecRequest.
type FileUpload struct {
	Name   string
	Reader io.Reader
	Size   int64
}

// ExecRequest describes one command execution.
type ExecRequest struct {
	Command string
	Timeout time.Duration
	// Inputs are file ids returned by SaveFile or by an earlier execution.
	Inputs []string
	// Files are uploaded immediately before the command runs.
	Files []FileUpload
}

// ListEntry is one entry of a workspace directory listing.
type ListEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	IsDir    bool   `json:"is_dir"`
	Modified string `json:"modified"`
}

// Limits is the resource envelope advertised by a runner.
type Limits struct {
	MaxUploadBytes   int64 `json:"max_upload_bytes"`
	MaxCommandBytes  int64 `json:"max_command_bytes"`
	MaxInputs        int   `json:"max_inputs"`
	MaxStdoutBytes   int   `json:"max_stdout_bytes"`
	MaxOutputFiles   int   `json:"max_output_files"`
	MaxOutputBytes   int64 `json:"max_output_bytes"`
	QuotaBytes       int64 `json:"quota_bytes"`
	KeepBytes        int64 `json:"keep_bytes"`
	RetentionSeconds int64 `json:"retention_seconds"`
	DefaultTimeout   int64 `json:"default_timeout_seconds"`
	MaxTimeout       int64 `json:"max_timeout_seconds"`
}

// Capabilities describes a runner and the tooling detected in its image.
type Capabilities struct {
	Version  string          `json:"version"`
	OS       string          `json:"os"`
	Arch     string          `json:"arch"`
	Limits   Limits          `json:"limits"`
	Features map[string]bool `json:"features"`
}

// Health is the response of GET /health.
type Health struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// Backend is the executor contract. Client and the in-container runner both
// implement it, so application code can depend on this interface and switch
// between an HTTP runner and an in-process one.
type Backend interface {
	Enabled() bool
	SaveFile(ctx context.Context, identity Identity, name string, reader io.Reader, size int64) (string, error)
	OpenFile(ctx context.Context, identity Identity, fileID string) (io.ReadCloser, string, error)
	OpenPath(ctx context.Context, identity Identity, path string) (io.ReadCloser, string, error)
	Exec(ctx context.Context, identity Identity, command string, inputs []string, timeout time.Duration) (*Result, error)
	Archive(ctx context.Context, identity Identity, fileID string) (Output, error)
	List(ctx context.Context, identity Identity, path string) ([]ListEntry, error)
	DeleteWorkspace(ctx context.Context, identity Identity, purge bool) error
}
