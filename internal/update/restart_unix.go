//go:build !windows

package update

import (
	"fmt"
	"os"
	"syscall"
)

// Restart replaces the current process image with the on-disk binary, keeping the PID.
func Restart() error {
	// Run the same path update replaced: a symlinked install must exec the target, not the link.
	exe, err := ExecutablePath()
	if err != nil {
		return err
	}

	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", exe, err)
	}

	return nil
}
