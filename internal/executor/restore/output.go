package restore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
)

// chooseOutput returns the first conflict name for desired that no other selected version or Library
// original owns and that accept admits; a nil accept admits the first such name.
func (a *jobRestoreRunner) chooseOutput(
	ctx context.Context, itemID int64, desired string, accept func(string) (bool, error),
) (string, error) {
	restored := path.Join(path.Dir(desired), library.RestoredName(path.Base(desired), itemID))
	ext := path.Ext(restored)
	stem := restored[:len(restored)-len(ext)]
	for suffix := 0; ; suffix++ {
		// Choose the next deterministic candidate without continuing a canceled operation.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		candidate := desired
		if suffix == 1 {
			candidate = restored
		}
		if suffix > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, suffix, ext)
		}

		// Only nonempty paths reserve outputs; pending empty rows must not enter this lookup.
		// Match the existing partial index predicate so each candidate avoids a manifest-wide scan.
		var count int64
		if err := a.db.WithContext(ctx).Model(&File{}).
			Where("path = ? AND path <> '' AND item_id <> ?", candidate, itemID).Count(&count).Error; err != nil {
			return "", err
		}
		if count > 0 {
			continue
		}

		// Never choose an output already associated with a Library original.
		if a.destination.GetLocationId() != 0 {
			owner, err := a.exe.Lib().GetFileLocationAtPath(
				ctx, a.destination.LocationId, path.Join(a.destination.Path, candidate),
			)
			if err != nil {
				return "", err
			}
			if owner != nil {
				continue
			}
		}
		if accept == nil {
			return candidate, nil
		}
		accepted, err := accept(candidate)
		if err != nil {
			return "", err
		}
		if accepted {
			return candidate, nil
		}
	}
}

// claimOutput creates the item's output exclusively before ACP replaces it with transferred bytes.
// The filesystem reports any existing entry, including a case- or normalization-equivalent name,
// as EEXIST; the next conflict name is then tried. A path this Job already claimed is kept.
func (a *jobRestoreRunner) claimOutput(ctx context.Context, copy *copyCandidate) error {
	var output File
	if err := a.db.WithContext(ctx).First(&output, "item_id = ?", copy.ItemID).Error; err != nil {
		return err
	}
	if output.ReadMediaID != 0 {
		created, err := a.createOutput(ctx, copy.TargetPath)
		if err != nil || created {
			return err
		}
		if !a.emptyOutput(copy.TargetPath) {
			return &restoreItemError{fmt.Errorf("reserved Restore output changed: %q", copy.TargetPath)}
		}
		return nil
	}
	target, err := a.chooseOutput(ctx, copy.ItemID, output.DesiredPath, func(candidate string) (bool, error) {
		return a.createOutput(ctx, candidate)
	})
	if err != nil {
		return err
	}
	copy.TargetPath = target
	return nil
}

// createOutput reuses existing directories and creates an empty output only when its name is free.
func (a *jobRestoreRunner) createOutput(ctx context.Context, relative string) (bool, error) {
	filename, err := a.exe.RestoreOutputPath(ctx, a.destination, relative)
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(filename)
	if err := os.MkdirAll(parent, os.ModePerm); err != nil {
		return false, &restoreItemError{fmt.Errorf("create Restore output directory failed, path=%q, %w", relative, err)}
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, &restoreItemError{fmt.Errorf("create Restore output failed, path=%q, %w", relative, err)}
	}
	return true, file.Close()
}

// emptyOutput reports whether relative still holds only the empty output that claimOutput creates.
func (a *jobRestoreRunner) emptyOutput(relative string) bool {
	info, err := os.Lstat(a.restoreTarget(relative))
	return err == nil && info.Mode().IsRegular() && info.Size() == 0
}

// releaseOutput removes a still-empty claim after a failed transfer; a retry creates it again.
func (a *jobRestoreRunner) releaseOutput(ctx context.Context, copy *copyCandidate) {
	if copy.Size == 0 || !a.emptyOutput(copy.TargetPath) {
		return
	}
	if err := os.Remove(a.restoreTarget(copy.TargetPath)); err != nil && !os.IsNotExist(err) {
		a.logger.WithContext(ctx).WithError(err).Warnf("remove empty Restore output failed, path=%q", copy.TargetPath)
	}
}

func (a *jobRestoreRunner) outputMatches(ctx context.Context, relative string, hash []byte, size int64) (matched, exists bool, err error) {
	// Invalid destination configuration or unsafe paths stop the attempt before any file-local work.
	filename, err := a.exe.RestoreOutputPath(ctx, a.destination, relative)
	if err != nil {
		return false, false, err
	}

	// A failure inspecting this admitted output must not discard independent candidates.
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, &restoreItemError{fmt.Errorf("inspect existing Restore output failed, path=%q, %w", relative, err)}
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, true, nil
	}

	// Actual-byte verification can fail per file; cancellation still stops the whole attempt.
	facts, err := executor.VerifyObservedContent(ctx, filename)
	if err != nil {
		if stopped := ctx.Err(); stopped != nil {
			return false, true, stopped
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, true, err
		}
		return false, true, &restoreItemError{fmt.Errorf("verify existing Restore output failed, path=%q, %w", relative, err)}
	}
	return bytes.Equal(facts.Sha256, hash) && facts.SizeBytes == size, true, nil
}

// outputUnchanged trusts an output staged by this Job without reading it: verified bytes received
// the selected version's mode/mtime before staging, so an unchanged size, mode and mtime suffice.
// Damaged bytes keep no version metadata and are always verified by content.
func (a *jobRestoreRunner) outputUnchanged(ctx context.Context, copy *copyCandidate, output *File) (bool, error) {
	if output.Damaged || copy.FileVersionID == 0 {
		return false, nil
	}
	filename, err := a.exe.RestoreOutputPath(ctx, a.destination, output.Path)
	if err != nil {
		return false, err
	}
	// The content verification that follows a mismatch reports inspection failures.
	info, err := os.Lstat(filename)
	if err != nil {
		return false, nil
	}
	return info.Mode().IsRegular() && info.Size() == output.ActualSize &&
		info.Mode()&restoredModeBits == fs.FileMode(copy.Mode)&restoredModeBits &&
		info.ModTime().UnixNano() == copy.MtimeNS, nil
}

// restoredModeBits are the mode bits a Restore applies from the selected version.
const restoredModeBits = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

func (a *jobRestoreRunner) restoreMetadata(relative string, mode uint32, mtime int64) error {
	// Metadata belongs to the selected version, independently of the shared archive copy.
	filename := a.restoreTarget(relative)
	permissions := fs.FileMode(mode) & restoredModeBits
	if err := os.Chmod(filename, permissions); err != nil {
		return err
	}
	modified := time.Unix(0, mtime)
	return os.Chtimes(filename, modified, modified)
}
