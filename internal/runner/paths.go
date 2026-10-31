package runner

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/xy200303/runbox/sandbox"
)

// secureJoin resolves rel inside root and guarantees that the result — after
// following symlinks — is still inside root. A command can create symlinks in
// its workspace; without the second check `out/link -> /etc/shadow` would be
// served by the runner, which runs as root.
func secureJoin(root, rel string) (string, error) {
	clean, err := sandbox.NormalizeRel(rel)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	if !isWithin(root, full) {
		return "", sandbox.BadRequest("path escapes the workspace")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", sandbox.NotFound("workspace not found")
	}
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", sandbox.NotFound("file not found")
	}
	if !isWithin(resolvedRoot, resolved) {
		return "", sandbox.BadRequest("path escapes the workspace")
	}
	return resolved, nil
}

func isWithin(base, target string) bool {
	relative, err := filepath.Rel(base, target)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

// secureMkdirAll creates relDir below root one segment at a time, refusing to
// traverse a symlink. os.MkdirAll would silently follow a link planted by the
// sandbox identity and create root-owned directories outside the store.
func secureMkdirAll(root, relDir string) error {
	if relDir == "" || relDir == "." {
		return nil
	}
	clean, err := sandbox.NormalizeRel(relDir)
	if err != nil {
		return err
	}
	current := root
	for _, segment := range strings.Split(clean, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing to follow symlink at %s", current)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", current)
			}
		case os.IsNotExist(err):
			if err := os.Mkdir(current, 0o750); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// ensureSubdir creates rel below root one segment at a time. A user-writable
// workspace means a command can replace files/ or out/ with a symlink; mkdir
// and chmod would then follow it as root. Replace such entries instead.
func ensureSubdir(root, rel string, mode fs.FileMode) (bool, error) {
	clean, err := sandbox.NormalizeRel(rel)
	if err != nil {
		return false, err
	}
	current := root
	created := false
	for _, segment := range strings.Split(clean, "/") {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				if removeErr := os.RemoveAll(current); removeErr != nil {
					return created, removeErr
				}
				if mkErr := os.Mkdir(current, mode); mkErr != nil {
					return created, mkErr
				}
				created = true
			}
		case os.IsNotExist(err):
			if mkErr := os.Mkdir(current, mode); mkErr != nil {
				return created, mkErr
			}
			created = true
		default:
			return created, err
		}
		// Do not chmod existing entries: the workspace is writable by its
		// identity, and chmod follows symlinks. The 0750 workspace root is
		// enough to stop another UID from traversing it.
	}
	return created, nil
}

// copyInto writes source to destination atomically enough for delivery:
// existing entries are unlinked first and the new file is created with
// O_EXCL, so a pre-planted symlink can never be followed.
func copyInto(source, destination string) error {
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
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(destination)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(destination)
		return err
	}
	return os.Chmod(destination, 0o640)
}
