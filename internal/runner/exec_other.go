//go:build !linux

package runner

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// UID isolation requires Linux. Other platforms run commands as the current
// user and are meant for development and tests only.
func canDropPrivileges() bool { return false }

func missingCapabilities() []string { return nil }

func currentUID() int { return -1 }

func isWindows() bool { return runtime.GOOS == "windows" }

func statUID(os.FileInfo) int { return -1 }

// killUIDProcesses has no meaning without per-identity UIDs.
func killUIDProcesses(int) error { return nil }

func baseEnv(_ string) []string {
	if runtime.GOOS != "windows" {
		return []string{
			"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
			"LANG=C.UTF-8",
		}
	}
	// cmd.exe needs a handful of Windows variables; everything else in the
	// runner process (including RUNBOX_API_KEY) stays out of the command.
	allowed := map[string]bool{
		"PATH": true, "SystemRoot": true, "SystemDrive": true, "ComSpec": true,
		"PATHEXT": true, "WINDIR": true, "TEMP": true, "TMP": true,
		"USERPROFILE": true, "LOCALAPPDATA": true, "APPDATA": true,
		"NUMBER_OF_PROCESSORS": true, "OS": true, "PROCESSOR_ARCHITECTURE": true,
	}
	env := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && allowed[key] {
			env = append(env, entry)
		}
	}
	return env
}

func commandFor(_ int, _ bool, dir, command string, extraEnv []string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("/bin/sh", "-c", command)
	}
	cmd.Dir = dir
	cmd.Env = commandEnv(dir, extraEnv)
	return cmd
}

func killProcessTree(pid int) error {
	if runtime.GOOS == "windows" {
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run(); err == nil {
			return nil
		}
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
