package server

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xy200303/runbox/internal/config"
	"github.com/xy200303/runbox/internal/runner"
	"github.com/xy200303/runbox/sdk/go"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Addr:            ":0",
		APIKey:          "test-key",
		Root:            t.TempDir(),
		MaxRequestBytes: 4 << 20,
		MaxUploadBytes:  1 << 20,
		MaxCommandBytes: 64 << 10,
		MaxInputs:       64,
		MaxStdoutBytes:  64 << 10,
		MaxOutputFiles:  10,
		MaxOutputBytes:  8 << 20,
		MaxBatchFiles:   1000,
		MaxBatchBytes:   1 << 30,
		QuotaBytes:      0,
		KeepBytes:       0,
		Retention:       time.Hour,
		SweepInterval:   time.Hour,
		MaxConcurrency:  2,
		UIDMin:          20000,
		UIDMax:          20100,
		DefaultTimeout:  10 * time.Second,
		MaxTimeout:      30 * time.Second,
		ShutdownTimeout: 5 * time.Second,
		LogFormat:       "text",
		Version:         "test",
	}
}

func newTestServer(t *testing.T, mutate func(*config.Config)) (*httptest.Server, *sandbox.Client, *runner.Runner, config.Config) {
	t.Helper()
	cfg := testConfig(t)
	if mutate != nil {
		mutate(&cfg)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	executor, err := runner.New(runner.Options{
		Root:                     cfg.Root,
		MaxConcurrency:           cfg.MaxConcurrency,
		UIDMin:                   cfg.UIDMin,
		UIDMax:                   cfg.UIDMax,
		MaxUploadBytes:           cfg.MaxUploadBytes,
		MaxStdoutBytes:           cfg.MaxStdoutBytes,
		MaxOutputFiles:           cfg.MaxOutputFiles,
		MaxOutputBytes:           cfg.MaxOutputBytes,
		QuotaBytes:               cfg.QuotaBytes,
		KeepBytes:                cfg.KeepBytes,
		Retention:                cfg.Retention,
		DefaultTimeout:           cfg.DefaultTimeout,
		MaxTimeout:               cfg.MaxTimeout,
		Logger:                   logger,
		AllowMissingCapabilities: true,
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	app := New(cfg, executor, logger)
	ts := httptest.NewServer(app.Handler())
	t.Cleanup(ts.Close)
	client, err := sandbox.NewClient(sandbox.ClientOptions{
		BaseURL:  ts.URL,
		APIKey:   cfg.APIKey,
		Identity: sandbox.TokenIdentity("alice"),
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return ts, client, executor, cfg
}

func TestHealthAndCapabilities(t *testing.T) {
	ts, client, _, _ := newTestServer(t, nil)
	ctx := context.Background()

	health, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.Status != "healthy" || health.Version != "test" {
		t.Fatalf("unexpected health: %+v", health)
	}

	// Health is intentionally reachable without a key for container probes.
	response, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("raw health: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", response.StatusCode)
	}

	caps, err := client.Capabilities(ctx)
	if err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if caps.Version != "test" || caps.Limits.MaxUploadBytes != 1<<20 || caps.Features == nil {
		t.Fatalf("unexpected capabilities: %+v", caps)
	}
}

func TestAuthentication(t *testing.T) {
	ts, _, _, _ := newTestServer(t, nil)
	body := `{"command":"echo hi"}`

	for name, headers := range map[string]map[string]string{
		"missing token":    {},
		"wrong token":      {"Authorization": "Bearer nope"},
		"legacy X-API-Key": {"X-API-Key": "test-key"},
	} {
		request, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/exec", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Runbox-Identity", "alice")
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if response.StatusCode != http.StatusUnauthorized {
			response.Body.Close()
			t.Fatalf("%s: status %d, want 401", name, response.StatusCode)
		}
		var payload struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		json.NewDecoder(response.Body).Decode(&payload)
		response.Body.Close()
		if payload.Code != "unauthorized" || payload.Message == "" {
			t.Fatalf("%s: unexpected error body: %+v", name, payload)
		}
	}

	// Authorization: Bearer is the only accepted authentication scheme.
	request, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/exec", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("X-Runbox-Identity", "alice")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("bearer: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("bearer status %d", response.StatusCode)
	}
}

func TestIdentityHeaders(t *testing.T) {
	ts, _, _, _ := newTestServer(t, nil)
	exec := func(headers map[string]string) int {
		request, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/exec", strings.NewReader(`{"command":"`+echoCommand("hi")+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer test-key")
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := exec(map[string]string{}); status != http.StatusBadRequest {
		t.Fatalf("missing identity status %d, want 400", status)
	}
	if status := exec(map[string]string{"X-Runbox-Identity": "a b"}); status != http.StatusBadRequest {
		t.Fatalf("invalid identity status %d, want 400", status)
	}
	canonical := map[string]string{"X-Runbox-Identity": "alice"}
	if status := exec(canonical); status != http.StatusOK {
		t.Fatalf("token identity status %d, want 200", status)
	}
	scoped := map[string]string{"X-Runbox-Namespace": "tenant-a", "X-Runbox-Principal": "alice"}
	if status := exec(scoped); status != http.StatusOK {
		t.Fatalf("scoped identity status %d, want 200", status)
	}
	ambiguous := map[string]string{
		"X-Runbox-Identity":  "alice",
		"X-Runbox-Namespace": "tenant-a",
		"X-Runbox-Principal": "alice",
	}
	if status := exec(ambiguous); status != http.StatusBadRequest {
		t.Fatalf("ambiguous identity status %d, want 400", status)
	}
	partial := map[string]string{"X-Runbox-Namespace": "tenant-a"}
	if status := exec(partial); status != http.StatusBadRequest {
		t.Fatalf("partial identity status %d, want 400", status)
	}
	oldHeader := map[string]string{"X-Sandbox-Identity": "alice"}
	if status := exec(oldHeader); status != http.StatusBadRequest {
		t.Fatalf("old identity header status %d, want 400", status)
	}
	foreign := map[string]string{"X-Tenant-Id": "ns", "X-User-Id": "alice"}
	if status := exec(foreign); status != http.StatusBadRequest {
		t.Fatalf("foreign identity headers status %d, want 400", status)
	}
}

func TestExecRoundTrip(t *testing.T) {
	_, client, _, _ := newTestServer(t, nil)
	ctx := context.Background()

	result, err := client.Run(ctx, sandbox.ExecRequest{Command: echoCommand("hello"), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "hello") {
		t.Fatalf("unexpected result: %+v", result)
	}

	result, err = client.Run(ctx, sandbox.ExecRequest{Command: exitCommand(3), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("exit exec: %v", err)
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit code %d, want 3", result.ExitCode)
	}

	_, err = client.Run(ctx, sandbox.ExecRequest{Command: sleepCommand(), Timeout: 200 * time.Millisecond})
	var apiErr *sandbox.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "timeout" {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestUploadExecArchive(t *testing.T) {
	_, client, executor, cfg := newTestServer(t, nil)
	ctx := context.Background()

	uploaded, err := client.Upload(ctx, "note.txt", strings.NewReader("hello-file"), 10)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	result, err := client.Run(ctx, sandbox.ExecRequest{
		Command: catCommand("files/note.txt"),
		Inputs:  []string{uploaded.FileID},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("exec input: %v", err)
	}
	if !strings.Contains(result.Stdout, "hello-file") {
		t.Fatalf("uploaded input not visible: %q", result.Stdout)
	}

	// Attachments can also be streamed with the exec request itself.
	result, err = client.Run(ctx, sandbox.ExecRequest{
		Command: catCommand("files/inline.txt"),
		Files:   []sandbox.FileUpload{{Name: "inline.txt", Reader: strings.NewReader("inline-data")}},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("exec with file: %v", err)
	}
	if !strings.Contains(result.Stdout, "inline-data") {
		t.Fatalf("inline attachment not visible: %q", result.Stdout)
	}

	result, err = client.Run(ctx, sandbox.ExecRequest{
		Command: writeCommand("out/report.txt", "report-data"),
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("exec output: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].Name != "report.txt" {
		t.Fatalf("unexpected outputs: %+v", result.Files)
	}
	outputID := result.Files[0].FileID

	reader, name, err := client.OpenFile(ctx, sandbox.Identity{}, outputID)
	if err != nil {
		t.Fatalf("download output: %v", err)
	}
	if name != "report.txt" {
		t.Fatalf("download name %q", name)
	}
	content, _ := io.ReadAll(reader)
	reader.Close()
	if !strings.Contains(string(content), "report-data") {
		t.Fatalf("output content %q", content)
	}

	reader, _, err = client.OpenPath(ctx, sandbox.Identity{}, "out/report.txt")
	if err != nil {
		t.Fatalf("open path: %v", err)
	}
	pathContent, _ := io.ReadAll(reader)
	reader.Close()
	if !strings.Contains(string(pathContent), "report-data") {
		t.Fatalf("path content %q", pathContent)
	}

	entries, err := client.List(ctx, sandbox.Identity{}, "out")
	if err != nil || len(entries) != 1 || entries[0].Name != "report.txt" {
		t.Fatalf("list: %+v, %v", entries, err)
	}
	rootEntries, err := client.List(ctx, sandbox.Identity{}, "")
	if err != nil {
		t.Fatalf("list root: %v", err)
	}
	foundOut := false
	for _, entry := range rootEntries {
		if entry.Name == "out" && entry.IsDir {
			foundOut = true
		}
	}
	if !foundOut {
		t.Fatalf("workspace root listing missing out/: %+v", rootEntries)
	}

	archived, err := client.Archive(ctx, sandbox.Identity{}, outputID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	reader, _, err = client.OpenFile(ctx, sandbox.Identity{}, archived.FileID)
	if err != nil {
		t.Fatalf("download archived: %v", err)
	}
	archivedContent, _ := io.ReadAll(reader)
	reader.Close()
	if !strings.Contains(string(archivedContent), "report-data") {
		t.Fatalf("archived content %q", archivedContent)
	}
	key, _ := sandbox.TokenIdentity("alice").CanonicalKey()
	keepPath := filepath.Join(cfg.Root, "keep", key, "report.txt")
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("keep file missing: %v", err)
	}

	// Workspace deletion keeps deliverables unless purge=true is requested.
	if err := client.DeleteWorkspace(ctx, sandbox.Identity{}, false); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, "workspaces", key)); !os.IsNotExist(err) {
		t.Fatal("workspace still exists")
	}
	if _, _, err := client.OpenFile(ctx, sandbox.Identity{}, archived.FileID); err != nil {
		t.Fatalf("archived file lost with workspace: %v", err)
	}
	if err := client.DeleteWorkspace(ctx, sandbox.Identity{}, true); err != nil {
		t.Fatalf("delete keep: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Root, "keep", key)); !os.IsNotExist(err) {
		t.Fatal("keep still exists")
	}

	// The runner's own workspace must not leak archived files into commands.
	if _, _, err := executor.OpenPath(context.Background(), sandbox.TokenIdentity("alice"), "keep/report.txt"); err == nil {
		t.Fatal("keep must not be reachable through workspace paths")
	}
}

func TestBatchUploadAndArchive(t *testing.T) {
	_, client, _, _ := newTestServer(t, nil)
	ctx := context.Background()

	uploaded, err := client.UploadBatch(ctx, []sandbox.FileUpload{
		{Name: "alpha.txt", Reader: strings.NewReader("alpha")},
		{Name: "beta.txt", Reader: strings.NewReader("beta")},
	})
	if err != nil {
		t.Fatalf("batch upload: %v", err)
	}
	if len(uploaded) != 2 || uploaded[0].Size != 5 || uploaded[1].Size != 4 {
		t.Fatalf("unexpected batch upload: %+v", uploaded)
	}

	for name, content := range map[string]string{"out/a.txt": "alpha", "out/b.txt": "beta"} {
		if _, err := client.Run(ctx, sandbox.ExecRequest{
			Command: writeCommand(name, content),
			Timeout: 5 * time.Second,
		}); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	var archive bytes.Buffer
	if _, err := client.DownloadArchive(ctx, sandbox.Identity{}, []string{"out"}, &archive); err != nil {
		t.Fatalf("archive: %v", err)
	}
	entries := map[string]string{}
	reader := tar.NewReader(&archive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		data, _ := io.ReadAll(reader)
		entries[header.Name] = string(data)
	}
	if len(entries) != 2 || strings.TrimSpace(entries["out/a.txt"]) != "alpha" || strings.TrimSpace(entries["out/b.txt"]) != "beta" {
		t.Fatalf("unexpected archive: %+v", entries)
	}

	bob := client.WithIdentity(sandbox.TokenIdentity("bob"))
	_, err = bob.DownloadArchive(ctx, sandbox.Identity{}, []string{"out"}, io.Discard)
	var apiErr *sandbox.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("cross-identity archive error: %v", err)
	}
}

func TestCrossWorkspaceAccess(t *testing.T) {
	_, client, _, _ := newTestServer(t, nil)
	ctx := context.Background()
	uploaded, err := client.Upload(ctx, "secret.txt", strings.NewReader("secret"), 6)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	bob := client.WithIdentity(sandbox.TokenIdentity("bob"))
	if _, _, err := bob.OpenFile(ctx, sandbox.Identity{}, uploaded.FileID); err == nil {
		t.Fatal("another identity downloaded the file")
	}
	if _, err := bob.Archive(ctx, sandbox.Identity{}, uploaded.FileID); err == nil {
		t.Fatal("another identity archived the file")
	}
}

func TestTraversal(t *testing.T) {
	ts, client, _, _ := newTestServer(t, nil)
	ctx := context.Background()
	if _, err := client.Upload(ctx, "ok.txt", strings.NewReader("ok"), 2); err != nil {
		t.Fatalf("upload: %v", err)
	}
	request, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/path?path="+url.QueryEscape("../etc/passwd"), nil)
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("X-Runbox-Identity", "alice")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal status %d, want 400", response.StatusCode)
	}
}

func TestUnsupportedContentType(t *testing.T) {
	ts, _, _, _ := newTestServer(t, nil)
	request, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/exec", strings.NewReader("echo hi"))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("X-Runbox-Identity", "alice")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415", response.StatusCode)
	}
}

func TestRequestTooLarge(t *testing.T) {
	_, client, _, _ := newTestServer(t, func(cfg *config.Config) {
		cfg.MaxRequestBytes = 1024
		cfg.MaxUploadBytes = 512
	})
	payload := bytes.Repeat([]byte("x"), 2048)
	_, err := client.Upload(context.Background(), "big.txt", bytes.NewReader(payload), int64(len(payload)))
	var apiErr *sandbox.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %v", err)
	}
}

func TestAnonymousMode(t *testing.T) {
	ts, _, _, _ := newTestServer(t, func(cfg *config.Config) {
		cfg.APIKey = ""
		cfg.AllowAnonymous = true
	})
	anonymous, err := sandbox.NewClient(sandbox.ClientOptions{BaseURL: ts.URL, Identity: sandbox.TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	result, err := anonymous.Run(context.Background(), sandbox.ExecRequest{Command: echoCommand("anon"), Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("anonymous exec: %v", err)
	}
	if !strings.Contains(result.Stdout, "anon") {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
}

func TestReadyAndMetrics(t *testing.T) {
	ts, client, _, _ := newTestServer(t, nil)
	ctx := context.Background()

	ready, err := http.Get(ts.URL + "/ready")
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	readyBody, _ := io.ReadAll(ready.Body)
	ready.Body.Close()
	if ready.StatusCode != http.StatusOK || !strings.Contains(string(readyBody), `"status":"ready"`) {
		t.Fatalf("unexpected ready response: %d %s", ready.StatusCode, readyBody)
	}

	unauthorized, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics without key: %v", err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics without key status %d", unauthorized.StatusCode)
	}

	if _, err := client.Run(ctx, sandbox.ExecRequest{Command: echoCommand("metrics"), Timeout: 5 * time.Second}); err != nil {
		t.Fatalf("exec: %v", err)
	}
	uploaded, err := client.Upload(ctx, "metrics.txt", strings.NewReader("hello"), 5)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	reader, _, err := client.OpenFile(ctx, sandbox.Identity{}, uploaded.FileID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	_, _ = io.ReadAll(reader)
	reader.Close()
	if _, err := client.Archive(ctx, sandbox.Identity{}, uploaded.FileID); err != nil {
		t.Fatalf("archive: %v", err)
	}

	request, _ := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	request.Header.Set("Authorization", "Bearer test-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metrics status %d", response.StatusCode)
	}
	text := string(body)
	for _, want := range []string{
		`runbox_build_info{version="test"} 1`,
		`runbox_exec_total 1`,
		`runbox_active_execs 0`,
		`runbox_upload_bytes_total 5`,
		`runbox_download_bytes_total 5`,
		`runbox_archive_total 1`,
		`runbox_requests_total{status="4xx"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q in:\n%s", want, text)
		}
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestLargeUploadRangeAndHead(t *testing.T) {
	ts, client, _, _ := newTestServer(t, func(cfg *config.Config) {
		cfg.MaxUploadBytes = 64 << 20
		cfg.MaxRequestBytes = 80 << 20
	})
	const size = int64(33 << 20)

	uploaded, err := client.Upload(context.Background(), "large.bin", io.LimitReader(zeroReader{}, size), size)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if uploaded.Size != size {
		t.Fatalf("uploaded size %d, want %d", uploaded.Size, size)
	}

	request := func(method string, headers map[string]string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, ts.URL+"/v1/files/"+uploaded.FileID, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("X-Runbox-Identity", "alice")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		return response
	}

	response := request(http.MethodGet, map[string]string{"Range": "bytes=0-15"})
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status %d, want 206", response.StatusCode)
	}
	if len(body) != 16 {
		t.Fatalf("range body %d bytes, want 16", len(body))
	}
	if got := response.Header.Get("Content-Range"); got != "bytes 0-15/"+strconv.FormatInt(size, 10) {
		t.Fatalf("content-range %q", got)
	}

	head := request(http.MethodHead, nil)
	head.Body.Close()
	if head.StatusCode != http.StatusOK {
		t.Fatalf("head status %d", head.StatusCode)
	}
	if got := head.Header.Get("Content-Length"); got != strconv.FormatInt(size, 10) {
		t.Fatalf("head content-length %q, want %d", got, size)
	}
}

func echoCommand(text string) string {
	if runtime.GOOS == "windows" {
		return "echo " + text
	}
	return "printf '%s\\n' " + shellQuote(text)
}

func writeCommand(relPath, content string) string {
	if runtime.GOOS == "windows" {
		return "echo " + content + "> " + filepath.FromSlash(relPath)
	}
	return "printf '%s' " + shellQuote(content) + " > " + relPath
}

func catCommand(relPath string) string {
	if runtime.GOOS == "windows" {
		return "type " + filepath.FromSlash(relPath)
	}
	return "cat " + shellQuote(relPath)
}

func exitCommand(code int) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("exit /b %d", code)
	}
	return fmt.Sprintf("exit %d", code)
}

func sleepCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 6 127.0.0.1 >nul"
	}
	return "sleep 5"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
