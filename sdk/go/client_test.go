package sandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClientValidation(t *testing.T) {
	if _, err := NewClient(ClientOptions{BaseURL: ""}); err == nil {
		t.Fatal("empty base URL must be rejected")
	}
	if _, err := NewClient(ClientOptions{BaseURL: "ftp://example.com"}); err == nil {
		t.Fatal("non-http scheme must be rejected")
	}
	client, err := NewClient(ClientOptions{BaseURL: "http://example.com/"})
	if err != nil || !client.Enabled() {
		t.Fatalf("valid client: %v", err)
	}
}

func TestClientRequiresIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"healthy","version":"test"}`)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("health should not require identity: %v", err)
	}
	if _, err := client.Run(context.Background(), ExecRequest{Command: "true"}); err == nil {
		t.Fatal("exec must require identity")
	}
}

func TestClientErrorMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"busy","message":"runner is at capacity"}`)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "k", Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	_, err = client.Run(context.Background(), ExecRequest{Command: "true"})
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != "busy" || apiErr.Message == "" {
		t.Fatalf("unexpected error: %+v", apiErr)
	}
}

func TestClientSendsBearerToken(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"exit_code":0,"stdout":"","files":[]}`)
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "secret", Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if _, err := client.Run(context.Background(), ExecRequest{Command: "true"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if authorization != "Bearer secret" {
		t.Fatalf("Authorization = %q", authorization)
	}
}

func TestClientDecodesRFC5987Filename(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="____.xlsx"; filename*=UTF-8''%E6%8A%A5%E8%A1%A8.xlsx`)
		_, _ = io.WriteString(w, "data")
	}))
	defer server.Close()
	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	reader, name, err := client.OpenPath(context.Background(), Identity{}, "out/report.xlsx")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reader.Close()
	if name != "报表.xlsx" {
		t.Fatalf("filename = %q", name)
	}
}

func TestWithIdentityCopiesClient(t *testing.T) {
	base := &Client{baseURL: "http://example.com", identity: TokenIdentity("b")}
	copied := base.WithIdentity(TokenIdentity("c"))
	if copied.identity != TokenIdentity("c") {
		t.Fatalf("copy identity = %+v", copied.identity)
	}
	if base.identity != TokenIdentity("b") {
		t.Fatal("WithIdentity mutated the original client")
	}
	if !strings.HasPrefix(base.baseURL, "http://") {
		t.Fatal("unexpected base URL")
	}
}

func TestOpenPathWithOptionsForwardsRange(t *testing.T) {
	var gotRange string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="report.pdf"`)
		w.Header().Set("Content-Length", "4")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "part")
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, APIKey: "k", Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	reader, meta, err := client.OpenPathWithOptions(context.Background(), TokenIdentity("alice"), "out/report.pdf", PathOptions{Range: "bytes=0-3"})
	if err != nil {
		t.Fatalf("open range: %v", err)
	}
	defer reader.Close()

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "part" {
		t.Fatalf("body = %q, want %q", body, "part")
	}
	if gotRange != "bytes=0-3" {
		t.Fatalf("Range header = %q, want bytes=0-3", gotRange)
	}
	if meta.Name != "report.pdf" || meta.Size != 4 || meta.ContentType != "application/pdf" {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

func TestOpenPathKeepsWholeFileMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "" {
			t.Fatalf("unexpected Range header %q", got)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="data.csv"`)
		w.Header().Set("Content-Length", "3")
		_, _ = io.WriteString(w, "csv")
	}))
	defer server.Close()

	client, err := NewClient(ClientOptions{BaseURL: server.URL, Identity: TokenIdentity("alice")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	reader, name, err := client.OpenPath(context.Background(), TokenIdentity("alice"), "out/data.csv")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reader.Close()
	if name != "data.csv" {
		t.Fatalf("name = %q", name)
	}
	body, _ := io.ReadAll(reader)
	if string(body) != "csv" {
		t.Fatalf("body = %q", body)
	}
}
