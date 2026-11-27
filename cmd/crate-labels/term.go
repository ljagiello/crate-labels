//go:build darwin

package main

import (
	"bufio"
	"os"

	"golang.org/x/sys/unix"
)

// readHidden reads one line from a terminal with echo off.
func readHidden(f *os.File) (string, error) {
	fd := int(f.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return "", errNotTerminal
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &quiet); err != nil {
		return "", errNotTerminal
	}
	defer func() { _ = unix.IoctlSetTermios(fd, unix.TIOCSETA, old) }()
	return bufio.NewReader(f).ReadString('\n')
}
