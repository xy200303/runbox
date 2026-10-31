# Runbox Go SDK

```bash
go get github.com/xy200303/runbox/sdk/go
```

```go
import (
    "context"
    "os"
    "time"

    "github.com/xy200303/runbox/sdk/go"
)

client, _ := sandbox.NewClient(sandbox.ClientOptions{
    BaseURL:  "http://localhost:8099",
    APIKey:   os.Getenv("RUNBOX_API_KEY"),
    Identity: sandbox.ScopedIdentity("team-a", "user-42"),
})
result, _ := client.Run(context.Background(), sandbox.ExecRequest{
    Command: "python3 -c \"print(6 * 7)\"",
    Timeout: 30 * time.Second,
})
```

The package name is `sandbox`; the import path is `sdk/go`. It uses only the Go
standard library and implements `sandbox.Backend`, so callers can swap between
the HTTP client and an in-process executor.

Full reference: [docs/api.md](../../docs/api.md).
