package fileops

import (
	"errors"
	"io/fs"
	"os"
)

func renameNoReplace(source, target string) error {
	// Reject an observed conflict at the primitive boundary as well as in planning.
	if _, err := os.Lstat(target); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	// FreeBSD's ordinary rename can replace a destination created after that check.
	// This experimental target provides no atomic no-replace guarantee or
	// copy/delete fallback.
	return os.Rename(source, target)
}
