package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsReadyStates(t *testing.T) {
	var ready atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"status":"not_ready","message":"state dir missing"}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ready"}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ok, err := client.IsReady(context.Background())
	if err != nil {
		t.Fatalf("not ready must not be an error: %v", err)
	}
	if ok {
		t.Fatal("expected not ready")
	}
	ready.Store(true)
	if ok, err = client.IsReady(context.Background()); err != nil || !ok {
		t.Fatalf("ready: %v %v", ok, err)
	}
}

func TestWaitReadyPolls(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ready"}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.WaitReady(ctx, 10*time.Millisecond); err != nil {
		t.Fatalf("wait ready: %v", err)
	}
	if calls.Load() < 3 {
		t.Fatalf("expected retries, got %d calls", calls.Load())
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	err = client.WaitReady(ctx, 10*time.Millisecond)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_ready" {
		t.Fatalf("expected not_ready error, got %v", err)
	}
}

func TestMetricsReturnsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accept := r.Header.Get("Accept"); accept != "text/plain" {
			t.Errorf("Accept = %q", accept)
		}
		_, _ = io.WriteString(w, "runbox_active_execs 0\n")
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "k"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	body, err := client.Metrics(context.Background())
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if body != "runbox_active_execs 0\n" {
		t.Fatalf("metrics body = %q", body)
	}
}

func TestReadPathAndDownloadToFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
		_, _ = io.WriteString(w, "report-body")
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	data, err := client.ReadPath(context.Background(), Identity{}, "out/report.txt")
	if err != nil {
		t.Fatalf("read path: %v", err)
	}
	if string(data) != "report-body" {
		t.Fatalf("body = %q", data)
	}
}

func TestUploadBytes(t *testing.T) {
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"path":"files/a.csv","name":"a.csv","size":3}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	uploaded, err := client.UploadBytes(context.Background(), "a.csv", []byte("1,2"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if uploaded.Path != "files/a.csv" {
		t.Fatalf("path = %q", uploaded.Path)
	}
	if !bytes.Contains(body, []byte("1,2")) {
		t.Fatalf("multipart body missing payload: %q", body)
	}
}

func TestOpenArchiveStreams(t *testing.T) {
	var sawQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/x-tar")
		_, _ = io.WriteString(w, "tar-data")
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	reader, err := client.OpenArchive(context.Background(), Identity{}, []string{"out", "tmp"})
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "tar-data" {
		t.Fatalf("body = %q", body)
	}
	if sawQuery != "path=out&path=tmp" {
		t.Fatalf("query = %q", sawQuery)
	}
}

func TestRunStreamsAttachmentFromReader(t *testing.T) {
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		file, _, err := r.FormFile("files")
		if err != nil {
			t.Errorf("form file: %v", err)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		received.Store(int64(len(data)))
		_, _ = io.WriteString(w, `{"exit_code":0,"stdout":"ok","files":[]}`)
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	// A pipe is only drained while the request runs: if the client buffered the
	// reader up front, this would deadlock instead of streaming.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write(bytes.Repeat([]byte("x"), 1<<16))
		_ = pw.Close()
	}()
	result, err := client.Run(context.Background(), ExecRequest{
		Command: "cat files/input.bin",
		Files:   []FileUpload{{Name: "input.bin", Reader: pr}},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit = %d", result.ExitCode)
	}
	if received.Load() != 1<<16 {
		t.Fatalf("server received %d bytes", received.Load())
	}
}
