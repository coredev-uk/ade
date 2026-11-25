package workspace

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// processStart returns the kernel start time (clock ticks since boot) of pid,
// read from /proc, or 0 where /proc is unavailable. Paired with the pid it
// identifies one process even after the pid is recycled.
func processStart(pid int) uint64 {
	_, start, _ := procStat(pid)
	return start
}

// haveProc reports whether /proc is mounted (Linux), so a missing
// /proc/<pid> means the process is gone rather than that /proc is unavailable.
var haveProc = sync.OnceValue(func() bool {
	_, err := os.Stat("/proc/self/stat")
	return err == nil
})

// procStat parses /proc/<pid>/stat into the state letter and start time.
// gone says /proc is mounted but has no entry for pid: the process was reaped.
func procStat(pid int) (state byte, start uint64, gone bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, errors.Is(err, fs.ErrNotExist) && haveProc()
	}
	// "pid (comm) S ppid ... starttime ..." where comm may contain spaces and parens.
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return 0, 0, false
	}
	start, _ = strconv.ParseUint(f[19], 10, 64)
	return f[0][0], start, false
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
	// kill(2) still finds a zombie; when its parent reaps it before /proc is
	// read, the entry is gone, which must not read as "start time unknown".
	state, start, gone := procStat(pid)
	if gone || state == 'Z' || state == 'X' {
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

// ProcessStart is the kernel start time of pid (0 where /proc is unavailable),
// for callers that record a process and must tell it from a later one that
// reuses the pid.
func ProcessStart(pid int) uint64 { return processStart(pid) }

// ProcessAlive reports whether pid is the live, non-zombie process recorded
// with start time pstart (0 skips the identity check).
func ProcessAlive(pid int, pstart uint64) bool { return processAlive(pid, pstart) }
