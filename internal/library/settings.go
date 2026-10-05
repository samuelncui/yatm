package library

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

func (l *Library) ResolveFileScope(ctx context.Context, scope entity.FileScope) (entity.FileScope, error) {
	// Explicit query scopes do not depend on a browser preference.
	switch scope {
	case entity.FileScope_FILE_SCOPE_ALL, entity.FileScope_FILE_SCOPE_SAVED:
		return scope, nil
	case entity.FileScope_FILE_SCOPE_DEFAULT, entity.FileScope_FILE_SCOPE_UNSPECIFIED:
		settings, err := l.settings.Library.Current(ctx)
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
	query := filterFileScope(l.readDB().WithContext(ctx).Model(ModelFile), scope).Where("parent_id = ?", parentID)
	if expression != nil {
		query = query.Where(expression)
	}
	if present {
		query = query.Where("name > ?", after)
	}
	rows := make([]*fileRow, 0, pageLimit+1)
	if err := query.Order("name").Limit(pageLimit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	page := &FilePage{Files: fileViews(rows), Scope: scope}
	if len(page.Files) > pageLimit {
		page.Files = page.Files[:pageLimit]
		page.NextCursor = encodePageCursor(fileListCursorKind, input, page.Files[pageLimit-1].Name, 0)
	}

	// Compatibility callers receive presentation facts through an explicit projection step.
	if err := hydrateFileViews(l.readDB().WithContext(ctx), page.Files...); err != nil {
		return nil, fmt.Errorf("hydrate File page failed, %w", err)
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
