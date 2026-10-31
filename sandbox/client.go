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
			Command        string   `json:"command"`
			TimeoutSeconds int      `json:"timeout_seconds"`
			Inputs         []string `json:"inputs,omitempty"`
		}{req.Command, secondsCeil(timeout), req.Inputs}
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
			for _, input := range req.Inputs {
				if err := writer.WriteField("inputs", input); err != nil {
					pw.CloseWithError(err)
					return
				}
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
func (c *Client) Exec(ctx context.Context, identity Identity, command string, inputs []string, timeout time.Duration) (*Result, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return nil, err
	}
	return client.Run(ctx, ExecRequest{
		Command: command,
		Timeout: timeout,
		Inputs:  inputs,
	})
}

// SaveFile uploads one attachment and returns its file id. It satisfies Backend.
func (c *Client) SaveFile(ctx context.Context, identity Identity, name string, reader io.Reader, size int64) (string, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return "", err
	}
	uploaded, err := client.Upload(ctx, name, reader, size)
	if err != nil {
		return "", err
	}
	return uploaded.FileID, nil
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

// OpenFile downloads an uploaded or produced file by id.
func (c *Client) OpenFile(ctx context.Context, identity Identity, fileID string) (io.ReadCloser, string, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return nil, "", err
	}
	if fileID == "" {
		return nil, "", BadRequest("file id is required")
	}
	request, err := client.newRequest(ctx, http.MethodGet, "/v1/files/"+url.PathEscape(fileID), nil, "")
	if err != nil {
		return nil, "", err
	}
	response, err := client.send(request)
	if err != nil {
		return nil, "", err
	}
	return response.Body, responseFilename(response), nil
}

// DownloadTo streams a file into w and returns the server-side name and the
// number of bytes copied.
func (c *Client) DownloadTo(ctx context.Context, fileID string, w io.Writer) (string, int64, error) {
	reader, name, err := c.OpenFile(ctx, Identity{}, fileID)
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

// OpenPath downloads a file by its workspace-relative path (out/report.xlsx).
func (c *Client) OpenPath(ctx context.Context, identity Identity, path string) (io.ReadCloser, string, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(path) == "" {
		return nil, "", BadRequest("path is required")
	}
	request, err := client.newRequest(ctx, http.MethodGet, "/v1/path?path="+url.QueryEscape(path), nil, "")
	if err != nil {
		return nil, "", err
	}
	response, err := client.send(request)
	if err != nil {
		return nil, "", err
	}
	return response.Body, responseFilename(response), nil
}

// Archive copies a produced file into the immutable keep/ store and returns
// the long-lived file reference.
func (c *Client) Archive(ctx context.Context, identity Identity, fileID string) (Output, error) {
	client, err := c.scoped(identity)
	if err != nil {
		return Output{}, err
	}
	request, err := client.newRequest(ctx, http.MethodPost, "/v1/files/"+url.PathEscape(fileID)+"/keep", nil, "")
	if err != nil {
		return Output{}, err
	}
	response, err := client.send(request)
	if err != nil {
		return Output{}, err
	}
	defer response.Body.Close()
	var output Output
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&output); err != nil {
		return Output{}, Internal("invalid archive response: " + err.Error())
	}
	return output, nil
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

// DeleteWorkspace removes the caller's workspace. When purge is true the
// archived deliverables are removed too.
func (c *Client) DeleteWorkspace(ctx context.Context, identity Identity, purge bool) error {
	client, err := c.scoped(identity)
	if err != nil {
		return err
	}
	endpoint := "/v1/workspace"
	if purge {
		endpoint += "?purge=true"
	}
	request, err := client.newRequest(ctx, http.MethodDelete, endpoint, nil, "")
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

func (c *Client) send(request *http.Request) (*http.Response, error) {
	response, err := c.http.Do(request)
	if err != nil {
		if request.Context().Err() != nil {
			return nil, NewError(0, "canceled", request.Context().Err().Error())
		}
		return nil, NewError(0, "unreachable", err.Error())
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
