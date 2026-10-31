package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ClientOptions configures an HTTP client.
type ClientOptions struct {
	BaseURL string
	APIKey  string
	// Identity can be left empty for Health/Capabilities, or replaced per
	// call with WithIdentity.
	Identity   Identity
	HTTPClient *http.Client
}

// Client talks to a Runbox runner over HTTP.
type Client struct {
	baseURL  string
	apiKey   string
	identity Identity
	http     *http.Client
}

var _ Backend = (*Client)(nil)

// NewClient validates options and returns a client.
func NewClient(opts ClientOptions) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil, BadRequest("base URL is required")
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, BadRequest("base URL must be an absolute http(s) URL")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 6 * time.Minute}
	}
	return &Client{
		baseURL:  base,
		apiKey:   opts.APIKey,
		identity: opts.Identity,
		http:     httpClient,
	}, nil
}

// Enabled reports whether the client has a usable base URL.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// WithIdentity returns a shallow copy bound to another identity. It is safe
// to reuse the same client for many concurrent callers.
func (c *Client) WithIdentity(identity Identity) *Client {
	copied := *c
	copied.identity = identity
	return &copied
}

func (c *Client) scoped(identity Identity) (*Client, error) {
	if identity.Mode() == "" {
		identity = c.identity
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	return c.WithIdentity(identity), nil
}

func (c *Client) requireIdentity() error {
	if err := c.identity.Validate(); err != nil {
		return err
	}
	return nil
}

// Health calls GET /health.
func (c *Client) Health(ctx context.Context) (*Health, error) {
	var health Health
	if err := c.doJSON(ctx, http.MethodGet, "/health", nil, &health); err != nil {
		return nil, err
	}
	return &health, nil
}

// Capabilities calls GET /v1/capabilities.
func (c *Client) Capabilities(ctx context.Context) (*Capabilities, error) {
	var caps Capabilities
	if err := c.doJSON(ctx, http.MethodGet, "/v1/capabilities", nil, &caps); err != nil {
		return nil, err
	}
	return &caps, nil
}

// Run executes a command for the client's bound identity. Requests without
// attachments use JSON; requests with attachments use multipart so files
// stream straight from the reader.
func (c *Client) Run(ctx context.Context, req ExecRequest) (*Result, error) {
	if err := c.requireIdentity(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Command) == "" {
		return nil, BadRequest("command is required")
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout+30*time.Second)
	defer cancel()

	var response *http.Response
	if len(req.Files) == 0 {
		payload := struct {
			Command        string `json:"command"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}{req.Command, secondsCeil(timeout)}
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, Internal(err.Error())
		}
		request, err := c.newRequest(execCtx, http.MethodPost, "/v1/exec", bytes.NewReader(body), "application/json")
		if err != nil {
			return nil, err
		}
		response, err = c.send(request)
		if err != nil {
			return nil, err
		}
	} else {
		pr, pw := io.Pipe()
		writer := multipart.NewWriter(pw)
		go func() {
			defer pw.Close()
			if err := writer.WriteField("command", req.Command); err != nil {
				pw.CloseWithError(err)
				return
			}
			if err := writer.WriteField("timeout_seconds", strconv.Itoa(secondsCeil(timeout))); err != nil {
				pw.CloseWithError(err)
				return
			}
			for _, file := range req.Files {
				part, err := writer.CreateFormFile("files", file.Name)
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				if _, err := io.Copy(part, file.Reader); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
			if err := writer.Close(); err != nil {
				pw.CloseWithError(err)
			}
		}()
		request, err := c.newRequest(execCtx, http.MethodPost, "/v1/exec", pr, writer.FormDataContentType())
		if err != nil {
			pr.Close()
			return nil, err
		}
		response, err = c.send(request)
		if err != nil {
			return nil, err
		}
	}
	defer response.Body.Close()
	var result Result
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&result); err != nil {
		return nil, Internal("invalid runner response: " + err.Error())
	}
	return &result, nil
}

// Exec implements Backend for callers that pass the identity per call. An
// empty identity falls back to the client's bound identity.
func (c *Client) Exec(ctx context.Context, identity Identity, command string, timeout time.Duration) (*Result, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return nil, err
	}
	return client.Run(ctx, ExecRequest{
		Command: command,
		Timeout: timeout,
	})
}

// SaveFile uploads one attachment and returns its workspace-relative path.
func (c *Client) SaveFile(ctx context.Context, identity Identity, name string, reader io.Reader, size int64) (string, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return "", err
	}
	uploaded, err := client.Upload(ctx, name, reader, size)
	if err != nil {
		return "", err
	}
	return uploaded.Path, nil
}

// Upload sends one file to the bound identity's files/ directory.
func (c *Client) Upload(ctx context.Context, name string, reader io.Reader, size int64) (*UploadedFile, error) {
	if err := c.requireIdentity(); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		part, err := writer.CreateFormFile("file", name)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, reader); err != nil {
			pw.CloseWithError(err)
			return
		}
		if err := writer.Close(); err != nil {
			pw.CloseWithError(err)
		}
	}()
	request, err := c.newRequest(ctx, http.MethodPost, "/v1/files", pr, writer.FormDataContentType())
	if err != nil {
		pr.Close()
		return nil, err
	}
	response, err := c.send(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var uploaded UploadedFile
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&uploaded); err != nil {
		return nil, Internal("invalid upload response: " + err.Error())
	}
	return &uploaded, nil
}

// UploadBatch uploads several files in one multipart request. Files are
// stored under the client's bound identity.
func (c *Client) UploadBatch(ctx context.Context, files []FileUpload) ([]UploadedFile, error) {
	if err := c.requireIdentity(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, BadRequest("at least one file is required")
	}
	pr, pw := io.Pipe()
	writer := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		for _, file := range files {
			part, err := writer.CreateFormFile("files", file.Name)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(part, file.Reader); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
		if err := writer.Close(); err != nil {
			pw.CloseWithError(err)
		}
	}()
	request, err := c.newRequest(ctx, http.MethodPost, "/v1/files/batch", pr, writer.FormDataContentType())
	if err != nil {
		pr.Close()
		return nil, err
	}
	response, err := c.send(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var payload struct {
		Files []UploadedFile `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, Internal("invalid batch upload response: " + err.Error())
	}
	return payload.Files, nil
}

// DownloadArchive writes a tar archive of the requested workspace paths to w.
// Paths are relative to the caller's workspace and may name files or
// directories.
func (c *Client) DownloadArchive(ctx context.Context, identity Identity, paths []string, w io.Writer) (int64, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return 0, err
	}
	if len(paths) == 0 {
		return 0, BadRequest("at least one path is required")
	}
	query := url.Values{}
	for _, path := range paths {
		query.Add("path", path)
	}
	request, err := client.newRequest(ctx, http.MethodGet, "/v1/archive?"+query.Encode(), nil, "")
	if err != nil {
		return 0, err
	}
	response, err := client.send(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return io.Copy(w, response.Body)
}

// OpenArchive streams a tar of several paths or directories. The caller owns
// the reader and must close it; this is what an HTTP handler forwards to a
// response so the archive never touches disk.
func (c *Client) OpenArchive(ctx context.Context, identity Identity, paths []string) (io.ReadCloser, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, BadRequest("at least one path is required")
	}
	query := url.Values{}
	for _, path := range paths {
		query.Add("path", path)
	}
	request, err := client.newRequest(ctx, http.MethodGet, "/v1/archive?"+query.Encode(), nil, "")
	if err != nil {
		return nil, err
	}
	response, err := client.send(request)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

// PathOptions controls how a workspace file is streamed back.
type PathOptions struct {
	// Range asks for a byte range, for example "bytes=0-1023". Empty streams
	// the whole file; a 206 response is passed through unchanged.
	Range string
}

// PathMeta carries the response headers of a streamed download so an HTTP
// handler can forward them instead of guessing.
type PathMeta struct {
	Name        string // filename from Content-Disposition
	Size        int64  // Content-Length, or -1 when the runner streams without one
	ContentType string
}

// OpenPath streams a file by its workspace-relative path (out/report.xlsx).
func (c *Client) OpenPath(ctx context.Context, identity Identity, path string) (io.ReadCloser, string, error) {
	reader, meta, err := c.OpenPathWithOptions(ctx, identity, path, PathOptions{})
	if err != nil {
		return nil, "", err
	}
	return reader, meta.Name, nil
}

// OpenPathWithOptions streams a file with an optional byte range and returns
// the response metadata. The caller owns the reader and must close it; nothing
// is buffered, so an HTTP handler can forward it straight to a client.
func (c *Client) OpenPathWithOptions(ctx context.Context, identity Identity, path string, opts PathOptions) (io.ReadCloser, PathMeta, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return nil, PathMeta{}, err
	}
	if strings.TrimSpace(path) == "" {
		return nil, PathMeta{}, BadRequest("path is required")
	}
	request, err := client.newRequest(ctx, http.MethodGet, "/v1/path?path="+url.QueryEscape(path), nil, "")
	if err != nil {
		return nil, PathMeta{}, err
	}
	if opts.Range != "" {
		request.Header.Set("Range", opts.Range)
	}
	response, err := client.send(request)
	if err != nil {
		return nil, PathMeta{}, err
	}
	meta := PathMeta{
		Name:        responseFilename(response),
		Size:        response.ContentLength,
		ContentType: response.Header.Get("Content-Type"),
	}
	return response.Body, meta, nil
}

// DownloadPathTo streams a workspace file into w and returns the server-side
// name and the number of bytes copied.
func (c *Client) DownloadPathTo(ctx context.Context, identity Identity, path string, w io.Writer) (string, int64, error) {
	reader, name, err := c.OpenPath(ctx, identity, path)
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()
	n, err := io.Copy(w, reader)
	if err != nil {
		return name, n, err
	}
	return name, n, nil
}

// UploadBytes uploads an in-memory buffer to the bound identity's files/ directory.
func (c *Client) UploadBytes(ctx context.Context, name string, data []byte) (*UploadedFile, error) {
	return c.Upload(ctx, name, bytes.NewReader(data), int64(len(data)))
}

// UploadFile uploads a local file and returns its workspace-relative path.
func (c *Client) UploadFile(ctx context.Context, path string) (*UploadedFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, BadRequest(err.Error())
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, BadRequest(err.Error())
	}
	return c.Upload(ctx, filepath.Base(path), file, info.Size())
}

// IsReady calls GET /ready. A 503 answer means "not ready", not a failure, so
// it returns false with a nil error. Other non-2xx answers are returned as
// *Error.
func (c *Client) IsReady(ctx context.Context) (bool, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/ready", nil, "")
	if err != nil {
		return false, err
	}
	response, err := c.doRaw(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	switch response.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusServiceUnavailable:
		return false, nil
	default:
		return false, NewError(response.StatusCode, fmt.Sprintf("http_%d", response.StatusCode), http.StatusText(response.StatusCode))
	}
}

// WaitReady polls /ready until the runner reports ready or ctx is done. Use it
// right after starting a container instead of sleeping blindly.
func (c *Client) WaitReady(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	for {
		ready, err := c.IsReady(ctx)
		if err == nil && ready {
			return nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return NewError(0, "not_ready", ctx.Err().Error()+": "+err.Error())
			}
			return NewError(0, "not_ready", ctx.Err().Error())
		case <-time.After(interval):
		}
	}
}

// Metrics calls GET /metrics and returns the Prometheus text exposition.
func (c *Client) Metrics(ctx context.Context) (string, error) {
	request, err := c.newRequest(ctx, http.MethodGet, "/metrics", nil, "")
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "text/plain")
	response, err := c.send(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", Internal(err.Error())
	}
	return string(body), nil
}

// ReadPath downloads a workspace file into memory. Use OpenPath or
// DownloadPathTo for large files.
func (c *Client) ReadPath(ctx context.Context, identity Identity, path string) ([]byte, error) {
	reader, _, err := c.OpenPath(ctx, identity, path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, Internal(err.Error())
	}
	return data, nil
}

// DownloadToFile streams a workspace file into a local file and returns the
// number of bytes written.
func (c *Client) DownloadToFile(ctx context.Context, identity Identity, path, destination string) (int64, error) {
	file, err := os.Create(destination)
	if err != nil {
		return 0, BadRequest(err.Error())
	}
	_, written, err := c.DownloadPathTo(ctx, identity, path, file)
	if err != nil {
		file.Close()
		return written, err
	}
	if err := file.Close(); err != nil {
		return written, Internal(err.Error())
	}
	return written, nil
}

// List returns a directory listing inside the caller's workspace. An empty
// path lists the workspace root.
func (c *Client) List(ctx context.Context, identity Identity, path string) ([]ListEntry, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Entries []ListEntry `json:"entries"`
	}
	endpoint := "/v1/list"
	if strings.TrimSpace(path) != "" {
		endpoint += "?path=" + url.QueryEscape(path)
	}
	if err := client.doJSON(ctx, http.MethodGet, endpoint, nil, &payload); err != nil {
		return nil, err
	}
	return payload.Entries, nil
}

// DeleteWorkspace removes the caller's workspace.
func (c *Client) DeleteWorkspace(ctx context.Context, identity Identity) error {
	client, err := c.scoped(identity)
	if err != nil {
		return err
	}
	request, err := client.newRequest(ctx, http.MethodDelete, "/v1/workspace", nil, "")
	if err != nil {
		return err
	}
	response, err := client.send(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, body io.Reader, out any) error {
	contentType := ""
	if body != nil {
		contentType = "application/json"
	}
	request, err := c.newRequest(ctx, method, endpoint, body, contentType)
	if err != nil {
		return err
	}
	response, err := c.send(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(out); err != nil {
		return Internal("invalid runner response: " + err.Error())
	}
	return nil
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body io.Reader, contentType string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, body)
	if err != nil {
		return nil, Internal(err.Error())
	}
	request.Header.Set("User-Agent", "runbox-go/"+Version)
	request.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	switch c.identity.Mode() {
	case "token":
		request.Header.Set("X-Runbox-Identity", c.identity.Token)
	case "scoped":
		request.Header.Set("X-Runbox-Namespace", c.identity.Namespace)
		request.Header.Set("X-Runbox-Principal", c.identity.Principal)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	return request, nil
}

func (c *Client) doRaw(request *http.Request) (*http.Response, error) {
	response, err := c.http.Do(request)
	if err != nil {
		if request.Context().Err() != nil {
			return nil, NewError(0, "canceled", request.Context().Err().Error())
		}
		return nil, NewError(0, "unreachable", err.Error())
	}
	return response, nil
}

func (c *Client) send(request *http.Request) (*http.Response, error) {
	response, err := c.doRaw(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return nil, decodeAPIError(response)
	}
	return response, nil
}

func decodeAPIError(response *http.Response) error {
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = json.Unmarshal(body, &payload)
	message := strings.TrimSpace(payload.Message)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" || len(message) > 2048 || strings.HasPrefix(message, "<") {
		message = http.StatusText(response.StatusCode)
	}
	code := payload.Code
	if code == "" {
		code = fmt.Sprintf("http_%d", response.StatusCode)
	}
	return &Error{Status: response.StatusCode, Code: code, Message: message}
}

func responseFilename(response *http.Response) string {
	_, params, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	if err != nil {
		return ""
	}
	if encoded := params["filename*"]; encoded != "" {
		if _, value, ok := strings.Cut(encoded, "''"); ok {
			if decoded, err := url.PathUnescape(value); err == nil {
				return decoded
			}
		}
	}
	return params["filename"]
}

func secondsCeil(d time.Duration) int {
	seconds := int((d + time.Second - 1) / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return seconds
}
