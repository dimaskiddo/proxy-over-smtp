//go:build !windows

package update

import (
	"fmt"
	"os"
	"syscall"
)

// Restart replaces the current process image with the on-disk binary, keeping the PID.
func Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", exe, err)
	}

	return nil
}
