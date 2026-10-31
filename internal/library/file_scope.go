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
		if settings.IncludeFilesWithoutVersions {
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
