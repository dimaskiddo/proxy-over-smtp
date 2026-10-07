package update

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// oldSuffix names the file a running Windows executable is renamed to during replacement.
const oldSuffix = ".old"

// replace swaps exe for bin atomically. Windows cannot overwrite a running executable
// but can rename it, so the old file is moved aside first.
func replace(exe string, bin []byte) error {
	info, err := os.Stat(exe)
	if err != nil {
		return fmt.Errorf("stat %s: %w", exe, err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(exe), filepath.Base(exe)+".new-*")
	if err != nil {
		return wrapPerm(fmt.Errorf("create temp file: %w", err))
	}

	tmpName := tmp.Name()
	// committed stops the deferred cleanup from deleting the new binary once it is installed.
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if runtime.GOOS == "windows" {
		if err := moveAside(exe); err != nil {
			return err
		}
	}

	if err := os.Rename(tmpName, exe); err != nil {
		if runtime.GOOS == "windows" {
			// Roll back, or the install would leave no executable at exe at all.
			_ = os.Rename(exe+oldSuffix, exe)
		}

		return wrapPerm(fmt.Errorf("install new binary: %w", err))
	}

	committed = true

	return nil
}

// cleanOld removes the file left behind by a previous Windows update. Best effort: it may
// still be locked by a running process.
func cleanOld(exe string) {
	_ = os.Remove(exe + oldSuffix)
}

// moveAside renames the running executable out of the way. A leftover .old that cleanOld could
// not remove makes the first rename fail, so that case removes the stale file and retries once;
// otherwise a second consecutive update would never succeed.
func moveAside(exe string) error {
	err := os.Rename(exe, exe+oldSuffix)
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return wrapPerm(fmt.Errorf("move old binary aside: %w", err))
	}

	if rmErr := os.Remove(exe + oldSuffix); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
		return wrapPerm(fmt.Errorf("move old binary aside: %w", err))
	}

	return wrapPerm(fmt.Errorf("move old binary aside: %w", os.Rename(exe, exe+oldSuffix)))
}

// wrapPerm adds a hint to permission errors, the usual failure when the binary sits in a
// root-owned directory.
func wrapPerm(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w (run with sufficient permissions)", err)
	}

	return err
}
