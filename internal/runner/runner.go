// Package runner implements the in-container executor: per-identity
// workspaces, per-identity UIDs, bounded output capture and idle cleanup.
//
// It is intentionally not part of the public SDK. The HTTP server uses it
// directly; SDK users talk to the server.
package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xy200303/runbox/sandbox"
)

// Options configures a Runner. Zero values are replaced with safe defaults,
// except quota and keep limits where zero means "unlimited".
type Options struct {
	Root            string
	MaxConcurrency  int
	UIDMin          int
	UIDMax          int
	MaxUploadBytes  int64
	MaxCommandBytes int64
	MaxInputs       int
	MaxStdoutBytes  int
	MaxOutputFiles  int
	MaxOutputBytes  int64
	QuotaBytes      int64
	KeepBytes       int64
	Retention       time.Duration
	DefaultTimeout  time.Duration
	MaxTimeout      time.Duration
	Logger          *slog.Logger
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
	keepRoot       string
	stateRoot      string
	sem            chan struct{}

	mu      sync.Mutex
	uidPool chan int
	userUID map[string]int
	locks   map[string]*sync.Mutex
}

// Workspace layout:
//
//	<root>/workspaces/<identity>/   mutable, user-owned
//	<root>/keep/<identity>/         immutable deliverables, root-owned
//
// Keeping archived files outside the workspace means a command cannot chmod,
// rename or replace them after delivery.
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
	if opts.MaxInputs <= 0 {
		opts.MaxInputs = 64
	}
	if opts.MaxStdoutBytes < 1024 {
		opts.MaxStdoutBytes = sandbox.DefaultMaxStdoutBytes
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
		keepRoot:       filepath.Join(root, "keep"),
		stateRoot:      filepath.Join(root, "state"),
		sem:            make(chan struct{}, opts.MaxConcurrency),
		uidPool:        make(chan int, opts.UIDMax-opts.UIDMin),
		userUID:        map[string]int{},
		locks:          map[string]*sync.Mutex{},
	}
	for _, dir := range []string{root, runner.workspacesRoot, runner.keepRoot, runner.stateRoot, filepath.Join(runner.stateRoot, "identities")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	runner.loadUIDMappings()
	return runner, nil
}

// loadUIDMappings restores persisted identity -> UID assignments and fills
// the free pool. Without this, a restart could hand the same identity a
// different UID while its files still belong to the old one.
func (r *Runner) loadUIDMappings() {
	used := make(map[int]bool, r.opts.UIDMax-r.opts.UIDMin)
	uidsRoot := filepath.Join(r.stateRoot, "uids")
	entries, err := os.ReadDir(uidsRoot)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !sandbox.ValidKey(entry.Name()) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(uidsRoot, entry.Name()))
			if err != nil {
				continue
			}
			uid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || uid < r.opts.UIDMin || uid >= r.opts.UIDMax || used[uid] {
				continue
			}
			used[uid] = true
			r.userUID[entry.Name()] = uid
		}
	}
	for uid := r.opts.UIDMin; uid < r.opts.UIDMax; uid++ {
		if !used[uid] {
			r.uidPool <- uid
		}
	}
}

func (r *Runner) persistUID(identity string, uid int) {
	directory := filepath.Join(r.stateRoot, "uids")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		r.log.Warn("cannot persist uid mapping", "identity", identity, "error", err)
		return
	}
	if err := os.WriteFile(filepath.Join(directory, identity), []byte(strconv.Itoa(uid)+"\n"), 0o600); err != nil {
		r.log.Warn("cannot persist uid mapping", "identity", identity, "error", err)
	}
}

// Enabled satisfies sandbox.Backend.
func (r *Runner) Enabled() bool { return r != nil }

// Ready verifies that the data directories exist and the state directory is
// writable. It is used by GET /ready and is cheap enough for a probe.
func (r *Runner) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, dir := range []string{r.root, r.workspacesRoot, r.keepRoot, r.stateRoot, filepath.Join(r.stateRoot, "identities")} {
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

func (r *Runner) keepDir(identity sandbox.Identity) string {
	key, _ := identity.CanonicalKey()
	return filepath.Join(r.keepRoot, key)
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
	r.writeIdentityRecord(key, identity)
	return workspace, nil
}

// uidFor assigns a stable UID per identity. A stable UID keeps file ownership
// — and therefore pip/npm state — valid across calls.
type identityRecord struct {
	Version   int    `json:"version"`
	Mode      string `json:"mode"`
	Namespace string `json:"namespace,omitempty"`
	Principal string `json:"principal,omitempty"`
	CreatedAt string `json:"created_at"`
}

func (r *Runner) writeIdentityRecord(key string, identity sandbox.Identity) {
	directory := filepath.Join(r.stateRoot, "identities")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		r.log.Warn("cannot create identity metadata directory", "error", err)
		return
	}
	path := filepath.Join(directory, key+".json")
	if _, err := os.Stat(path); err == nil {
		return
	} else if !os.IsNotExist(err) {
		r.log.Warn("cannot inspect identity metadata", "key", key, "error", err)
		return
	}
	record := identityRecord{
		Version:   1,
		Mode:      identity.Mode(),
		Namespace: identity.Namespace,
		Principal: identity.Principal,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(record)
	if err != nil {
		r.log.Warn("cannot encode identity metadata", "key", key, "error", err)
		return
	}
	temp, err := os.CreateTemp(directory, ".identity-*")
	if err != nil {
		r.log.Warn("cannot create identity metadata", "key", key, "error", err)
		return
	}
	tempName := temp.Name()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempName)
		r.log.Warn("cannot write identity metadata", "key", key, "error", err)
		return
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		r.log.Warn("cannot close identity metadata", "key", key, "error", err)
		return
	}
	if err := os.Chmod(tempName, 0o640); err != nil {
		os.Remove(tempName)
		r.log.Warn("cannot chmod identity metadata", "key", key, "error", err)
		return
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		r.log.Warn("cannot store identity metadata", "key", key, "error", err)
	}
}

func (r *Runner) uidFor(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if uid, ok := r.userUID[key]; ok {
		return uid
	}
	select {
	case uid := <-r.uidPool:
		r.userUID[key] = uid
		r.persistUID(key, uid)
		return uid
	default:
		// The pool is exhausted: derive a stable UID from the key. The
		// per-identity lock still serialises calls, and directories are 0750.
		var sum uint32
		for _, char := range key {
			sum = sum*31 + uint32(char)
		}
		span := uint32(r.opts.UIDMax - r.opts.UIDMin)
		return r.opts.UIDMin + int(sum%span)
	}
}

func (r *Runner) execUID(key string) (int, bool) {
	if !canDropPrivileges() {
		return currentUID(), false
	}
	return r.uidFor(key), true
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

	uid, drop := r.execUID(key)
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
	return sandbox.EncodeFile(key, sandbox.KindFiles, safeName), nil
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

// OpenFile serves an uploaded or produced file by id.
func (r *Runner) OpenFile(ctx context.Context, identity sandbox.Identity, fileID string) (io.ReadCloser, string, error) {
	if apiErr := contextErr(ctx); apiErr != nil {
		return nil, "", apiErr
	}
	key, apiErr := r.key(identity)
	if apiErr != nil {
		return nil, "", apiErr
	}
	ref, err := sandbox.DecodeFile(fileID)
	if err != nil {
		return nil, "", err
	}
	if ref.Key != key {
		return nil, "", sandbox.Forbidden("file belongs to another workspace")
	}
	var root string
	relative := ref.Path
	switch ref.Kind {
	case sandbox.KindFiles, sandbox.KindOut:
		root = r.workspace(identity)
		relative = path.Join(ref.Kind, ref.Path)
	case sandbox.KeepDir:
		root = r.keepDir(identity)
	}
	full, err := secureJoin(root, relative)
	if err != nil {
		return nil, "", err
	}
	file, err := os.Open(full)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", sandbox.NotFound("file not found")
		}
		return nil, "", sandbox.NotFound("file not found")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, "", sandbox.NotFound("file not found")
	}
	return file, path.Base(ref.Path), nil
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
	file, err := os.Open(full)
	if err != nil {
		return nil, "", sandbox.NotFound("file not found")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, "", sandbox.NotFound("file not found")
	}
	return file, filepath.Base(full), nil
}

// Archive copies a produced file into the immutable keep/ store. The
// destination is root-owned and not writable by the sandbox identity.
func (r *Runner) Archive(ctx context.Context, identity sandbox.Identity, fileID string) (sandbox.Output, error) {
	if apiErr := contextErr(ctx); apiErr != nil {
		return sandbox.Output{}, apiErr
	}
	key, apiErr := r.key(identity)
	if apiErr != nil {
		return sandbox.Output{}, apiErr
	}
	ref, err := sandbox.DecodeFile(fileID)
	if err != nil {
		return sandbox.Output{}, err
	}
	if ref.Key != key {
		return sandbox.Output{}, sandbox.Forbidden("file belongs to another workspace")
	}
	lock := r.lockFor(key)
	lock.Lock()
	defer lock.Unlock()

	keepRoot := r.keepDir(identity)
	if ref.Kind == sandbox.KeepDir {
		full, err := secureJoin(keepRoot, ref.Path)
		if err != nil {
			return sandbox.Output{}, err
		}
		info, err := os.Stat(full)
		if err != nil || !info.Mode().IsRegular() {
			return sandbox.Output{}, sandbox.NotFound("file not found")
		}
		return sandbox.Output{
			Name:   path.Base(ref.Path),
			FileID: sandbox.EncodeFile(key, sandbox.KeepDir, ref.Path),
			Size:   info.Size(),
		}, nil
	}
	if ref.Kind != sandbox.KindOut && ref.Kind != sandbox.KindFiles {
		return sandbox.Output{}, sandbox.BadRequest("only files/ and out/ entries can be archived")
	}

	uid, drop := r.execUID(key)
	workspace, err := r.ensureWorkspace(identity, uid, drop)
	if err != nil {
		return sandbox.Output{}, sandbox.Internal("create workspace: " + err.Error())
	}
	source, err := secureJoin(workspace, path.Join(ref.Kind, ref.Path))
	if err != nil {
		return sandbox.Output{}, err
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return sandbox.Output{}, sandbox.NotFound("file not found")
	}
	if r.opts.KeepBytes > 0 {
		size, err := dirSize(keepRoot)
		if err == nil && size+info.Size() > r.opts.KeepBytes {
			return sandbox.Output{}, sandbox.Conflict("quota_exceeded",
				fmt.Sprintf("archived files exceed the %d byte keep limit", r.opts.KeepBytes))
		}
	}
	if err := os.MkdirAll(keepRoot, 0o750); err != nil {
		return sandbox.Output{}, sandbox.Internal("create keep directory: " + err.Error())
	}
	if err := secureMkdirAll(keepRoot, path.Dir(ref.Path)); err != nil {
		return sandbox.Output{}, sandbox.BadRequest("invalid archive path")
	}
	destination := filepath.Join(keepRoot, filepath.FromSlash(ref.Path))
	if !isWithin(keepRoot, destination) {
		return sandbox.Output{}, sandbox.BadRequest("invalid archive path")
	}
	if err := copyInto(source, destination); err != nil {
		return sandbox.Output{}, sandbox.Internal("archive file: " + err.Error())
	}
	return sandbox.Output{
		Name:   path.Base(ref.Path),
		FileID: sandbox.EncodeFile(key, sandbox.KeepDir, ref.Path),
		Size:   info.Size(),
	}, nil
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

// DeleteWorkspace removes the mutable workspace. purge=true also drops the
// archived deliverables.
func (r *Runner) DeleteWorkspace(ctx context.Context, identity sandbox.Identity, purge bool) error {
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
	if purge {
		if err := os.RemoveAll(r.keepDir(identity)); err != nil {
			return sandbox.Internal("remove archived files: " + err.Error())
		}
	}
	r.log.Info("workspace deleted", "key", key, "mode", identity.Mode(), "namespace", identity.Namespace, "principal", identity.Principal, "purge", purge)
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
func (r *Runner) collectOutputs(workspace string, since time.Time, key string) []sandbox.Output {
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
			Name:   relative,
			FileID: sandbox.EncodeFile(key, sandbox.KindOut, relative),
			Size:   info.Size(),
		})
		return nil
	})
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Name < outputs[j].Name })
	return outputs
}
