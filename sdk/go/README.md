# Runbox Go SDK

Go client for [Runbox](https://runbox.xyun.dev), the lightweight sandbox for
agent commands and files. The module is standard-library only: one HTTP client,
no generated code, no dependencies.

```bash
go get github.com/xy200303/runbox/sdk/go
```

The import path is `.../sdk/go`, the package name is `sandbox`.

## Quick start

```go
package main

import (
	"context"
	"fmt"
	"os"

	sandbox "github.com/xy200303/runbox/sdk/go"
)

func main() {
	client, err := sandbox.NewClient(sandbox.ClientOptions{
		BaseURL:  "http://localhost:8099",
		APIKey:   os.Getenv("RUNBOX_API_KEY"),
		Identity: sandbox.ScopedIdentity("team-a", "user-42"),
	})
	if err != nil {
		panic(err)
	}

	result, err := client.Run(context.Background(), sandbox.ExecRequest{
		Command: "python3 -c 'print(6 * 7)'",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(result.ExitCode, result.Stdout)
}
```

`Client` implements `sandbox.Backend`, so an application can swap the HTTP
client for an in-process executor without changing its call sites.

## Identity

Exactly one identity form per request:

| Form | Constructor | Headers sent |
| --- | --- | --- |
| Opaque token | `sandbox.TokenIdentity("user-42")` | `X-Runbox-Identity` |
| Tenant + user | `sandbox.ScopedIdentity("team-a", "user-42")` | `X-Runbox-Namespace` + `X-Runbox-Principal` |

Bind one identity at construction and override it per call:

```go
client, _ := sandbox.NewClient(sandbox.ClientOptions{BaseURL: url, APIKey: key})

// per-call identity, same client, safe for concurrent use
result, err := client.Exec(ctx, sandbox.TokenIdentity("alice"), "ls -la", 30*time.Second)
```

## Run a command

With the bound identity:

```go
result, err := client.Run(ctx, sandbox.ExecRequest{
	Command: "python3 analyze.py files/input.csv",
	Timeout: 60 * time.Second,
	Files: []sandbox.FileUpload{
		{Name: "input.csv", Reader: f},        // streamed from any io.Reader
	},
})
```

The runner writes attachments to `files/<name>` inside the caller's workspace,
so the command refers to them by that relative path. `Result` carries
`ExitCode`, `Stdout`, `Files` and `DurationMS`; the raw response is in
`docs/api.md` if you need the untruncated stream.

## Files

Uploads return the workspace path; there are no file IDs to translate.

```go
uploaded, err := client.UploadBytes(ctx, "input.csv", data) // or Upload / UploadFile / UploadBatch
```

Download a file to a writer, or to a path:

```go
name, n, err := client.DownloadPathTo(ctx, identity, "out/report.csv", w)
_, err = client.DownloadToFile(ctx, identity, "out/report.csv", "report.csv")
```

## Streaming and forwarding

`OpenPath` returns the body without buffering it. `OpenPathWithOptions` also
returns the headers, so an HTTP handler can forward status, length, type and
filename instead of guessing:

```go
reader, meta, err := client.OpenPathWithOptions(ctx, identity, "out/report.pdf",
	sandbox.PathOptions{Range: "bytes=0-1023"})
if err != nil {
	return err
}
defer reader.Close()
// meta.Name, meta.Size (-1 if unknown), meta.ContentType
```

A handler that proxies the download without touching disk:

```go
func forward(w http.ResponseWriter, r *http.Request, client *sandbox.Client, id sandbox.Identity) {
	reader, meta, err := client.OpenPathWithOptions(r.Context(), id, r.URL.Query().Get("path"),
		sandbox.PathOptions{Range: r.Header.Get("Range")})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer reader.Close()

	if meta.ContentType != "" {
		w.Header().Set("Content-Type", meta.ContentType)
	}
	if meta.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	}
	if meta.Name != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+meta.Name+`"`)
	}
	if r.Header.Get("Range") != "" {
		w.WriteHeader(http.StatusPartialContent)
	}
	io.Copy(w, reader)
}
```

Directories and several paths at once come back as one tar stream:

```go
archive, err := client.OpenArchive(ctx, identity, []string{"out", "tmp/step2"})
defer archive.Close()
io.Copy(w, archive)
```

Both streams stop as soon as the client disconnects: the handler's writer
returns an error and `io.Copy` gives up, so no goroutine is left reading.

## Workspace helpers

```go
entries, err := client.List(ctx, identity, "out")
err = client.DeleteWorkspace(ctx, identity)   // identity and UID mapping survive
body, err := client.ReadPath(ctx, identity, "out/notes.txt") // small files only
```

## Health, limits and errors

```go
health, _ := client.Health(ctx)               // no identity required
caps, _ := client.Capabilities(ctx)           // limits and detected tools
ready, _ := client.IsReady(ctx)
err = client.WaitReady(ctx, time.Second)
metrics, _ := client.Metrics(ctx)
```

Failures come back as `*sandbox.Error` with `Status`, `Code` and `Message`:

```go
var apiErr *sandbox.Error
if errors.As(err, &apiErr) {
	switch apiErr.Code {
	case "busy", "quota_exceeded": // retry later, or clean the workspace
	case "timeout":                // raise ExecRequest.Timeout
	}
}
```

## Links

- API reference: [docs/api.md](../../docs/api.md)
- Deployment and hardening: [docs/deployment.md](../../docs/deployment.md)
- Changelog: [CHANGELOG.md](../../CHANGELOG.md)
