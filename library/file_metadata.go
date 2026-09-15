package library

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxFileEditIDs           = 1000
	maxAddedTagsPerEdit      = 16
	fileTagMutationBatchSize = 256
	maxTagRunes              = 128
	maxNoteRunes             = 4096
)

type FileMetadataEdit struct {
	AddTags    []string
	RemoveTags []string
	Note       *string
}

func (l *Library) MGetFileTags(ctx context.Context, ids ...int64) (map[int64][]string, error) {
	return l.mGetFileTags(ctx, l.db.WithContext(ctx), ids...)
}

func (l *Library) mGetFileTags(ctx context.Context, tx *gorm.DB, ids ...int64) (map[int64][]string, error) {
	// Normalize the lookup so each row is queried and returned once.
	ids = uniqueFileIDs(ids)
	results := make(map[int64][]string, len(ids))
	for _, id := range ids {
		results[id] = []string{}
	}

	// Query bounded ID pages and retain the stable tag order.
	for start := 0; start < len(ids); start += batchSize {
		end := fileMetadataBatchEnd(start, len(ids))
		rows := make([]*FileTag, 0, end-start)
		result := tx.WithContext(ctx).
			Where("file_id IN (?)", ids[start:end]).
			Order("file_id ASC").Order("tag ASC").
			Find(&rows)
		if result.Error != nil {
			return nil, fmt.Errorf("query File Tags failed, start=%d end=%d, %w", start, end, result.Error)
		}
		for _, row := range rows {
			results[row.FileID] = append(results[row.FileID], row.Tag)
		}
	}
	return results, nil
}

func validateFileNote(note string) error {
	if !utf8.ValidString(note) {
		return fmt.Errorf("File note is not valid UTF-8")
	}
	if utf8.RuneCountInString(note) > maxNoteRunes {
		return fmt.Errorf("File note exceeds %d characters", maxNoteRunes)
	}
	return nil
}

func (l *Library) EditFileMetadata(ctx context.Context, ids []int64, edit FileMetadataEdit) error {
	// Normalize the complete patch before opening its mutation transaction.
	ids, err := normalizeFileEditIDs(ids)
	if err != nil {
		return err
	}
	edit.AddTags, err = normalizeTags(edit.AddTags)
	if err != nil {
		return err
	}
	if len(edit.AddTags) > maxAddedTagsPerEdit {
		return fmt.Errorf("File edit exceeds %d added Tags", maxAddedTagsPerEdit)
	}
	edit.RemoveTags, err = normalizeTags(edit.RemoveTags)
	if err != nil {
		return err
	}
	if edit.Note != nil {
		if err := validateFileNote(*edit.Note); err != nil {
			return err
		}
	}
	if len(edit.AddTags) == 0 && len(edit.RemoveTags) == 0 && edit.Note == nil {
		return fmt.Errorf("File metadata edit requires at least one change")
	}

	// Reject ambiguous patches before inspecting or changing stored metadata.
	removing := make(map[string]struct{}, len(edit.RemoveTags))
	for _, tag := range edit.RemoveTags {
		removing[tag] = struct{}{}
	}
	for _, tag := range edit.AddTags {
		if _, conflict := removing[tag]; conflict {
			return fmt.Errorf("Tag %q cannot be added and removed in one edit", tag)
		}
	}

	// Validate every target before applying the patch atomically.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateFileIDs(ctx, tx, ids); err != nil {
			return err
		}

		// Apply Tag removals before additions so the requested final set is explicit.
		if len(edit.RemoveTags) > 0 {
			if err := removeFileTags(ctx, tx, ids, edit.RemoveTags); err != nil {
				return err
			}
		}
		if len(edit.AddTags) > 0 {
			if err := insertFileTags(ctx, tx, ids, edit.AddTags); err != nil {
				return err
			}
		}

		// Replace notes only when the optional field is present.
		if edit.Note == nil {
			return nil
		}
		for start := 0; start < len(ids); start += batchSize {
			end := fileMetadataBatchEnd(start, len(ids))
			result := tx.WithContext(ctx).Model(ModelFile).Where("id IN (?)", ids[start:end]).Update("note", *edit.Note)
			if result.Error != nil {
				return fmt.Errorf("update File notes failed, start=%d end=%d, %w", start, end, result.Error)
			}
		}
		return nil
	})
}

func normalizeFileEditIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("File edit requires at least one ID")
	}
	if len(ids) > maxFileEditIDs {
		return nil, fmt.Errorf("File edit exceeds %d IDs", maxFileEditIDs)
	}
	return uniqueFileIDs(ids), nil
}

func uniqueFileIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}

func normalizeTags(tags []string) ([]string, error) {
	seen := make(map[string]struct{}, len(tags))
	normalized := make([]string, 0, len(tags))
	for _, value := range tags {
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("Tag is not valid UTF-8")
		}
		tag := strings.ToLower(strings.TrimSpace(value))
		if tag == "" {
			return nil, fmt.Errorf("Tag must not be empty")
		}
		if utf8.RuneCountInString(tag) > maxTagRunes {
			return nil, fmt.Errorf("Tag %q exceeds %d characters", tag, maxTagRunes)
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		normalized = append(normalized, tag)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func validateFileIDs(ctx context.Context, tx *gorm.DB, ids []int64) error {
	// Query bounded pages so validation also works at the public batch limit on SQLite.
	found := make(map[int64]struct{}, len(ids))
	for start := 0; start < len(ids); start += batchSize {
		end := fileMetadataBatchEnd(start, len(ids))
		var batch []int64
		result := tx.WithContext(ctx).Model(ModelFile).Where("id IN (?)", ids[start:end]).Pluck("id", &batch)
		if result.Error != nil {
			return fmt.Errorf("validate File IDs failed, start=%d end=%d, %w", start, end, result.Error)
		}
		for _, id := range batch {
			found[id] = struct{}{}
		}
	}

	// Report the first missing identity without partially applying the request.
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return fmt.Errorf("File not found, id=%d", id)
		}
	}
	return nil
}

func replaceFileTags(ctx context.Context, tx *gorm.DB, ids []int64, tags []string) error {
	// Remove every old relation for the selected Files in bounded pages.
	for start := 0; start < len(ids); start += batchSize {
		end := fileMetadataBatchEnd(start, len(ids))
		result := tx.WithContext(ctx).Where("file_id IN (?)", ids[start:end]).Delete(ModelFileTag)
		if result.Error != nil {
			return fmt.Errorf("clear File Tags failed, start=%d end=%d, %w", start, end, result.Error)
		}
	}

	// Insert the requested exact relation set after all old rows are gone.
	rows := make([]*FileTag, 0, len(ids)*len(tags))
	for _, id := range ids {
		for _, tag := range tags {
			rows = append(rows, &FileTag{FileID: id, Tag: tag})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	if result := tx.WithContext(ctx).CreateInBatches(rows, batchSize); result.Error != nil {
		return fmt.Errorf("replace File Tags failed, %w", result.Error)
	}
	return nil
}

func insertFileTags(ctx context.Context, tx *gorm.DB, ids []int64, tags []string) error {
	// Insert only missing relations so repeated additions remain idempotent.
	rows := make([]*FileTag, 0, len(ids)*len(tags))
	for _, id := range ids {
		for _, tag := range tags {
			rows = append(rows, &FileTag{FileID: id, Tag: tag})
		}
	}
	result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, batchSize)
	if result.Error != nil {
		return fmt.Errorf("insert File Tags failed, %w", result.Error)
	}
	return nil
}

func removeFileTags(ctx context.Context, tx *gorm.DB, ids []int64, tags []string) error {
	// Bound both dimensions so every delete stays below database parameter limits.
	for idStart := 0; idStart < len(ids); idStart += fileTagMutationBatchSize {
		idEnd := idStart + fileTagMutationBatchSize
		if idEnd > len(ids) {
			idEnd = len(ids)
		}
		for tagStart := 0; tagStart < len(tags); tagStart += fileTagMutationBatchSize {
			tagEnd := tagStart + fileTagMutationBatchSize
			if tagEnd > len(tags) {
				tagEnd = len(tags)
			}
			result := tx.WithContext(ctx).
				Where("file_id IN (?) AND tag IN (?)", ids[idStart:idEnd], tags[tagStart:tagEnd]).
				Delete(ModelFileTag)
			if result.Error != nil {
				return fmt.Errorf(
					"remove File Tags failed, id_start=%d id_end=%d tag_start=%d tag_end=%d, %w",
					idStart,
					idEnd,
					tagStart,
					tagEnd,
					result.Error,
				)
			}
		}
	}
	return nil
}

func fileMetadataBatchEnd(start, total int) int {
	end := start + batchSize
	if end > total {
		return total
	}
	return end
}

func (l *Library) mergeFileMetadata(
	ctx context.Context,
	tx *gorm.DB,
	source *File,
	target *File,
) (bool, error) {
	// Merge distinct notes without silently discarding either directory's metadata.
	mergedNote := target.Note
	if mergedNote == "" {
		mergedNote = source.Note
	} else if source.Note != "" && source.Note != mergedNote {
		mergedNote += "\n\n" + source.Note
	}
	if utf8.RuneCountInString(mergedNote) > maxNoteRunes {
		return false, fmt.Errorf("merged directory note exceeds %d characters", maxNoteRunes)
	}

	// Replace the target relations with the deterministic union of both Tag sets.
	tagsByID, err := l.mGetFileTags(ctx, tx, source.ID, target.ID)
	if err != nil {
		return false, err
	}
	mergedTags, err := normalizeTags(append(tagsByID[target.ID], tagsByID[source.ID]...))
	if err != nil {
		return false, err
	}
	if err := replaceFileTags(ctx, tx, []int64{target.ID}, mergedTags); err != nil {
		return false, err
	}
	changed := target.Note != mergedNote
	target.Note = mergedNote
	return changed, nil
}

func deleteFileRows(ctx context.Context, tx *gorm.DB, ids []int64) error {
	// Delete dependent Tag relations before their File identities.
	ids = uniqueFileIDs(ids)
	for start := 0; start < len(ids); start += batchSize {
		end := fileMetadataBatchEnd(start, len(ids))
		if result := tx.WithContext(ctx).Where("file_id IN (?)", ids[start:end]).Delete(ModelFileTag); result.Error != nil {
			return fmt.Errorf("delete File Tags failed, start=%d end=%d, %w", start, end, result.Error)
		}
		versions := tx.Model(&FileVersion{}).Select("id").Where("file_id IN ?", ids[start:end])
		if err := tx.WithContext(ctx).Where("version_id IN (?)", versions).Delete(&FileVersionArchive{}).Error; err != nil {
			return fmt.Errorf("delete archive observations failed, %w", err)
		}
		for _, model := range []any{&FileTrackingKey{}, &FileLocation{}, &FileVersion{}} {
			if err := tx.WithContext(ctx).Where("file_id IN ?", ids[start:end]).Delete(model).Error; err != nil {
				return err
			}
		}
	}

	// Remove the File rows only after every dependent relation is gone.
	for start := 0; start < len(ids); start += batchSize {
		end := fileMetadataBatchEnd(start, len(ids))
		if result := tx.WithContext(ctx).Where("id IN (?)", ids[start:end]).Delete(ModelFile); result.Error != nil {
			return fmt.Errorf("delete Files failed, start=%d end=%d, %w", start, end, result.Error)
		}
	}
	return nil
}
