//go:build darwin

package service

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// processStart is a process's start time in microseconds since the epoch, from the
// kernel's process table, or 0 if it cannot be read.
func processStart(pid int) int64 {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return 0
	}
	t := kp.Proc.P_starttime
	return t.Sec*1_000_000 + int64(t.Usec)
}

// processOwner identifies this process as "<pid>:<start µs>", so a reused pid is not
// mistaken for it later.
func processOwner() string {
	pid := os.Getpid()
	return strconv.Itoa(pid) + ":" + strconv.FormatInt(processStart(pid), 10)
}

// ownerAlive reports whether the process that wrote owner still runs. An empty or
// malformed owner counts as dead.
func ownerAlive(owner string) bool {
	pidText, startText, ok := strings.Cut(owner, ":")
	pid, err1 := strconv.Atoi(pidText)
	start, err2 := strconv.ParseInt(startText, 10, 64)
	if !ok || err1 != nil || err2 != nil || pid <= 0 {
		return false
	}
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false // ESRCH: no such process
	}
	if start == 0 {
		return true // the start time was unknown when it was recorded
	}
	now := processStart(pid)
	return now == 0 || now == start
}
