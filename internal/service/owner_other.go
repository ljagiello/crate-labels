//go:build !darwin

package service

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processOwner identifies this process as "<pid>:0" (no start time on this platform).
func processOwner() string { return strconv.Itoa(os.Getpid()) + ":0" }

// ownerAlive reports whether the pid in owner still runs. An empty or malformed owner
// counts as dead.
func ownerAlive(owner string) bool {
	pidText, _, ok := strings.Cut(owner, ":")
	pid, err := strconv.Atoi(pidText)
	if !ok || err != nil || pid <= 0 {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
