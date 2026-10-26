package build

import (
	"os"
	"os/exec"
)

// processAlive reports whether a process with this id exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}

// signalExitCode returns a shell's exit status for a process a signal
// ended; Windows reports exit codes only.
func signalExitCode(err *exec.ExitError) int { return -1 }
