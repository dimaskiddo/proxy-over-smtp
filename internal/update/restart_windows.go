//go:build windows

package update

import (
	"fmt"
	"os"
	"os/exec"
)

// Restart starts the on-disk binary as a child. Windows has no exec, so the caller exits afterwards.
func Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", exe, err)
	}

	return nil
}
