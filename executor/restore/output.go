package restore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"time"

	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

func (a *jobRestoreRunner) reserveOutput(ctx context.Context, names *outputNames, version *library.FileVersion, desired string) (string, bool, error) {
	// An already chosen output survives RetryIndex and never grows a second suffix on retry.
	var stored Output
	err := a.db.WithContext(ctx).First(&stored, "item_id = ?", version.ID).Error
	if err == nil {
		if stored.ReadMediaID != 0 {
			return stored.Path, false, nil
		}
		if a.destination.GetLocationId() != 0 {
			result, err := a.exe.Lib().GetRestoreResult(ctx, a.operationID, version.ID)
			if err != nil {
				return "", false, err
			}
			if result != nil {
				return stored.Path, false, nil
			}
			owner, err := a.exe.Lib().GetFileLocationAtPath(ctx, a.destination.LocationId, path.Join(a.destination.Path, stored.Path))
			if err != nil {
				return "", false, err
			}
			if owner != nil {
				return "", false, fmt.Errorf("reserved Restore output is already linked to File %d", owner.FileID)
			}
		}
		matched, exists, err := a.outputMatches(ctx, stored.Path, version.Hash, version.Size)
		if err != nil {
			return "", false, err
		}
		if exists && !matched {
			return "", false, fmt.Errorf("reserved Restore output changed: %q", stored.Path)
		}
		return stored.Path, matched, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, err
	}

	// Resolve both filesystem collisions and other selected versions without replacing existing bytes.
	restored := path.Join(path.Dir(desired), library.RestoredName(path.Base(desired), version.ID))
	ext := path.Ext(restored)
	stem := restored[:len(restored)-len(ext)]
	for suffix := 0; ; suffix++ {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		candidate := desired
		if suffix == 1 {
			candidate = restored
		}
		if suffix > 1 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, suffix, ext)
		}
		var count int64
		if err := a.db.WithContext(ctx).Model(&Output{}).Where("path = ?", candidate).Count(&count).Error; err != nil {
			return "", false, err
		}
		if count > 0 {
			continue
		}
		if a.destination.GetLocationId() != 0 {
			owner, err := a.exe.Lib().GetFileLocationAtPath(ctx, a.destination.LocationId, path.Join(a.destination.Path, candidate))
			if err != nil {
				return "", false, err
			}
			if owner != nil {
				continue
			}
		}
		matched, exists, err := a.outputMatches(ctx, candidate, version.Hash, version.Size)
		if err != nil {
			return "", false, err
		}
		if exists && !matched {
			continue
		}
		claimed, err := names.claim(ctx, candidate)
		if err != nil {
			return "", false, err
		}
		if !claimed {
			continue
		}
		if err := a.db.WithContext(ctx).Create(&Output{ItemID: version.ID, Path: candidate}).Error; err != nil {
			return "", false, err
		}
		return candidate, matched, nil
	}
}

func (a *jobRestoreRunner) outputMatches(ctx context.Context, relative string, hash []byte, size int64) (matched, exists bool, err error) {
	// A previous output is usable only after actual-byte verification through ACP.
	filename, err := a.exe.RestoreOutputPath(ctx, a.destination, relative)
	if err != nil {
		return false, false, err
	}
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.Mode().IsRegular() || info.Size() != size {
		return false, true, nil
	}
	facts, err := executor.VerifyObservedContent(ctx, filename, info)
	if err != nil {
		return false, true, err
	}
	return bytes.Equal(facts.Sha256, hash) && facts.Size == size, true, nil
}

func (a *jobRestoreRunner) restoreMetadata(relative string, mode uint32, mtime int64) error {
	// Metadata belongs to the selected version, independently of the shared archive copy.
	filename := a.restoreTarget(relative)
	permissions := fs.FileMode(mode) & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)
	if err := os.Chmod(filename, permissions); err != nil {
		return err
	}
	modified := time.Unix(0, mtime)
	return os.Chtimes(filename, modified, modified)
}
