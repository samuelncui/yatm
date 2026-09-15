package library

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

// ValidateRestoreVersionSelection rejects policy combinations that cannot express user consent.
func ValidateRestoreVersionSelection(policy *entity.RestoreVersionPolicy, skip bool) error {
	if policy.GetBeforeAtMs() < 0 {
		return fmt.Errorf("restore cutoff must be a nonnegative Unix millisecond timestamp")
	}
	if skip && (policy == nil || policy.BeforeAtMs == nil) {
		return fmt.Errorf("skipping unmatched versions requires a restore cutoff")
	}
	return nil
}

// RestoreSelectionItem is one resolved output or one explicitly reported policy mismatch.
type RestoreSelectionItem struct {
	File       *File
	Version    *FileVersion
	Resolution *entity.RestoreVersionResolution
	Path       string
	Direct     bool
}

// WalkRestoreSelections shares version overrides and bounded expansion between estimates and indexing.
func (l *Library) WalkRestoreSelections(ctx context.Context, selections []*entity.FileSelection, ids []int64,
	policy *entity.RestoreVersionPolicy, yield func(*RestoreSelectionItem) error,
) error {
	// Retain only explicit inputs; directory descendants use the existing paged walker.
	if len(selections)+len(ids) == 0 || len(selections)+len(ids) > 1000 {
		return fmt.Errorf("Restore requires between 1 and 1000 selection roots or versions")
	}
	if err := ValidateRestoreVersionSelection(policy, false); err != nil {
		return err
	}
	overridden := make(map[int64]struct{}, len(ids))
	handled := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if _, found := handled[id]; found {
			continue
		}
		version, err := l.GetFileVersion(ctx, id)
		if err != nil {
			return err
		}
		file, err := l.GetFile(ctx, version.FileID)
		if err != nil {
			return err
		}
		paths, err := l.resolveFilePaths(ctx, []*File{file})
		if err != nil {
			return err
		}
		item := &RestoreSelectionItem{File: file, Version: version, Path: strings.TrimPrefix(paths[file.ID], "/"), Direct: true,
			Resolution: &entity.RestoreVersionResolution{FileId: file.ID, Version: version.ToEntity(),
				ArchivedAtMs: version.LastArchivedAt, Match: entity.RestoreVersionMatch_RESTORE_VERSION_MATCHED}}
		if err := yield(item); err != nil {
			return err
		}
		handled[id] = struct{}{}
		overridden[file.ID] = struct{}{}
	}

	// Mark only explicitly selected regular roots for the bounded review response.
	direct := make(map[int64]struct{}, len(selections))
	for _, selection := range selections {
		if selection == nil {
			return fmt.Errorf("File selection is missing")
		}
		if target := selection.GetLibrary(); target != nil && target.FileId != 0 {
			direct[target.FileId] = struct{}{}
		}
		if target := selection.GetLocation(); target != nil && target.Path != "" {
			original, err := l.GetFileLocationAtPath(ctx, target.LocationId, target.Path)
			if err != nil {
				return err
			}
			if original != nil {
				direct[original.FileID] = struct{}{}
			}
		}
	}

	// Explicit versions replace the automatic policy for their File, not merely the same version ID.
	return l.WalkFileSelections(ctx, selections, func(file *File, target string) error {
		if _, found := overridden[file.ID]; found {
			return nil
		}
		resolution, err := l.ResolveRestoreVersion(ctx, file.ID, policy)
		if err != nil {
			return err
		}
		_, isDirect := direct[file.ID]
		item := &RestoreSelectionItem{File: file, Resolution: resolution, Path: target, Direct: isDirect}
		if version := resolution.Version; version != nil {
			item.Version = &FileVersion{ID: version.Id, FileID: version.FileId, Signature: version.Signature,
				Hash: version.Sha256, Size: version.Size, Mode: version.Mode, MtimeNS: version.MtimeNs,
				FirstArchivedAt: version.FirstArchivedAtMs, LastArchivedAt: version.LastArchivedAtMs}
		}
		return yield(item)
	})
}
