// Command example shows the small end-to-end flow: run Python, download the
// artifact, and keep a long-lived copy.
//
//	RUNBOX_URL=http://localhost:8099 RUNBOX_API_KEY=... go run ./examples/go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/xy200303/runbox/sdk/go"
)

func main() {
	baseURL := envOr("RUNBOX_URL", "http://localhost:8099")
	client, err := sandbox.NewClient(sandbox.ClientOptions{
		BaseURL:  baseURL,
		APIKey:   os.Getenv("RUNBOX_API_KEY"),
		Identity: sandbox.ScopedIdentity(envOr("RUNBOX_NAMESPACE", "demo"), envOr("RUNBOX_PRINCIPAL", "alice")),
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	caps, err := client.Capabilities(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("runner %s (%s/%s), python=%v, libreoffice=%v\n",
		caps.Version, caps.OS, caps.Arch, caps.Features["python"], caps.Features["libreoffice"])

	result, err := client.Run(ctx, sandbox.ExecRequest{
		Command: `python3 -c "open('out/hello.txt','w').write('hello from runbox')"`,
		Timeout: 30 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("exit=%d stdout=%q\n", result.ExitCode, result.Stdout)

	for _, output := range result.Files {
		destination := filepath.Join(os.TempDir(), filepath.Base(output.Name))
		file, err := os.Create(destination)
		if err != nil {
			log.Fatal(err)
		}
		name, written, err := client.DownloadTo(ctx, output.FileID, file)
		file.Close()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("downloaded %s as %s (%d bytes)\n", name, destination, written)

		archived, err := client.Archive(ctx, sandbox.Identity{}, output.FileID)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("archived for long-term download: %s (%d bytes)\n", archived.FileID, archived.Size)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
