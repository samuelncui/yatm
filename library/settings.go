package library

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// LibrarySettings controls presentation, never identity, retention or authorization.
type LibrarySettings struct {
	ID                     int64 `gorm:"primaryKey;autoIncrement:false"`
	IncludeUnbackedFiles   bool
	AutoCollectFiles       bool `gorm:"not null;default:true"`
	ConfirmPermanentDelete bool `gorm:"not null;default:true"`
	Revision               int64
	CreatedAt              int64 `gorm:"autoCreateTime:milli"`
	UpdatedAt              int64 `gorm:"autoUpdateTime:milli"`
}

func (s *LibrarySettings) ToEntity() *entity.LibrarySettings {
	return &entity.LibrarySettings{IncludeUnbackedFiles: s.IncludeUnbackedFiles, Revision: s.Revision,
		AutoCollectFiles: s.AutoCollectFiles, ConfirmPermanentDelete: s.ConfirmPermanentDelete}
}

func (s *LibrarySettings) BeforeSave(*gorm.DB) error {
	s.Revision = maxOnlineRevision(s.Revision, time.Now().UnixNano())
	return nil
}

func (l *Library) GetLibrarySettings(ctx context.Context) (*LibrarySettings, error) {
	// An absent preference preserves the complete Library view.
	settings := &LibrarySettings{ID: 1, IncludeUnbackedFiles: true, AutoCollectFiles: true, ConfirmPermanentDelete: true}
	if err := l.db.WithContext(ctx).First(settings, 1).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("read Library settings failed, %w", err)
	}
	return settings, nil
}

func (l *Library) UpdateLibrarySettings(ctx context.Context, include bool, revision int64) (*LibrarySettings, error) {
	// Visibility changes preserve the independent collection and deletion preferences.
	settings, err := l.GetLibrarySettings(ctx)
	if err != nil {
		return nil, err
	}
	settings.IncludeUnbackedFiles, settings.Revision = include, revision
	return l.UpdateFileSettings(ctx, settings)
}

func (l *Library) UpdateFileSettings(ctx context.Context, settings *LibrarySettings) (*LibrarySettings, error) {
	// Serialize the singleton's initial creation and subsequent compare-and-swap updates.
	settings.ID = 1
	revision := settings.Revision
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored LibrarySettings
		if err := tx.First(&stored, 1).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			if revision != 0 {
				return ErrOnlineConflict
			}
			// Insert defaults first so GORM cannot replace explicit false booleans with defaults.
			initial := &LibrarySettings{ID: 1, IncludeUnbackedFiles: settings.IncludeUnbackedFiles, AutoCollectFiles: true, ConfirmPermanentDelete: true}
			if err := tx.Create(initial).Error; err != nil {
				return err
			}
			return tx.Model(settings).Select("include_unbacked_files", "auto_collect_files", "confirm_permanent_delete", "revision", "updated_at").Updates(settings).Error
		} else if err != nil {
			return err
		}
		if stored.Revision != revision {
			return ErrOnlineConflict
		}

		// Select false-valued preferences explicitly and retain GORM revision hooks.
		result := tx.Model(settings).Where("revision = ?", revision).
			Select("include_unbacked_files", "auto_collect_files", "confirm_permanent_delete", "revision", "updated_at").Updates(settings)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrOnlineConflict
		}
		return nil
	})
	return settings, err
}

func (l *Library) ResolveFileScope(ctx context.Context, scope entity.FileScope) (entity.FileScope, error) {
	// Explicit query scopes do not depend on a browser preference.
	switch scope {
	case entity.FileScope_FILE_SCOPE_ALL, entity.FileScope_FILE_SCOPE_SAVED:
		return scope, nil
	case entity.FileScope_FILE_SCOPE_DEFAULT:
		settings, err := l.GetLibrarySettings(ctx)
		if err != nil {
			return scope, err
		}
		if settings.IncludeUnbackedFiles {
			return entity.FileScope_FILE_SCOPE_ALL, nil
		}
		return entity.FileScope_FILE_SCOPE_SAVED, nil
	default:
		return scope, fmt.Errorf("invalid File scope %d", scope)
	}
}

func filterFileScope(db *gorm.DB, scope entity.FileScope) *gorm.DB {
	if scope != entity.FileScope_FILE_SCOPE_SAVED {
		return db
	}
	return db.Where("files.kind = ? OR EXISTS (SELECT 1 FROM file_versions WHERE file_versions.file_id = files.id)", entity.FileKind_FILE_KIND_DIRECTORY)
}

type FilePage struct {
	Files      []*File
	NextCursor string
	Scope      entity.FileScope
}

func (l *Library) ListFiles(ctx context.Context, parentID int64, scope entity.FileScope, cursor string, limit int64) (*FilePage, error) {
	return l.ListFilesMatching(ctx, parentID, scope, cursor, limit, "")
}

func (l *Library) ListFilesMatching(ctx context.Context, parentID int64, scope entity.FileScope, cursor string, limit int64, filter string) (*FilePage, error) {
	// Bind the cursor to the effective scope, not a changeable default preference.
	scope, err := l.ResolveFileScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	pageLimit, err := normalizeFileSearchLimit(limit)
	if err != nil {
		return nil, err
	}
	expression, err := l.filesQuery(filter, false)
	if err != nil {
		return nil, err
	}
	input := fmt.Sprintf("%d/%d/%s", parentID, scope, filter)
	after, _, present, err := decodePageCursor(cursor, fileListCursorKind, input)
	if err != nil {
		return nil, err
	}

	// Apply visibility before pagination, retaining directories in either scope.
	query := filterFileScope(l.db.WithContext(ctx).Model(ModelFile), scope).Where("parent_id = ?", parentID)
	if expression != nil {
		query = query.Where(expression)
	}
	if present {
		query = query.Where("name > ?", after)
	}
	page := &FilePage{Scope: scope}
	if err := query.Order("name").Limit(pageLimit + 1).Find(&page.Files).Error; err != nil {
		return nil, err
	}
	if len(page.Files) > pageLimit {
		page.Files = page.Files[:pageLimit]
		page.NextCursor = encodePageCursor(fileListCursorKind, input, page.Files[pageLimit-1].Name, 0)
	}
	return page, nil
}

func (l *Library) FileTreeSize(ctx context.Context, id int64, scope entity.FileScope) (int64, error) {
	// Aggregate the complete selected tree in bounded content-hydration batches.
	var size int64
	files := make([]*File, 0, batchSize)
	flush := func() error {
		if err := l.HydrateFileContent(ctx, files...); err != nil {
			return err
		}
		for _, file := range files {
			size += file.Size
		}
		files = files[:0]
		return nil
	}
	selection := &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}}, Scope: scope}
	err := l.WalkFileSelections(ctx, []*entity.FileSelection{selection}, func(file *File, _ string) error {
		files = append(files, file)
		if len(files) == batchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := flush(); err != nil {
		return 0, err
	}
	return size, nil
}
