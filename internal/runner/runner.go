// Package runner implements the in-container executor: per-identity
// workspaces, per-identity UIDs, bounded output capture and idle cleanup.
//
// It is intentionally not part of the public SDK. The HTTP server uses it
// directly; SDK users talk to the server.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xy200303/runbox/internal/store"
	"github.com/xy200303/runbox/sdk/go"
)

// Options configures a Runner. Zero values are replaced with safe defaults,
// except the quota where zero means unlimited.
type Options struct {
	Root            string
	MaxConcurrency  int
	UIDMin          int
	UIDMax          int
	MaxUploadBytes  int64
	MaxCommandBytes int64
	MaxStdoutBytes  int
	MaxOutputFiles  int
	MaxOutputBytes  int64
	MaxBatchFiles   int
	MaxBatchBytes   int64
	QuotaBytes      int64
	Retention       time.Duration
	DefaultTimeout  time.Duration
	MaxTimeout      time.Duration
	// PipIndex and NpmRegistry are injected into every command environment so
	// package installs use a reachable mirror. Empty leaves the image defaults.
	PipIndex    string
	NpmRegistry string
	Logger      *slog.Logger
	// AllowMissingCapabilities lets a root runner start without the Linux
	// capabilities it needs. Development only; production should fail closed.
	AllowMissingCapabilities bool
}

// Runner executes commands inside the current process's container.
type Runner struct {
	opts Options
	log  *slog.Logger

	root           string
	workspacesRoot string
	stateRoot      string
	sem            chan struct{}
	store          *store.Store

	mu      sync.Mutex
	uidPool chan int
	userUID map[string]int
	locks   map[string]*sync.Mutex
}

// Workspace layout:
//
//	<root>/workspaces/<identity>/   mutable, identity-owned
//	<root>/state/runbox.db          identities and UID assignments
func New(opts Options) (*Runner, error) {
	if strings.TrimSpace(opts.Root) == "" {
		opts.Root = ".runbox"
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 2
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = sandbox.DefaultMaxUploadBytes
	}
	if opts.MaxCommandBytes <= 0 {
		opts.MaxCommandBytes = 64 << 10
	}
	if opts.MaxStdoutBytes < 1024 {
		opts.MaxStdoutBytes = sandbox.DefaultMaxStdoutBytes
	}
	if opts.MaxBatchFiles <= 0 {
		opts.MaxBatchFiles = 1000
	}
	if opts.MaxBatchBytes <= 0 {
		opts.MaxBatchBytes = 1 << 30
	}
	if opts.DefaultTimeout <= 0 {
		opts.DefaultTimeout = sandbox.DefaultTimeout
	}
	if opts.MaxTimeout < opts.DefaultTimeout {
		opts.MaxTimeout = sandbox.DefaultMaxTimeout
		if opts.MaxTimeout < opts.DefaultTimeout {
			opts.MaxTimeout = opts.DefaultTimeout
		}
	}
	if opts.Retention <= 0 {
		opts.Retention = sandbox.DefaultRetention
	}
	if opts.UIDMin == 0 && opts.UIDMax == 0 {
		opts.UIDMin, opts.UIDMax = 20000, 20100
	}
	if opts.UIDMin < 1 || opts.UIDMax <= opts.UIDMin || opts.UIDMax > 1<<31 || opts.UIDMax-opts.UIDMin > 100000 {
		return nil, fmt.Errorf("invalid UID range %d..%d", opts.UIDMin, opts.UIDMax)
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if canDropPrivileges() {
		if missing := missingCapabilities(); len(missing) > 0 {
			if !opts.AllowMissingCapabilities {
				return nil, fmt.Errorf("missing required Linux capabilities %v; add CHOWN, FOWNER, SETUID, SETGID, DAC_OVERRIDE and KILL to the container, or set RUNBOX_ALLOW_MISSING_CAPS=1 for development", missing)
			}
			logger.Warn("missing Linux capabilities; workspace ownership or timeout kills may fail",
				"missing", missing,
				"hint", "add CHOWN, FOWNER, SETUID, SETGID, DAC_OVERRIDE and KILL to the container",
			)
		}
	}

	runner := &Runner{
		opts:           opts,
		log:            logger,
		root:           root,
		workspacesRoot: filepath.Join(root, "workspaces"),
		stateRoot:      filepath.Join(root, "state"),
		sem:            make(chan struct{}, opts.MaxConcurrency),
		uidPool:        make(chan int, opts.UIDMax-opts.UIDMin),
		userUID:        map[string]int{},
		locks:          map[string]*sync.Mutex{},
	}
	// The workspace root must stay traversable but not listable: an identity
	// reaches its own directory by path and must not enumerate the others.
	// The state directory holds the identity table, so it is root-only.
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{
		{root, 0o755},
		{runner.workspacesRoot, 0o711},
		{runner.stateRoot, 0o700},
	} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir.path, err)
		}
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return nil, fmt.Errorf("chmod %s: %w", dir.path, err)
		}
	}
	st, err := store.Open(filepath.Join(runner.stateRoot, "runbox.db"))
	if err != nil {
		return nil, fmt.Errorf("open state store: %w", err)
	}
	runner.store = st
	if err := runner.loadUIDMappings(); err != nil {
		_ = st.Close()
		return nil, err
	}
	return runner, nil
}

// loadUIDMappings restores persisted identity -> UID assignments and fills
// the free pool. Without this, a restart could hand the same identity a
// different UID while its files still belong to the old one.
func (r *Runner) loadUIDMappings() error {
	identities, err := r.store.All()
	if err != nil {
		return fmt.Errorf("load identities: %w", err)
	}
	used := make(map[int]bool, r.opts.UIDMax-r.opts.UIDMin)
	for _, identity := range identities {
		uid := int(identity.UID)
		if uid < r.opts.UIDMin || uid >= r.opts.UIDMax {
			return fmt.Errorf("identity %s has UID %d outside the configured range %d..%d; fix the state database or widen RUNBOX_UID_MIN/RUNBOX_UID_MAX", identity.Key, uid, r.opts.UIDMin, r.opts.UIDMax)
		}
		if used[uid] {
			return fmt.Errorf("duplicate UID %d in state database", uid)
		}
		used[uid] = true
		r.userUID[identity.Key] = uid
	}
	for uid := r.opts.UIDMin; uid < r.opts.UIDMax; uid++ {
		if !used[uid] {
			r.uidPool <- uid
		}
	}
	return nil
}

// Close releases the state database.
func (r *Runner) Close() error {
	if r == nil || r.store == nil {
		return nil
	}
	return r.store.Close()
}

// Enabled satisfies sandbox.Backend.
func (r *Runner) Enabled() bool { return r != nil }

// Ready verifies that the data directories exist and the state directory is
// writable. It is used by GET /ready and is cheap enough for a probe.
func (r *Runner) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, dir := range []string{r.root, r.workspacesRoot, r.stateRoot} {
		info, err := os.Stat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
	}
	probe, err := os.CreateTemp(r.stateRoot, ".ready-*")
	if err != nil {
		return fmt.Errorf("state directory is not writable: %w", err)
	}
	name := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func contextErr(ctx context.Context) *sandbox.Error {
	if ctx.Err() != nil {
		return sandbox.NewError(408, "canceled", "request canceled")
	}
	return nil
}

func (r *Runner) key(identity sandbox.Identity) (string, *sandbox.Error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	key, err := identity.CanonicalKey()
	if err != nil {
		return "", sandbox.BadRequest("invalid identity")
	}
	return key, nil
}

func (r *Runner) workspace(identity sandbox.Identity) string {
	key, _ := identity.CanonicalKey()
	return filepath.Join(r.workspacesRoot, key)
}

// ensureWorkspace creates the workspace and its conventional subdirectories,
// and hands newly created directories to the identity's UID when privileges
// allow it. Existing directories are left alone so a changed UID mapping is
// detected and repaired by chownTreeIfNeeded.
func (r *Runner) ensureWorkspace(identity sandbox.Identity, uid int, drop bool) (string, error) {
	key, err := identity.CanonicalKey()
	if err != nil {
		return "", err
	}
	workspace := filepath.Join(r.workspacesRoot, key)
	// The workspaces root is runner-owned, so the workspace path cannot
	// normally be a link. Replace anything unexpected before using it as a
	// root for MkdirAll/Chmod, both of which follow symlinks.
	if info, err := os.Lstat(workspace); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			if err := os.RemoveAll(workspace); err != nil {
				return "", err
			}
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	createdRoot := false
	if _, err := os.Lstat(workspace); os.IsNotExist(err) {
		createdRoot = true
	}
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return "", err
	}
	if err := os.Chmod(workspace, 0o750); err != nil {
		return "", err
	}
	if drop && createdRoot {
		if err := os.Lchown(workspace, uid, uid); err != nil {
			return "", err
		}
	}
	specs := []struct {
		rel  string
		mode fs.FileMode
	}{
		{sandbox.KindFiles, 0o750},
		{sandbox.KindOut, 0o750},
		{".tmp", 0o700},
		{".cache", 0o700},
		{".config", 0o700},
		{".local", 0o700},
		{".npm-global", 0o700},
		{".npm-cache", 0o700},
	}
	for _, spec := range specs {
		created, err := ensureSubdir(workspace, spec.rel, spec.mode)
		if err != nil {
			return "", err
		}
		if drop && created {
			full := filepath.Join(workspace, filepath.FromSlash(spec.rel))
			if err := os.Lchown(full, uid, uid); err != nil {
				return "", err
			}
		}
	}
	if err := r.store.Touch(key); err != nil {
		r.log.Warn("cannot update identity metadata", "key", key, "error", err)
	}
	return workspace, nil
}

// uidFor assigns a stable UID per identity. A stable UID keeps file ownership
// — and therefore pip/npm state — valid across calls.
func (r *Runner) uidFor(identity sandbox.Identity) (int, error) {
	key, err := identity.CanonicalKey()
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if uid, ok := r.userUID[key]; ok {
		return uid, nil
	}
	select {
	case uid := <-r.uidPool:
		if err := r.store.Create(store.FromSandbox(identity, uid, key)); err != nil {
			r.uidPool <- uid
			return 0, sandbox.Internal("persist identity: " + err.Error())
		}
		r.userUID[key] = uid
		return uid, nil
	default:
		return 0, sandbox.Conflict("uid_exhausted", fmt.Sprintf("no free UID in %d..%d", r.opts.UIDMin, r.opts.UIDMax))
	}
}

func (r *Runner) execUID(identity sandbox.Identity) (int, bool, error) {
	uid, err := r.uidFor(identity)
	if err != nil {
		return 0, false, err
	}
	if !canDropPrivileges() {
		return currentUID(), false, nil
	}
	return uid, true, nil
}

func (r *Runner) lockFor(key string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, ok := r.locks[key]
	if !ok {
		lock = &sync.Mutex{}
		r.locks[key] = lock
	}
	return lock
}

// SaveFile writes an upload into files/. The destination name is flattened to
// one path segment; content is copied through a limit reader so an oversized
// body cannot fill the disk.
func (r *Runner) SaveFile(ctx context.Context, identity sandbox.Identity, name string, reader io.Reader, size int64) (string, error) {
	if apiErr := contextErr(ctx); apiErr != nil {
		return "", apiErr
	}
	key, apiErr := r.key(identity)
	if apiErr != nil {
		return "", apiErr
	}
	if size > r.opts.MaxUploadBytes {
		return "", sandbox.TooLarge(fmt.Sprintf("file exceeds the %d byte upload limit", r.opts.MaxUploadBytes))
	}
	lock := r.lockFor(key)
	lock.Lock()
	defer lock.Unlock()

	uid, drop, uidErr := r.execUID(identity)
	if uidErr != nil {
		return "", uidErr
	}
	workspace, err := r.ensureWorkspace(identity, uid, drop)
	if err != nil {
		return "", sandbox.Internal("create workspace: " + err.Error())
	}
	filesDir := filepath.Join(workspace, sandbox.KindFiles)
	temp, err := os.CreateTemp(filesDir, ".upload-*")
	if err != nil {
		return "", sandbox.Internal("create upload: " + err.Error())
	}
	tempName := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(tempName)
	}
	written, err := io.Copy(temp, io.LimitReader(reader, r.opts.MaxUploadBytes+1))
	if err != nil {
		cleanup()
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return "", sandbox.TooLarge("request body is too large")
		}
		return "", sandbox.Internal("write upload: " + err.Error())
	}
	if written > r.opts.MaxUploadBytes {
		cleanup()
		return "", sandbox.TooLarge(fmt.Sprintf("file exceeds the %d byte upload limit", r.opts.MaxUploadBytes))
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return "", sandbox.Internal("close upload: " + err.Error())
	}
	if err := os.Chmod(tempName, 0o640); err != nil {
		os.Remove(tempName)
		return "", sandbox.Internal("chmod upload: " + err.Error())
	}
	if drop {
		if err := os.Chown(tempName, uid, uid); err != nil {
			os.Remove(tempName)
			return "", sandbox.Internal("chown upload: " + err.Error())
		}
	}
	safeName := sandbox.SanitizeFileName(name)
	destination := filepath.Join(filesDir, safeName)
	if err := replaceFile(tempName, destination); err != nil {
		os.Remove(tempName)
		return "", sandbox.Internal("store upload: " + err.Error())
	}
	return path.Join(sandbox.KindFiles, safeName), nil
}

// replaceFile removes an existing destination (including a symlink, which is
// unlinked rather than followed) and renames the temp file into place.
func replaceFile(tempName, destination string) error {
	if info, err := os.Lstat(destination); err == nil {
		if info.IsDir() {
			return fmt.Errorf("destination is a directory")
		}
		if err := os.Remove(destination); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(tempName, destination)
}

// OpenPath serves a file by workspace-relative path, which is what an agent
// sees when it runs `ls` in the sandbox.
func (r *Runner) OpenPath(ctx context.Context, identity sandbox.Identity, relPath string) (io.ReadCloser, string, error) {
	if apiErr := contextErr(ctx); apiErr != nil {
		return nil, "", apiErr
	}
	if _, apiErr := r.key(identity); apiErr != nil {
		return nil, "", apiErr
	}
	workspace := r.workspace(identity)
	full, err := secureJoin(workspace, relPath)
	if err != nil {
		return nil, "", err
	}
	// Stat before opening: opening a FIFO blocks until a writer appears, which
	// would pin this request (and its goroutine) indefinitely.
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return nil, "", sandbox.NotFound("file not found")
	}
	file, err := os.Open(full)
	if err != nil {
		return nil, "", sandbox.NotFound("file not found")
	}
	return file, filepath.Base(full), nil
}

// List returns one directory level of the workspace.
func (r *Runner) List(ctx context.Context, identity sandbox.Identity, relPath string) ([]sandbox.ListEntry, error) {
	if apiErr := contextErr(ctx); apiErr != nil {
		return nil, apiErr
	}
	if _, apiErr := r.key(identity); apiErr != nil {
		return nil, apiErr
	}
	workspace := r.workspace(identity)
	cleanPath := "."
	if strings.TrimSpace(relPath) != "" {
		normalized, err := sandbox.NormalizeRel(relPath)
		if err != nil {
			return nil, err
		}
		cleanPath = normalized
	}
	full := workspace
	if cleanPath != "." {
		resolved, err := secureJoin(workspace, cleanPath)
		if err != nil {
			return nil, err
		}
		full = resolved
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, sandbox.NotFound("directory not found")
	}
	if !info.IsDir() {
		return nil, sandbox.BadRequest("path is not a directory")
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, sandbox.Internal("read directory: " + err.Error())
	}
	result := make([]sandbox.ListEntry, 0, len(entries))
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			continue
		}
		rel := entry.Name()
		if cleanPath != "." {
			rel = path.Join(cleanPath, entry.Name())
		}
		result = append(result, sandbox.ListEntry{
			Name:     entry.Name(),
			Path:     rel,
			Size:     entryInfo.Size(),
			IsDir:    entryInfo.IsDir(),
			Modified: entryInfo.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// BatchFile is one regular file selected for a batch download.
type BatchFile struct {
	Path    string
	Full    string
	Size    int64
	ModTime time.Time
}

// BatchFiles resolves relative paths inside the identity workspace and returns
// the regular files they contain. Directories are walked without following
// symlinks.
func (r *Runner) BatchFiles(ctx context.Context, identity sandbox.Identity, paths []string) ([]BatchFile, error) {
	if apiErr := contextErr(ctx); apiErr != nil {
		return nil, apiErr
	}
	if _, apiErr := r.key(identity); apiErr != nil {
		return nil, apiErr
	}
	if len(paths) == 0 {
		return nil, sandbox.BadRequest("at least one path is required")
	}
	if len(paths) > r.opts.MaxBatchFiles {
		return nil, sandbox.BadRequest(fmt.Sprintf("archive accepts at most %d paths", r.opts.MaxBatchFiles))
	}
	workspace := r.workspace(identity)
	seen := make(map[string]bool)
	files := make([]BatchFile, 0)
	var total int64
	add := func(full string, info fs.FileInfo) error {
		relative, err := filepath.Rel(workspace, full)
		if err != nil {
			return sandbox.BadRequest("invalid batch path")
		}
		relative = filepath.ToSlash(relative)
		if seen[relative] {
			return nil
		}
		if len(files) >= r.opts.MaxBatchFiles {
			return sandbox.BadRequest(fmt.Sprintf("batch exceeds %d files", r.opts.MaxBatchFiles))
		}
		if total+info.Size() > r.opts.MaxBatchBytes {
			return sandbox.BadRequest(fmt.Sprintf("batch exceeds %d bytes", r.opts.MaxBatchBytes))
		}
		seen[relative] = true
		total += info.Size()
		files = append(files, BatchFile{
			Path:    relative,
			Full:    full,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		return nil
	}

	for _, raw := range paths {
		relative, err := sandbox.NormalizeRel(raw)
		if err != nil {
			return nil, err
		}
		full, err := secureJoin(workspace, relative)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(full)
		if err != nil {
			return nil, sandbox.NotFound("path not found: " + relative)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, sandbox.BadRequest("symlinks are not downloadable: " + relative)
		}
		if info.IsDir() {
			walkErr := filepath.WalkDir(full, func(current string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return nil
				}
				if entry.IsDir() {
					return nil
				}
				entryInfo, err := entry.Info()
				if err != nil || !entryInfo.Mode().IsRegular() {
					return nil
				}
				return add(current, entryInfo)
			})
			if walkErr != nil {
				return nil, walkErr
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, sandbox.BadRequest("not a regular file or directory: " + relative)
		}
		if err := add(full, info); err != nil {
			return nil, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// DeleteWorkspace removes the mutable workspace and frees the identity's UID.
// Nothing in the workspace survives, so the next request starts from an empty
// directory with a freshly allocated UID.
func (r *Runner) DeleteWorkspace(ctx context.Context, identity sandbox.Identity) error {
	if apiErr := contextErr(ctx); apiErr != nil {
		return apiErr
	}
	key, apiErr := r.key(identity)
	if apiErr != nil {
		return apiErr
	}
	lock := r.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	if err := os.RemoveAll(r.workspace(identity)); err != nil {
		return sandbox.Internal("remove workspace: " + err.Error())
	}
	if err := r.releaseIdentity(key); err != nil {
		return sandbox.Internal("release identity: " + err.Error())
	}
	r.log.Info("workspace deleted", "key", key, "mode", identity.Mode(), "namespace", identity.Namespace, "principal", identity.Principal)
	return nil
}

// releaseIdentity drops the persisted mapping so the UID returns to the pool.
// The caller holds the identity lock, so no command can be running for it.
// Processes the identity left behind (setsid, nohup) are killed first: a
// recycled UID must never inherit someone else's daemons.
func (r *Runner) releaseIdentity(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	uid, ok := r.userUID[key]
	if !ok {
		return nil
	}
	if err := killUIDProcesses(uid); err != nil {
		// Keep the mapping: reusing this UID while its processes still run
		// would hand them the next identity's workspace.
		r.log.Warn("cannot reap identity processes; keeping its UID", "key", key, "uid", uid, "error", err.Error())
		return nil
	}
	if err := r.store.Delete(key); err != nil {
		return err
	}
	delete(r.userUID, key)
	select {
	case r.uidPool <- uid:
	default:
	}
	return nil
}

// Remove deletes one workspace path. It is the escape hatch for a workspace
// that is over quota: no command can run, so the caller frees space directly.
func (r *Runner) Remove(ctx context.Context, identity sandbox.Identity, relPath string) error {
	if apiErr := contextErr(ctx); apiErr != nil {
		return apiErr
	}
	key, apiErr := r.key(identity)
	if apiErr != nil {
		return apiErr
	}
	if strings.TrimSpace(relPath) == "" {
		return sandbox.BadRequest("path is required")
	}
	workspace := r.workspace(identity)
	full, err := secureJoin(workspace, relPath)
	if err != nil {
		return err
	}
	lock := r.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	info, err := os.Lstat(full)
	if err != nil {
		return sandbox.NotFound("path not found: " + relPath)
	}
	if info.IsDir() {
		if filepath.Clean(full) == filepath.Clean(workspace) {
			return sandbox.BadRequest("use DELETE /v1/workspace to remove the whole workspace")
		}
		if err := os.RemoveAll(full); err != nil {
			return sandbox.Internal("remove path: " + err.Error())
		}
		return nil
	}
	if err := os.Remove(full); err != nil {
		return sandbox.Internal("remove path: " + err.Error())
	}
	return nil
}

// Sweep removes workspaces that have been idle for longer than the retention
// window. It skips identities with an execution in flight.
func (r *Runner) Sweep(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(r.workspacesRoot)
	if err != nil {
		return 0, err
	}
	swept := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return swept, err
		}
		if !entry.IsDir() || !sandbox.ValidKey(entry.Name()) {
			continue
		}
		key := entry.Name()
		lock := r.lockFor(key)
		if !lock.TryLock() {
			continue
		}
		workspace := filepath.Join(r.workspacesRoot, key)
		idle, err := lastModified(workspace)
		expired := err == nil && time.Since(idle) > r.opts.Retention
		if expired {
			if removeErr := os.RemoveAll(workspace); removeErr == nil {
				swept++
				r.log.Info("workspace expired", "key", key, "idle", time.Since(idle).String())
				if releaseErr := r.releaseIdentity(key); releaseErr != nil {
					r.log.Warn("release identity failed", "key", key, "error", releaseErr.Error())
				}
			}
		}
		lock.Unlock()
	}
	return swept, nil
}

func lastModified(root string) (time.Time, error) {
	info, err := os.Stat(root)
	if err != nil {
		return time.Time{}, err
	}
	latest := info.ModTime()
	err = filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return nil
		}
		if entryInfo.ModTime().After(latest) {
			latest = entryInfo.ModTime()
		}
		return nil
	})
	return latest, err
}

func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	if os.IsNotExist(err) {
		return 0, nil
	}
	return total, err
}

// collectOutputs returns files under out/ touched by this execution. Symlinks
// are skipped: serving them as root would turn a harmless link into a read of
// files the sandbox identity cannot access itself.
func (r *Runner) collectOutputs(workspace string, since time.Time) []sandbox.Output {
	outDir := filepath.Join(workspace, sandbox.KindOut)
	outputs := make([]sandbox.Output, 0)
	var total int64
	_ = filepath.WalkDir(outDir, func(full string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return nil
		}
		if info.ModTime().Before(since) {
			return nil
		}
		if r.opts.MaxOutputFiles > 0 && len(outputs) >= r.opts.MaxOutputFiles {
			return fs.SkipAll
		}
		if r.opts.MaxOutputBytes > 0 && total+info.Size() > r.opts.MaxOutputBytes {
			return nil
		}
		relative, err := filepath.Rel(outDir, full)
		if err != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		total += info.Size()
		outputs = append(outputs, sandbox.Output{
			Name: relative,
			Path: path.Join(sandbox.KindOut, relative),
			Size: info.Size(),
		})
		return nil
	})
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Name < outputs[j].Name })
	return outputs
}
