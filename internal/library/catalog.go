package library

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// ArchiveImportResult summarizes one explicit inventory admission without retaining a tree-sized result.
type ArchiveImportResult struct {
	FileIDs         []int64
	Files           int64
	Directories     int64
	SkippedFiles    int64
	ExistingFiles   int64
	SkippedUnsigned int64
}

func (l *Library) ListContentCopies(ctx context.Context, signature []byte, after int64, limit int) ([]*Position, bool, error) {
	if len(signature) == 0 || after < 0 || limit <= 0 || limit > 1000 {
		return nil, false, fmt.Errorf("invalid content copy page")
	}
	var values []*Position
	if err := l.readDB().WithContext(ctx).Where("signature = ? AND is_dir = ? AND id > ?", signature, false, after).
		Order("id").Limit(limit + 1).Find(&values).Error; err != nil {
		return nil, false, err
	}
	more := len(values) > limit
	if more {
		values = values[:limit]
	}
	return values, more, nil
}

func (l *Library) ListContentDuplicates(ctx context.Context, signature []byte, after int64, limit int) ([]*File, bool, error) {
	if len(signature) == 0 || after < 0 || limit <= 0 || limit > 1000 {
		return nil, false, fmt.Errorf("invalid duplicate content page")
	}
	db := l.readDB().WithContext(ctx)
	originals := db.Model(&FileLocation{}).Select("1").Where("file_locations.file_id = files.id AND signature = ?", signature)
	versions := db.Model(&FileVersion{}).Select("1").Where("file_versions.file_id = files.id AND signature = ?", signature)
	var rows []*fileRow
	if err := db.Where("id > ? AND (EXISTS (?) OR EXISTS (?))", after, originals, versions).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	files := fileViews(rows)
	more := len(files) > limit
	if more {
		files = files[:limit]
	}
	return files, more, nil
}

// ImportArchivePositions explicitly creates independent catalog objects from physical inventory.
// Historical archive time is unknown; observing a copy is not a new backup operation.
func (l *Library) ImportArchivePositions(ctx context.Context, ids []int64) ([]int64, error) {
	result, err := l.ImportArchivePositionRoots(ctx, ids)
	if err != nil {
		return nil, err
	}
	return result.FileIDs, nil
}

// ImportArchivePositionRoots admits the selected roots without a Media scope or a dry run.
func (l *Library) ImportArchivePositionRoots(ctx context.Context, ids []int64) (*ArchiveImportResult, error) {
	return l.ImportArchivePositionSelection(ctx, ids, nil, false)
}

// ImportArchivePositionSelection imports explicit files and the signed regular descendants of selected directories.
// A nil mediaID leaves the selection unrestricted; a set one rejects roots from another Media.
// dryRun reports the same outcomes without creating Files, versions or directory nodes.
func (l *Library) ImportArchivePositionSelection(ctx context.Context, ids []int64, mediaID *int64, dryRun bool) (_ *ArchiveImportResult, rerr error) {
	// Classify candidates and publish only the new Files in the selected ranges.
	result := new(ArchiveImportResult)
	var planned *gorm.DB
	run := func(tx *gorm.DB) error {
		// Resolve the complete request before admitting any candidate.
		roots, err := resolveArchiveImportRoots(tx, ids, mediaID)
		if err != nil {
			return err
		}
		admit := func(tx *gorm.DB, media *Media, copy *Position) error {
			// Existing candidates contribute only to the report, never another suffixed File.
			create, err := countArchiveImport(tx, planned, media, copy, result)
			if err != nil {
				return err
			}
			if dryRun || !create {
				return nil
			}

			// Publish the classified new File and its saved content together.
			file, err := importArchivePosition(tx, media, copy)
			if err != nil {
				return err
			}
			result.FileIDs = append(result.FileIDs, file)
			return nil
		}

		// Import every selected range in stable path pages within one authoritative metadata transaction.
		media := make(map[int64]*Media)
		for _, root := range roots {
			stored := media[root.MediaID]
			if stored == nil {
				stored = new(Media)
				if err := tx.First(stored, root.MediaID).Error; err != nil {
					return fmt.Errorf("load archive Media failed, id=%d, %w", root.MediaID, err)
				}
				if _, err := archiveImportDirectory(stored); err != nil {
					return err
				}
				media[root.MediaID] = stored
			}
			if !root.IsDir {
				if len(root.Signature) == 0 {
					return fmt.Errorf("Position %d has no archived content identity", root.ID)
				}
				if err := admit(tx, stored, root); err != nil {
					return err
				}
				continue
			}
			// A selected directory with no recorded content admits nothing and is reported, not mirrored.
			before := result.Files + result.ExistingFiles
			if err := importArchiveDirectory(ctx, tx, stored, root.Path, result, admit); err != nil {
				return err
			}
			if result.Files+result.ExistingFiles == before {
				result.SkippedUnsigned++
			}
		}
		return nil
	}

	// Reports leave the catalog untouched; real admission rolls back on any failed candidate.
	if dryRun {
		stage, closeStage, err := openArchiveImportStage(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { rerr = errors.Join(rerr, closeStage()) }()
		planned = stage
		return result, l.readDB().WithContext(ctx).Transaction(run)
	}
	if err := l.db.WithContext(ctx).Transaction(run); err != nil {
		return nil, err
	}
	return result, nil
}

// resolveArchiveImportRoots resolves the bounded selection, or a Media's top-level directories when none is given.
func resolveArchiveImportRoots(tx *gorm.DB, ids []int64, mediaID *int64) ([]*Position, error) {
	ids = uniqueFileIDs(ids)
	if mediaID != nil {
		if *mediaID <= 0 {
			return nil, fmt.Errorf("import Media ID is invalid, value=%d", *mediaID)
		}
		if len(ids) == 0 {
			var roots []*Position
			if err := tx.Where("media_id = ? AND is_dir = ? AND parent_path = ?", *mediaID, true, "").
				Order("path").Find(&roots).Error; err != nil {
				return nil, fmt.Errorf("load Media import roots failed, media_id=%d, %w", *mediaID, err)
			}
			if len(roots) == 0 {
				return nil, fmt.Errorf("Media %d has no recorded top-level directory to import", *mediaID)
			}
			return roots, nil
		}
	}
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, fmt.Errorf("select between 1 and 1000 archive positions")
	}

	// Resolve and order the bounded selection before pruning descendants of selected directories.
	var roots []*Position
	if err := tx.Where("id IN ?", ids).Find(&roots).Error; err != nil {
		return nil, fmt.Errorf("load archive position selection failed, %w", err)
	}
	if len(roots) != len(ids) {
		return nil, fmt.Errorf("archive position selection changed; refresh Media and try again")
	}
	if mediaID != nil {
		for _, root := range roots {
			if root.MediaID != *mediaID {
				return nil, fmt.Errorf("selected Position %d belongs to another Media", root.ID)
			}
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].MediaID != roots[j].MediaID {
			return roots[i].MediaID < roots[j].MediaID
		}
		return roots[i].Path < roots[j].Path
	})
	return pruneArchiveImportRoots(roots), nil
}

// countArchiveImport classifies one candidate without changing it, so a dry run and the real
// admission report the same numbers. Repeated admission of recorded content is skipped.
func countArchiveImport(tx, planned *gorm.DB, media *Media, copy *Position, result *ArchiveImportResult) (bool, error) {
	// Resolve whether the Media's logical import path is already occupied.
	directory, err := archiveImportDirectory(media)
	if err != nil {
		return false, err
	}
	logicalPath := path.Join("Unforged", directory, copy.Path)
	admitted, nodes, err := countImportedPath(tx, planned, logicalPath)
	if err != nil {
		return false, err
	}
	if admitted {
		result.ExistingFiles++
		return false, nil
	}

	// Count and admit only a new path, preserving the dry-run classification.
	result.Files++
	result.Directories += nodes
	return true, nil
}

func pruneArchiveImportRoots(roots []*Position) []*Position {
	result := make([]*Position, 0, len(roots))
	var mediaID int64
	var directory string
	for _, root := range roots {
		if root.MediaID != mediaID {
			mediaID, directory = root.MediaID, ""
		}
		if directory != "" && strings.HasPrefix(root.Path, directory) {
			continue
		}
		result = append(result, root)
		if root.IsDir {
			directory = archiveDirectoryPrefix(root.Path)
		} else {
			directory = ""
		}
	}
	return result
}

func archiveImportDirectory(media *Media) (string, error) {
	// Tape and unnamed Volume imports retain their established identity-based directory.
	name := media.Name
	if media.Kind == entity.MediaKind_MEDIA_KIND_TAPE || name == "" {
		name = media.Identity
	}

	// The chosen text is now a real File component, without display-label normalization.
	if err := entity.ValidatePathComponent(name); err != nil {
		return "", fmt.Errorf("invalid Media import directory %q, %w", name, err)
	}
	return name, nil
}

func importArchivePosition(tx *gorm.DB, media *Media, copy *Position) (int64, error) {
	name, err := archiveImportDirectory(media)
	if err != nil {
		return 0, err
	}
	file, err := createImportedFile(tx, path.Join("Unforged", name, copy.Path))
	if err != nil {
		return 0, err
	}
	if _, err := recordVersion(tx, &FileVersion{FileID: file.ID, Signature: copy.Signature, Hash: copy.Hash, Size: copy.Size,
		Mode: copy.Mode, MtimeNS: copy.MtimeNS}); err != nil {
		return 0, err
	}
	return file.ID, nil
}

// importArchiveFile admits one recorded Position or only accounts for it during a dry run.
type importArchiveFile func(tx *gorm.DB, media *Media, copy *Position) error

func importArchiveDirectory(ctx context.Context, tx *gorm.DB, media *Media, directory string, result *ArchiveImportResult, admit importArchiveFile) error {
	after := ""
	prefix := archiveDirectoryPrefix(directory)
	pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(prefix) + "%"
	for {
		var copies []*Position
		if err := tx.WithContext(ctx).Where("media_id = ? AND is_dir = ? AND path > ? AND path LIKE ? ESCAPE '!'",
			media.ID, false, after, pattern).Order("path").Limit(batchSize).Find(&copies).Error; err != nil {
			return fmt.Errorf("list archive directory failed, media_id=%d path=%q, %w", media.ID, directory, err)
		}
		if len(copies) == 0 {
			return nil
		}
		for _, copy := range copies {
			if len(copy.Signature) == 0 {
				result.SkippedFiles++
				continue
			}
			if err := admit(tx, media, copy); err != nil {
				return err
			}
		}
		after = copies[len(copies)-1].Path
	}
}

func archiveDirectoryPrefix(directory string) string {
	if directory == "" || strings.HasSuffix(directory, "/") {
		return directory
	}
	return directory + "/"
}

// ImportArchivedInventory is reserved for explicit inventory-import workflows and fixtures.
// Archive and Volume Scan must not invoke this to infer logical identity from signatures.
// Each invocation creates new organization; callers select this one-shot import explicitly.
func (l *Library) ImportArchivedInventory(ctx context.Context) error {
	var after int64
	for {
		var ids []int64
		if err := l.readDB().WithContext(ctx).Model(ModelPosition).Where("id > ? AND is_dir = ?", after, false).
			Where("signature IS NOT NULL").Order("id").Limit(batchSize).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if _, err := l.ImportArchivePositions(ctx, ids); err != nil {
			return err
		}
		after = ids[len(ids)-1]
	}
}

// CreateArchiveFile allocates a raw input's logical identity before any physical transfer.
func (l *Library) CreateArchiveFile(ctx context.Context, targetPath string) (*File, error) {
	if err := entity.ValidateRelativePath(targetPath); err != nil {
		return nil, err
	}
	var result *File
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = createImportedFile(tx, path.Join("Unforged", "Archive", targetPath))
		return err
	})
	return result, err
}
