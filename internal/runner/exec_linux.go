//go:build linux

package runner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// In the container the runner starts as root so it can hand each identity its
// own UID. Outside such a container it degrades to running as the caller.
func canDropPrivileges() bool { return os.Geteuid() == 0 }

func currentUID() int { return os.Geteuid() }

func isWindows() bool { return false }

func statUID(info os.FileInfo) int {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid)
	}
	return -1
}

func baseEnv(_ string) []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
	}
}

func commandFor(uid int, drop bool, dir, command string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = commandEnv(dir)
	// A dedicated process group lets us kill the whole tree on timeout
	// without touching another identity's processes.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if drop {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid:    uint32(uid),
			Gid:    uint32(uid),
			Groups: []uint32{},
		}
	}
	return cmd
}

func killProcessTree(pid int) error {
	groupErr := syscall.Kill(-pid, syscall.SIGKILL)
	if groupErr == nil || errors.Is(groupErr, syscall.ESRCH) {
		return nil
	}
	processErr := syscall.Kill(pid, syscall.SIGKILL)
	if processErr == nil || errors.Is(processErr, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("kill process group %d: %v; kill process: %v", pid, groupErr, processErr)
}

// killUIDProcesses kills every process owned by uid. A recycled UID must not
// inherit daemons a previous identity left behind with setsid or nohup.
func killUIDProcesses(uid int) error {
	if uid <= 0 {
		return nil
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		pids := pidsForUID(uid)
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				return fmt.Errorf("kill pid %d: %w", pid, err)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d process(es) still running as uid %d", len(pids), uid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func pidsForUID(uid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
		if readErr != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.HasPrefix(line, "Uid:") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[1] == strconv.Itoa(uid) {
				pids = append(pids, pid)
			}
			break
		}
	}
	return pids
}

// missingCapabilities reports the Linux capabilities the runner expects in
// order to chown workspaces, drop to another UID and kill that UID's
// processes. A missing /proc entry is treated as "all missing" so startup
// fails closed.
func missingCapabilities() []string {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return requiredCapabilityNames()
	}
	return parseMissingCapabilities(string(data))
}
