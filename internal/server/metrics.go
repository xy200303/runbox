package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// metrics is a deliberately small, dependency-free Prometheus exposition.
// Labels are bounded to status classes and error reasons to avoid cardinality
// explosions from identities or paths.
type metrics struct {
	requests2xx atomic.Int64
	requests3xx atomic.Int64
	requests4xx atomic.Int64
	requests5xx atomic.Int64

	execTotal   atomic.Int64
	execTimeout atomic.Int64
	execBusy    atomic.Int64
	execOther   atomic.Int64

	uploadBytes      atomic.Int64
	downloadBytes    atomic.Int64
	archiveTotal     atomic.Int64
	workspaceDeletes atomic.Int64

	activeExecs atomic.Int64
}

func (m *metrics) observeStatus(status int) {
	switch status / 100 {
	case 2:
		m.requests2xx.Add(1)
	case 3:
		m.requests3xx.Add(1)
	case 4:
		m.requests4xx.Add(1)
	case 5:
		m.requests5xx.Add(1)
	default:
		m.requests5xx.Add(1)
	}
}

func (m *metrics) observeExecError(reason string) {
	switch reason {
	case "timeout":
		m.execTimeout.Add(1)
	case "busy":
		m.execBusy.Add(1)
	default:
		m.execOther.Add(1)
	}
}

func (m *metrics) write(w http.ResponseWriter, version string, uptime time.Duration) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP runbox_requests_total HTTP requests by status class.\n")
	fmt.Fprintf(w, "# TYPE runbox_requests_total counter\n")
	fmt.Fprintf(w, "runbox_requests_total{status=\"2xx\"} %d\n", m.requests2xx.Load())
	fmt.Fprintf(w, "runbox_requests_total{status=\"3xx\"} %d\n", m.requests3xx.Load())
	fmt.Fprintf(w, "runbox_requests_total{status=\"4xx\"} %d\n", m.requests4xx.Load())
	fmt.Fprintf(w, "runbox_requests_total{status=\"5xx\"} %d\n", m.requests5xx.Load())

	fmt.Fprintf(w, "# HELP runbox_exec_total Commands accepted for execution.\n")
	fmt.Fprintf(w, "# TYPE runbox_exec_total counter\n")
	fmt.Fprintf(w, "runbox_exec_total %d\n", m.execTotal.Load())
	fmt.Fprintf(w, "# HELP runbox_exec_errors_total Failed commands by reason.\n")
	fmt.Fprintf(w, "# TYPE runbox_exec_errors_total counter\n")
	fmt.Fprintf(w, "runbox_exec_errors_total{reason=\"timeout\"} %d\n", m.execTimeout.Load())
	fmt.Fprintf(w, "runbox_exec_errors_total{reason=\"busy\"} %d\n", m.execBusy.Load())
	fmt.Fprintf(w, "runbox_exec_errors_total{reason=\"other\"} %d\n", m.execOther.Load())

	fmt.Fprintf(w, "# HELP runbox_upload_bytes_total Uploaded file bytes.\n")
	fmt.Fprintf(w, "# TYPE runbox_upload_bytes_total counter\n")
	fmt.Fprintf(w, "runbox_upload_bytes_total %d\n", m.uploadBytes.Load())
	fmt.Fprintf(w, "# HELP runbox_download_bytes_total Downloaded file bytes.\n")
	fmt.Fprintf(w, "# TYPE runbox_download_bytes_total counter\n")
	fmt.Fprintf(w, "runbox_download_bytes_total %d\n", m.downloadBytes.Load())

	fmt.Fprintf(w, "# HELP runbox_archive_total Files archived.\n")
	fmt.Fprintf(w, "# TYPE runbox_archive_total counter\n")
	fmt.Fprintf(w, "runbox_archive_total %d\n", m.archiveTotal.Load())
	fmt.Fprintf(w, "# HELP runbox_workspace_deletes_total Workspaces deleted.\n")
	fmt.Fprintf(w, "# TYPE runbox_workspace_deletes_total counter\n")
	fmt.Fprintf(w, "runbox_workspace_deletes_total %d\n", m.workspaceDeletes.Load())

	fmt.Fprintf(w, "# HELP runbox_active_execs Commands currently running.\n")
	fmt.Fprintf(w, "# TYPE runbox_active_execs gauge\n")
	fmt.Fprintf(w, "runbox_active_execs %d\n", m.activeExecs.Load())
	fmt.Fprintf(w, "# HELP runbox_uptime_seconds Process uptime.\n")
	fmt.Fprintf(w, "# TYPE runbox_uptime_seconds gauge\n")
	fmt.Fprintf(w, "runbox_uptime_seconds %d\n", int64(uptime/time.Second))

	fmt.Fprintf(w, "# HELP runbox_build_info Build information.\n")
	fmt.Fprintf(w, "# TYPE runbox_build_info gauge\n")
	fmt.Fprintf(w, "runbox_build_info{version=\"%s\"} 1\n", promLabel(version))
}

func promLabel(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\n", "\\n")
	return replacer.Replace(value)
}

// countingReader reports how many bytes were actually read, which is more
// trustworthy than a client-supplied Content-Length.
type countingReader struct {
	reader io.Reader
	n      int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.n += int64(n)
	return n, err
}
