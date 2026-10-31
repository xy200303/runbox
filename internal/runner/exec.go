package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xy200303/runbox/sdk/go"
)

// Exec runs one command as the identity's UID and collects files written to
// out/ during the call. A non-zero exit code is a normal result.
func (r *Runner) Exec(ctx context.Context, identity sandbox.Identity, command string, inputs []string, timeout time.Duration) (*sandbox.Result, error) {
	key, apiErr := r.key(identity)
	if apiErr != nil {
		return nil, apiErr
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, sandbox.BadRequest("command is required")
	}
	if int64(len(command)) > r.opts.MaxCommandBytes {
		return nil, sandbox.BadRequest(fmt.Sprintf("command exceeds the %d byte limit", r.opts.MaxCommandBytes))
	}
	if len(inputs) > r.opts.MaxInputs {
		return nil, sandbox.BadRequest(fmt.Sprintf("too many inputs (limit %d)", r.opts.MaxInputs))
	}
	for _, input := range inputs {
		ref, err := sandbox.DecodeFile(input)
		if err != nil {
			return nil, err
		}
		if ref.Key != key {
			return nil, sandbox.BadRequest("attachment belongs to another workspace")
		}
	}

	lock := r.lockFor(key)
	lock.Lock()
	defer lock.Unlock()

	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	default:
		return nil, sandbox.Conflict("busy", "runner is at capacity, try again shortly")
	}

	uid, drop, err := r.execUID(identity)
	if err != nil {
		return nil, err
	}
	workspace, err := r.ensureWorkspace(identity, uid, drop)
	if err != nil {
		return nil, sandbox.Internal("create workspace: " + err.Error())
	}
	if r.opts.QuotaBytes > 0 {
		if size, sizeErr := dirSize(workspace); sizeErr == nil && size > r.opts.QuotaBytes {
			return nil, sandbox.Conflict("quota_exceeded",
				fmt.Sprintf("workspace exceeds the %d byte quota; clean out/ or old files", r.opts.QuotaBytes))
		}
	}
	if drop {
		if err := chownTreeIfNeeded(workspace, uid); err != nil {
			return nil, sandbox.Internal("prepare workspace ownership: " + err.Error())
		}
	}

	if timeout <= 0 {
		timeout = r.opts.DefaultTimeout
	}
	if r.opts.MaxTimeout > 0 && timeout > r.opts.MaxTimeout {
		timeout = r.opts.MaxTimeout
	}
	started := time.Now()
	outcome, runErr := runCommand(ctx, runSpec{
		uid:       uid,
		drop:      drop,
		dir:       workspace,
		command:   command,
		timeout:   timeout,
		maxStdout: r.opts.MaxStdoutBytes,
	})
	if runErr != nil {
		return nil, runErr
	}
	output := outcome.stdout
	if outcome.stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += outcome.stderr
	}
	return &sandbox.Result{
		ExitCode:   outcome.exitCode,
		Stdout:     sandbox.TruncateTail(output, r.opts.MaxStdoutBytes),
		Files:      r.collectOutputs(workspace, started.Add(-time.Second), key),
		DurationMS: outcome.duration.Milliseconds(),
	}, nil
}

func chownTreeIfNeeded(root string, uid int) error {
	needs := false
	for _, candidate := range []string{root, filepath.Join(root, sandbox.KindFiles), filepath.Join(root, sandbox.KindOut)} {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if statUID(info) != uid {
			needs = true
			break
		}
	}
	if !needs {
		return nil
	}
	return filepath.WalkDir(root, func(current string, _ os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if chownErr := os.Lchown(current, uid, uid); chownErr != nil {
			return chownErr
		}
		return nil
	})
}

type runSpec struct {
	uid       int
	drop      bool
	dir       string
	command   string
	timeout   time.Duration
	maxStdout int
}

type runOutcome struct {
	exitCode int
	stdout   string
	stderr   string
	duration time.Duration
}

func runCommand(ctx context.Context, spec runSpec) (*runOutcome, *sandbox.Error) {
	stdout := newCappedBuffer(spec.maxStdout)
	stderr := newCappedBuffer(spec.maxStdout)
	cmd := commandFor(spec.uid, spec.drop, spec.dir, spec.command)
	// If a daemonized grandchild keeps the output pipe open after the process
	// group is killed, Stop waiting and let Wait return.
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, sandbox.Internal("start command: " + err.Error())
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(spec.timeout)
	defer timer.Stop()

	var runErr error
	var killErr error
	timedOut := false
	select {
	case runErr = <-done:
	case <-ctx.Done():
		killErr = killProcessTree(cmd.Process.Pid)
		waitForExit(done)
		message := "request canceled"
		if killErr != nil {
			message += "; failed to kill command: " + killErr.Error()
		}
		return nil, sandbox.NewError(408, "canceled", message)
	case <-timer.C:
		timedOut = true
		killErr = killProcessTree(cmd.Process.Pid)
		waitForExit(done)
	}
	outcome := &runOutcome{
		stdout:   stdout.String(),
		stderr:   stderr.String(),
		duration: time.Since(started),
	}
	if timedOut {
		if killErr != nil {
			return nil, sandbox.Conflict("timeout", fmt.Sprintf("command timed out after %s; failed to kill it: %v", spec.timeout, killErr))
		}
		return nil, sandbox.Conflict("timeout", fmt.Sprintf("command timed out after %s", spec.timeout))
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			outcome.exitCode = exitErr.ExitCode()
			return outcome, nil
		}
		return nil, sandbox.Internal("run command: " + runErr.Error())
	}
	return outcome, nil
}

// waitForExit gives the killed process tree a moment to release the output
// pipes. On platforms without process groups a grandchild can keep the pipe
// open; blocking forever would leak a request goroutine.
func waitForExit(done <-chan error) {
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// commandEnv builds a deliberately small environment. The runner's own
// environment contains the API key and must never be visible to commands.
func commandEnv(dir string) []string {
	values := map[string]string{}
	for _, entry := range baseEnv(dir) {
		if key, value, ok := strings.Cut(entry, "="); ok {
			values[key] = value
		}
	}
	workspacePath := filepath.Join(dir, ".local", "bin") + string(os.PathListSeparator) +
		filepath.Join(dir, ".npm-global", "bin")
	values["PATH"] = workspacePath + string(os.PathListSeparator) + values["PATH"]
	values["HOME"] = dir
	if _, ok := values["USERPROFILE"]; ok || isWindows() {
		values["USERPROFILE"] = dir
	}
	values["LANG"] = "C.UTF-8"
	values["LC_ALL"] = "C.UTF-8"
	values["PIP_USER"] = "1"
	values["PIP_BREAK_SYSTEM_PACKAGES"] = "1"
	values["PIP_DISABLE_PIP_VERSION_CHECK"] = "1"
	values["PYTHONUSERBASE"] = filepath.Join(dir, ".local")
	values["PYTHONUNBUFFERED"] = "1"
	values["PYTHONDONTWRITEBYTECODE"] = "1"
	values["PYTHONIOENCODING"] = "UTF-8"
	values["NPM_CONFIG_PREFIX"] = filepath.Join(dir, ".npm-global")
	values["NPM_CONFIG_CACHE"] = filepath.Join(dir, ".npm-cache")
	values["NPM_CONFIG_UPDATE_NOTIFIER"] = "false"
	values["NPM_CONFIG_FUND"] = "false"
	values["NPM_CONFIG_AUDIT"] = "false"
	values["GIT_TERMINAL_PROMPT"] = "0"
	values["XDG_CACHE_HOME"] = filepath.Join(dir, ".cache")
	values["XDG_CONFIG_HOME"] = filepath.Join(dir, ".config")
	values["XDG_DATA_HOME"] = filepath.Join(dir, ".local", "share")
	tempDir := filepath.Join(dir, ".tmp")
	values["TMPDIR"] = tempDir
	values["TEMP"] = tempDir
	values["TMP"] = tempDir

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

// cappedBuffer keeps the first and last half of a stream up to limit bytes.
// It bounds memory and stays O(1) per byte even when a command prints
// gigabytes: the tail is a ring, not a slice that is recopied on every write.
type cappedBuffer struct {
	mu    sync.Mutex
	limit int
	head  []byte
	tail  []byte
	pos   int
	count int
	total int64
}

func newCappedBuffer(limit int) *cappedBuffer {
	if limit < 2 {
		limit = 2
	}
	return &cappedBuffer{limit: limit, tail: make([]byte, limit/2)}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(written)
	half := b.limit / 2
	if len(b.head) < half {
		take := half - len(b.head)
		if take > len(p) {
			take = len(p)
		}
		b.head = append(b.head, p[:take]...)
		p = p[take:]
	}
	for len(p) > 0 {
		copied := copy(b.tail[b.pos:], p)
		b.pos = (b.pos + copied) % half
		p = p[copied:]
		if b.count < half {
			b.count += copied
			if b.count > half {
				b.count = half
			}
		}
	}
	return written, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	half := b.limit / 2
	var tail []byte
	if b.count < half {
		tail = b.tail[:b.count]
	} else {
		tail = append(append([]byte{}, b.tail[b.pos:]...), b.tail[:b.pos]...)
	}
	kept := append(append([]byte{}, b.head...), tail...)
	if b.total <= int64(b.limit) {
		return string(kept)
	}
	return fmt.Sprintf("%s\n…(%d bytes truncated)…\n%s", b.head, b.total-int64(len(kept)), tail)
}
