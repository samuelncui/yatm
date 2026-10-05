package library

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

// MatchFileRows applies Library catalog predicates to one bounded traversal batch.
func (l *Library) MatchFileRows(ctx context.Context, ids []int64, scope entity.FileScope, query string) (map[int64]bool, error) {
	compiled, err := l.CompileFilesQuery(query)
	if err != nil {
		return nil, err
	}
	return l.MatchFileQueryRows(ctx, ids, scope, compiled)
}

// MatchFileQueryRows shares catalog filtering with Search without recompiling each traversal batch.
func (l *Library) MatchFileQueryRows(ctx context.Context, ids []int64, scope entity.FileScope, query *FilesQuery) (map[int64]bool, error) {
	// Empty traversal batches require no catalog read.
	result := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return result, nil
	}

	// Traversal supplies at most one page, and omitted status never invokes its query chain.
	db := filterFileScope(l.readDB().WithContext(ctx).Model(ModelFile), scope).
		Where("files.id IN ?", ids)
	if query.catalog != nil {
		db = db.Where(query.catalog)
	}
	var matches []int64
	if err := db.Pluck("files.id", &matches).Error; err != nil {
		return nil, err
	}
	for _, id := range matches {
		result[id] = true
	}
	return result, nil
}

// ReadLocationBoundaries loads only registrations overlapping this measurement's root.
func (l *Library) ReadLocationBoundaries(ctx context.Context, location *Location) ([]string, error) {
	// Ancestors are a depth-bounded exact set; descendants use an escaped prefix query.
	ancestors := []string{location.RootPath}
	for parent := filepath.Dir(location.RootPath); ; parent = filepath.Dir(parent) {
		ancestors = append(ancestors, parent)
		if parent == filepath.Dir(parent) {
			break
		}
	}
	prefix := strings.TrimRight(location.RootPath, string(filepath.Separator)) + string(filepath.Separator)
	var roots []string
	err := l.readDB().WithContext(ctx).Model(&Location{}).
		Where("executor_id = ? AND id <> ?", location.ExecutorID, location.ID).
		Where(fmt.Sprintf("root_path IN ? OR root_path LIKE ? ESCAPE '%c'", likeEscape), ancestors, escapeLike(prefix, false)+"%").
		Pluck("root_path", &roots).Error
	return roots, err
}
