//go:build !windows

package build

import (
	"errors"
	"os/exec"
	"syscall"
)

// processAlive reports whether a process with this id exists.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// signalExitCode returns a shell's exit status for a process a signal
// ended (128 plus the signal's number), or -1.
func signalExitCode(err *exec.ExitError) int {
	if ws, ok := err.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return -1
}
