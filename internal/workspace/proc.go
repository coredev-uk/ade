package workspace

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processStart returns the kernel start time (clock ticks since boot) of pid,
// read from /proc, or 0 where /proc is unavailable. Paired with the pid it
// identifies one process even after the pid is recycled.
func processStart(pid int) uint64 {
	_, start := procStat(pid)
	return start
}

// procStat parses /proc/<pid>/stat into the state letter and start time.
func procStat(pid int) (state byte, start uint64) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0
	}
	// "pid (comm) S ppid ... starttime ..." where comm may contain spaces and parens.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, 0
	}
	start, _ = strconv.ParseUint(f[19], 10, 64)
	return f[0][0], start
}

// processAlive reports whether pid is the live, non-zombie process that was
// recorded with start time pstart (0 skips the identity check).
func processAlive(pid int, pstart uint64) bool {
	if pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	state, start := procStat(pid)
	if state == 'Z' || state == 'X' {
		return false
	}
	return pstart == 0 || start == 0 || start == pstart
}

// stopGroup ends the process group led by pid: SIGTERM, then SIGKILL after
// grace. Instances start in their own session, so the group is the instance
// and everything it spawned.
func stopGroup(pid int, pstart uint64, grace time.Duration) {
	if !processAlive(pid, pstart) {
		return
	}
	syscall.Kill(-pid, syscall.SIGTERM) //nolint:errcheck // already gone is fine
	if waitGone(pid, pstart, grace) {
		return
	}
	syscall.Kill(-pid, syscall.SIGKILL) //nolint:errcheck
	waitGone(pid, pstart, 2*time.Second)
}

func waitGone(pid int, pstart uint64, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !processAlive(pid, pstart) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return !processAlive(pid, pstart)
}
