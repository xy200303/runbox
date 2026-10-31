package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xy200303/runbox/sdk/go"
)

func newTestRunner(t *testing.T, mutate func(*Options)) *Runner {
	t.Helper()
	return newTestRunnerAt(t, t.TempDir(), mutate)
}

func newTestRunnerAt(t *testing.T, root string, mutate func(*Options)) *Runner {
	t.Helper()
	options := Options{
		Root:                     root,
		MaxConcurrency:           2,
		UIDMin:                   20000,
		UIDMax:                   20100,
		MaxUploadBytes:           1 << 20,
		MaxStdoutBytes:           64 << 10,
		MaxOutputFiles:           10,
		MaxOutputBytes:           10 << 20,
		QuotaBytes:               0,
		KeepBytes:                0,
		Retention:                24 * time.Hour,
		DefaultTimeout:           10 * time.Second,
		MaxTimeout:               30 * time.Second,
		Logger:                   slog.New(slog.NewTextHandler(io.Discard, nil)),
		AllowMissingCapabilities: true,
	}
	if mutate != nil {
		mutate(&options)
	}
	executor, err := New(options)
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	return executor
}

func testID(id string) sandbox.Identity { return sandbox.TokenIdentity(id) }

func testKey(t *testing.T, id string) string {
	t.Helper()
	key, err := testID(id).CanonicalKey()
	if err != nil {
		t.Fatalf("canonical key for %q: %v", id, err)
	}
	return key
}

func readAll(t *testing.T, reader io.ReadCloser) string {
	t.Helper()
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(data)
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	var apiErr *sandbox.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *sandbox.Error, got %T: %v", err, err)
	}
	return apiErr.Code
}

func TestSaveFileIsolation(t *testing.T) {
	executor := newTestRunner(t, nil)
	fileID, err := executor.SaveFile(context.Background(), testID("alice"), "hello.txt", strings.NewReader("hi"), 2)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	reader, name, err := executor.OpenFile(context.Background(), testID("alice"), fileID)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if name != "hello.txt" || readAll(t, reader) != "hi" {
		t.Fatalf("unexpected file %q", name)
	}
	if _, _, err := executor.OpenFile(context.Background(), testID("bob"), fileID); err == nil {
		t.Fatal("another identity must not read the file")
	}
	if _, _, err := executor.OpenFile(context.Background(), testID("other"), fileID); err == nil {
		t.Fatal("another identity must not read the file")
	}
}

func TestUploadLimit(t *testing.T) {
	executor := newTestRunner(t, func(options *Options) { options.MaxUploadBytes = 8 })
	if _, err := executor.SaveFile(context.Background(), testID("alice"), "big.txt", strings.NewReader("0123456789"), 10); apiCode(t, err) != "too_large" {
		t.Fatalf("expected too_large, got %v", err)
	}
	// A lying size must still be caught by the limit reader.
	if _, err := executor.SaveFile(context.Background(), testID("alice"), "big.txt", strings.NewReader("0123456789"), 0); apiCode(t, err) != "too_large" {
		t.Fatalf("expected too_large, got %v", err)
	}
}

func TestOpenPathRejectsEscapes(t *testing.T) {
	executor := newTestRunner(t, nil)
	if _, err := executor.SaveFile(context.Background(), testID("alice"), "ok.txt", strings.NewReader("ok"), 2); err != nil {
		t.Fatalf("save: %v", err)
	}
	for _, candidate := range []string{"../ok.txt", "/etc/passwd", `..\ok.txt`, `C:\Windows\win.ini`, "files/../../ok.txt"} {
		if _, _, err := executor.OpenPath(context.Background(), testID("alice"), candidate); err == nil {
			t.Fatalf("path %q must be rejected", candidate)
		}
	}
	reader, name, err := executor.OpenPath(context.Background(), testID("alice"), "files/ok.txt")
	if err != nil {
		t.Fatalf("open valid path: %v", err)
	}
	if name != "ok.txt" || readAll(t, reader) != "ok" {
		t.Fatal("unexpected content for valid path")
	}
}

func TestOpenPathRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs extra privileges on Windows")
	}
	executor := newTestRunner(t, nil)
	if _, err := executor.SaveFile(context.Background(), testID("alice"), "ok.txt", strings.NewReader("ok"), 2); err != nil {
		t.Fatalf("save: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	workspace := executor.workspace(testID("alice"))
	if err := os.Symlink(outside, filepath.Join(workspace, "out", "leak.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, _, err := executor.OpenPath(context.Background(), testID("alice"), "out/leak.txt"); err == nil {
		t.Fatal("a symlink pointing outside the workspace must be rejected")
	}
	outputs := executor.collectOutputs(workspace, time.Now().Add(-time.Minute), testKey(t, "alice"))
	for _, output := range outputs {
		if output.Name == "leak.txt" {
			t.Fatal("symlinks must not be reported as outputs")
		}
	}
}

func TestExecOutputsAndArchive(t *testing.T) {
	executor := newTestRunner(t, nil)
	ctx := context.Background()
	result, err := executor.Exec(ctx, testID("alice"), writeCommand("out/report.txt", "data"), nil, 10*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code %d: %s", result.ExitCode, result.Stdout)
	}
	if len(result.Files) != 1 || result.Files[0].Name != "report.txt" {
		t.Fatalf("unexpected outputs: %+v", result.Files)
	}
	output := result.Files[0]
	reader, _, err := executor.OpenFile(context.Background(), testID("alice"), output.FileID)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	if !strings.Contains(readAll(t, reader), "data") {
		t.Fatal("output content missing")
	}
	reader, _, err = executor.OpenPath(context.Background(), testID("alice"), "out/report.txt")
	if err != nil {
		t.Fatalf("open path: %v", err)
	}
	if !strings.Contains(readAll(t, reader), "data") {
		t.Fatal("path content missing")
	}
	entries, err := executor.List(context.Background(), testID("alice"), "out")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "report.txt" || entries[0].IsDir {
		t.Fatalf("unexpected listing: %+v", entries)
	}

	archived, err := executor.Archive(context.Background(), testID("alice"), output.FileID)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	keepID := archived.FileID
	reader, _, err = executor.OpenFile(context.Background(), testID("alice"), keepID)
	if err != nil {
		t.Fatalf("open archived: %v", err)
	}
	if !strings.Contains(readAll(t, reader), "data") {
		t.Fatal("archived content missing")
	}
	if _, err := os.Stat(filepath.Join(executor.workspace(testID("alice")), "keep")); !os.IsNotExist(err) {
		t.Fatal("keep must not live inside the user-writable workspace")
	}

	// Later writes to out/ must not change the delivered copy.
	if err := os.WriteFile(filepath.Join(executor.workspace(testID("alice")), "out", "report.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	reader, _, err = executor.OpenFile(context.Background(), testID("alice"), keepID)
	if err != nil {
		t.Fatalf("open archived again: %v", err)
	}
	if !strings.Contains(readAll(t, reader), "data") {
		t.Fatal("archived copy was modified")
	}

	if err := executor.DeleteWorkspace(context.Background(), testID("alice"), true); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(executor.keepDir(testID("alice"))); !os.IsNotExist(err) {
		t.Fatal("keep was not removed")
	}
}

func TestExecInputs(t *testing.T) {
	executor := newTestRunner(t, nil)
	fileID, err := executor.SaveFile(context.Background(), testID("alice"), "hello.txt", strings.NewReader("from-upload"), 11)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	result, err := executor.Exec(context.Background(), testID("alice"), catCommand("files/hello.txt"), []string{fileID}, 10*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !strings.Contains(result.Stdout, "from-upload") {
		t.Fatalf("input not visible: %q", result.Stdout)
	}
	if _, err := executor.Exec(context.Background(), testID("alice"), catCommand("files/hello.txt"), []string{sandbox.EncodeFile("bob", "files", "hello.txt")}, time.Second); err == nil {
		t.Fatal("an input from another workspace must be rejected")
	}
}

func TestExecExitCodeAndStderr(t *testing.T) {
	executor := newTestRunner(t, nil)
	ctx := context.Background()
	result, err := executor.Exec(ctx, testID("alice"), stderrCommand("oops"), nil, 10*time.Second)
	if err != nil {
		t.Fatalf("stderr exec: %v", err)
	}
	if !strings.Contains(result.Stdout, "oops") {
		t.Fatalf("stderr not captured: %q", result.Stdout)
	}
	result, err = executor.Exec(ctx, testID("alice"), exitCommand(7), nil, 10*time.Second)
	if err != nil {
		t.Fatalf("exit exec: %v", err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", result.ExitCode)
	}
}

func TestExecTimeout(t *testing.T) {
	executor := newTestRunner(t, nil)
	started := time.Now()
	_, err := executor.Exec(context.Background(), testID("alice"), sleepCommand(), nil, 200*time.Millisecond)
	if apiCode(t, err) != "timeout" {
		t.Fatalf("expected timeout, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 6*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestExecBusy(t *testing.T) {
	executor := newTestRunner(t, func(options *Options) { options.MaxConcurrency = 1 })
	executor.sem <- struct{}{}
	defer func() { <-executor.sem }()
	_, err := executor.Exec(context.Background(), testID("alice"), echoCommand("hi"), nil, time.Second)
	if apiCode(t, err) != "busy" {
		t.Fatalf("expected busy, got %v", err)
	}
}

func TestExecQuota(t *testing.T) {
	executor := newTestRunner(t, func(options *Options) { options.QuotaBytes = 16 })
	if _, err := executor.SaveFile(context.Background(), testID("alice"), "big.txt", strings.NewReader(strings.Repeat("x", 64)), 64); err != nil {
		t.Fatalf("save: %v", err)
	}
	_, err := executor.Exec(context.Background(), testID("alice"), echoCommand("hi"), nil, time.Second)
	if apiCode(t, err) != "quota_exceeded" {
		t.Fatalf("expected quota_exceeded, got %v", err)
	}
}

func TestSweep(t *testing.T) {
	executor := newTestRunner(t, func(options *Options) { options.Retention = time.Hour })
	if _, err := executor.SaveFile(context.Background(), testID("alice"), "old.txt", strings.NewReader("old"), 3); err != nil {
		t.Fatalf("save: %v", err)
	}
	workspace := executor.workspace(testID("alice"))
	if err := os.WriteFile(filepath.Join(workspace, "out", "deliver.txt"), []byte("payload"), 0o600); err != nil {
		t.Fatalf("write out: %v", err)
	}
	archived, err := executor.Archive(context.Background(), testID("alice"), sandbox.EncodeFile(testKey(t, "alice"), "out", "deliver.txt"))
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	backdate(t, workspace, time.Now().Add(-48*time.Hour))
	swept, err := executor.Sweep(context.Background())
	if err != nil || swept != 1 {
		t.Fatalf("sweep: %d, %v", swept, err)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatal("expired workspace still exists")
	}
	reader, _, err := executor.OpenFile(context.Background(), testID("alice"), archived.FileID)
	if err != nil {
		t.Fatalf("archived file removed by sweep: %v", err)
	}
	readAll(t, reader)
}

func TestCappedBuffer(t *testing.T) {
	buffer := newCappedBuffer(100)
	if _, err := buffer.Write([]byte(strings.Repeat("a", 20))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := buffer.String(); got != strings.Repeat("a", 20) {
		t.Fatalf("short stream changed: %q", got)
	}
	large := newCappedBuffer(100)
	payload := strings.Repeat("0123456789", 1000)
	if _, err := large.Write([]byte(payload)); err != nil {
		t.Fatalf("write large: %v", err)
	}
	got := large.String()
	if len(got) > 400 || !strings.Contains(got, "truncated") {
		t.Fatalf("unexpected bounded output (len %d)", len(got))
	}
	if !strings.HasPrefix(got, payload[:50]) || !strings.HasSuffix(got, payload[len(payload)-50:]) {
		t.Fatal("head or tail was not preserved")
	}

	ring := newCappedBuffer(10)
	for _, chunk := range []string{"ab", "cd", "ef", "gh"} {
		if _, err := ring.Write([]byte(chunk)); err != nil {
			t.Fatalf("ring write: %v", err)
		}
	}
	if got := ring.String(); got != "abcdefgh" {
		t.Fatalf("ring order broken: %q", got)
	}
	if _, err := ring.Write([]byte("ijkl")); err != nil {
		t.Fatalf("ring wrap: %v", err)
	}
	got = ring.String()
	if !strings.HasPrefix(got, "abcde") || !strings.HasSuffix(got, "hijkl") || !strings.Contains(got, "truncated") {
		t.Fatalf("ring wrap order broken: %q", got)
	}
}

func TestCommandEnvDoesNotLeakServerSecrets(t *testing.T) {
	t.Setenv("RUNBOX_API_KEY", "super-secret")
	t.Setenv("DATABASE_URL", "postgres://secret")
	env := commandEnv(t.TempDir())
	for _, entry := range env {
		if strings.Contains(entry, "super-secret") || strings.Contains(entry, "postgres://secret") {
			t.Fatalf("server secret leaked into command environment: %s", entry)
		}
		if strings.HasPrefix(entry, "RUNBOX_") {
			t.Fatalf("runner configuration leaked into command environment: %s", entry)
		}
	}
}

func backdate(t *testing.T, root string, when time.Time) {
	t.Helper()
	err := filepath.WalkDir(root, func(current string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(current, when, when)
	})
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func TestUploadOverwrite(t *testing.T) {
	executor := newTestRunner(t, nil)
	ctx := context.Background()
	if _, err := executor.SaveFile(ctx, testID("alice"), "same.txt", strings.NewReader("one"), 3); err != nil {
		t.Fatalf("first save: %v", err)
	}
	second, err := executor.SaveFile(ctx, testID("alice"), "same.txt", strings.NewReader("two"), 3)
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	reader, _, err := executor.OpenFile(ctx, testID("alice"), second)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := readAll(t, reader); got != "two" {
		t.Fatalf("overwrite content = %q", got)
	}
}

func TestNestedOutput(t *testing.T) {
	executor := newTestRunner(t, nil)
	result, err := executor.Exec(context.Background(), testID("alice"), nestedOutputCommand(), nil, 10*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].Name != "sub/n.txt" {
		t.Fatalf("unexpected nested outputs: %+v", result.Files)
	}
	reader, _, err := executor.OpenFile(context.Background(), testID("alice"), result.Files[0].FileID)
	if err != nil {
		t.Fatalf("open nested: %v", err)
	}
	if !strings.Contains(readAll(t, reader), "nested") {
		t.Fatal("nested output content missing")
	}
}

func TestOutputFileCap(t *testing.T) {
	executor := newTestRunner(t, func(options *Options) { options.MaxOutputFiles = 1 })
	result, err := executor.Exec(context.Background(), testID("alice"), twoOutputsCommand(), nil, 10*time.Second)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("output cap ignored: %+v", result.Files)
	}
}

func TestConcurrentIdentities(t *testing.T) {
	executor := newTestRunner(t, func(options *Options) { options.MaxConcurrency = 2 })
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for _, identity := range []string{"alice", "bob"} {
		wait.Add(1)
		go func(identity string) {
			defer wait.Done()
			_, err := executor.Exec(context.Background(), testID(identity), echoCommand("hi"), nil, 5*time.Second)
			errs <- err
		}(identity)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent exec: %v", err)
		}
	}
}

func TestReady(t *testing.T) {
	executor := newTestRunner(t, nil)
	if err := executor.Ready(context.Background()); err != nil {
		t.Fatalf("ready: %v", err)
	}
	if err := executor.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.RemoveAll(executor.stateRoot); err != nil {
		t.Fatalf("remove state: %v", err)
	}
	if err := executor.Ready(context.Background()); err == nil {
		t.Fatal("missing state directory must fail readiness")
	}
}

func TestExecLimitBoundaries(t *testing.T) {
	executor := newTestRunner(t, func(options *Options) {
		options.MaxCommandBytes = 8
		options.MaxInputs = 1
	})
	ctx := context.Background()
	_, err := executor.Exec(ctx, testID("alice"), "echo 12345", nil, time.Second)
	if apiCode(t, err) != "bad_request" {
		t.Fatalf("expected command limit error, got %v", err)
	}
	inputs := []string{
		sandbox.EncodeFile(testKey(t, "alice"), sandbox.KindFiles, "a"),
		sandbox.EncodeFile(testKey(t, "alice"), sandbox.KindFiles, "b"),
	}
	_, err = executor.Exec(ctx, testID("alice"), echoCommand("hi"), inputs, time.Second)
	if apiCode(t, err) != "bad_request" {
		t.Fatalf("expected input limit error, got %v", err)
	}
}

func TestIdentityMetadata(t *testing.T) {
	executor := newTestRunner(t, nil)
	ctx := context.Background()
	scoped := sandbox.ScopedIdentity("tenant-a", "alice")
	scopedKey, err := scoped.CanonicalKey()
	if err != nil {
		t.Fatalf("scoped key: %v", err)
	}
	if _, err := executor.SaveFile(ctx, scoped, "meta.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("save scoped: %v", err)
	}
	record, err := executor.store.Get(scopedKey)
	if err != nil {
		t.Fatalf("get scoped metadata: %v", err)
	}
	if record.Mode != "scoped" || record.Namespace != "tenant-a" || record.Principal != "alice" {
		t.Fatalf("unexpected scoped metadata: %+v", record)
	}
	if record.UID < int64(executor.opts.UIDMin) || record.UID >= int64(executor.opts.UIDMax) {
		t.Fatalf("uid %d outside pool", record.UID)
	}

	token := sandbox.TokenIdentity("01J-token-value")
	tokenKey, err := token.CanonicalKey()
	if err != nil {
		t.Fatalf("token key: %v", err)
	}
	if _, err := executor.SaveFile(ctx, token, "token.txt", strings.NewReader("x"), 1); err != nil {
		t.Fatalf("save token: %v", err)
	}
	tokenRecord, err := executor.store.Get(tokenKey)
	if err != nil {
		t.Fatalf("get token metadata: %v", err)
	}
	if tokenRecord.Mode != "token" || tokenRecord.Namespace != "" || tokenRecord.Principal != "" {
		t.Fatalf("unexpected token metadata: %+v", tokenRecord)
	}
	if strings.Contains(fmt.Sprint(tokenRecord), "01J-token-value") {
		t.Fatal("token must not be persisted in metadata")
	}
}

func TestUIDMappingPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	first := newTestRunnerAt(t, root, nil)
	key := testKey(t, "alice")
	uid, err := first.uidFor(testID("alice"))
	if err != nil {
		t.Fatalf("first uid: %v", err)
	}
	record, err := first.store.Get(key)
	if err != nil || record.UID != int64(uid) {
		t.Fatalf("persisted record: %+v, %v", record, err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second := newTestRunnerAt(t, root, nil)
	got, err := second.uidFor(testID("alice"))
	if err != nil {
		t.Fatalf("second uid: %v", err)
	}
	if got != uid {
		t.Fatalf("uid changed across restart: %d -> %d", uid, got)
	}
	other, err := second.uidFor(testID("bob"))
	if err != nil {
		t.Fatalf("bob uid: %v", err)
	}
	if other == uid {
		t.Fatalf("two identities share uid %d", uid)
	}
}

func TestWorkspaceReplacesSymlinkedSubdir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs extra privileges on Windows")
	}
	executor := newTestRunner(t, nil)
	if _, err := executor.SaveFile(context.Background(), testID("alice"), "seed.txt", strings.NewReader("seed"), 4); err != nil {
		t.Fatalf("save: %v", err)
	}
	workspace := executor.workspace(testID("alice"))
	outside := t.TempDir()
	filesDir := filepath.Join(workspace, "files")
	if err := os.RemoveAll(filesDir); err != nil {
		t.Fatalf("remove files dir: %v", err)
	}
	if err := os.Symlink(outside, filesDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := executor.SaveFile(context.Background(), testID("alice"), "new.txt", strings.NewReader("data"), 4); err != nil {
		t.Fatalf("save through symlink: %v", err)
	}
	info, err := os.Lstat(filesDir)
	if err != nil {
		t.Fatalf("lstat files: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		t.Fatal("files/ was not replaced with a real directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("upload escaped through the symlinked directory")
	}
}
