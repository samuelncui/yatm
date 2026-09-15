package library

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

// HydrateFileContent supplies bounded, metadata-only summaries for file lists.
func (l *Library) HydrateFileContent(ctx context.Context, files ...*File) error {
	// Omit directories and deduplicate identities before reading catalog facts.
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		if file != nil && file.Kind == entity.FileKind_FILE_KIND_REGULAR {
			ids = append(ids, file.ID)
		}
	}
	ids = uniqueFileIDs(ids)
	found := make(map[int64]*entity.FileContentSummary, len(ids))

	// Indexed subqueries count matching physical copies without loading positions or histories.
	for start := 0; start < len(ids); start += batchSize {
		end := start + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		var rows []struct {
			FileID                        int64
			HasOriginal                   bool
			HasVersions                   bool
			SignatureKnown                bool
			ArchivedCopies                int64
			HealthyCopies                 int64
			UncheckedCopies               int64
			UnhealthyCopies               int64
			VersionCopies                 int64
			RestorableVersionCopies       int64
			UnhealthyVersionCopies        int64
			LatestVersionID               int64
			LatestVersionRestorableCopies int64
			RestorableCurrentCopies       int64
		}
		if err := l.db.WithContext(ctx).Table("files").
			Select(fmt.Sprintf(`files.id AS file_id,
				file_locations.file_id IS NOT NULL AS has_original,
				EXISTS (SELECT 1 FROM file_versions WHERE file_versions.file_id = files.id) AS has_versions,
				(file_locations.signature IS NOT NULL AND locations.binding = @confirmed AND locations.binding_token != '' AND file_locations.observed_binding_token = locations.binding_token) AS signature_known,
				(SELECT COUNT(*) FROM positions WHERE positions.signature = file_locations.signature AND positions.is_dir = @directory) AS archived_copies,
				(SELECT COUNT(*) FROM positions WHERE positions.signature = file_locations.signature AND positions.is_dir = @directory AND health = @healthy) AS healthy_copies,
				(SELECT COUNT(*) FROM positions WHERE positions.signature = file_locations.signature AND positions.is_dir = @directory AND health = @unchecked) AS unchecked_copies,
				(SELECT COUNT(*) FROM positions WHERE positions.signature = file_locations.signature AND positions.is_dir = @directory AND health NOT IN (@healthy, @unchecked)) AS unhealthy_copies,
				(SELECT COUNT(*) FROM positions p WHERE p.signature = file_locations.signature AND p.is_dir = @directory AND p.health IN (@healthy, @unchecked) AND %[1]s) AS restorable_current_copies,
				(SELECT COUNT(*) FROM positions p WHERE p.is_dir = @directory AND EXISTS (SELECT 1 FROM file_versions v WHERE v.file_id = files.id AND v.signature = p.signature)) AS version_copies,
				(SELECT COUNT(*) FROM positions p WHERE p.is_dir = @directory AND p.health IN (@healthy, @unchecked) AND EXISTS (SELECT 1 FROM file_versions v WHERE v.file_id = files.id AND v.signature = p.signature AND %[2]s)) AS restorable_version_copies,
				(SELECT COUNT(*) FROM positions p WHERE p.is_dir = @directory AND p.health NOT IN (@healthy, @unchecked) AND EXISTS (SELECT 1 FROM file_versions v WHERE v.file_id = files.id AND v.signature = p.signature)) AS unhealthy_version_copies,
				COALESCE((SELECT v.id FROM file_versions v WHERE v.file_id = files.id ORDER BY v.last_archived_at DESC, v.id DESC LIMIT 1), 0) AS latest_version_id,
				(SELECT COUNT(*) FROM positions p WHERE p.is_dir = @directory AND p.health IN (@healthy, @unchecked) AND EXISTS (SELECT 1 FROM file_versions v WHERE v.id = (SELECT latest.id FROM file_versions latest WHERE latest.file_id = files.id ORDER BY latest.last_archived_at DESC, latest.id DESC LIMIT 1) AND v.signature = p.signature AND %[2]s)) AS latest_version_restorable_copies`,
				consistentRestoreContentSQL("file_locations"), consistentRestoreContentSQL("v")),
				sql.Named("confirmed", entity.OnlineBinding_CONFIRMED), sql.Named("directory", false),
				sql.Named("healthy", entity.PositionHealth_HEALTHY), sql.Named("unchecked", entity.PositionHealth_POSITION_HEALTH_UNKNOWN)).
			Joins("LEFT JOIN file_locations ON file_locations.file_id = files.id").
			Joins("LEFT JOIN locations ON locations.id = file_locations.location_id").
			Where("files.id IN ?", ids[start:end]).Scan(&rows).Error; err != nil {
			return fmt.Errorf("load File content summaries at batch %d failed, %w", start, err)
		}
		for _, row := range rows {
			availability := entity.OriginalAvailability_ORIGINAL_UNLINKED
			if row.HasOriginal {
				availability = entity.OriginalAvailability_ORIGINAL_UNCHECKED
			}
			found[row.FileID] = &entity.FileContentSummary{HasOriginal: row.HasOriginal, HasVersions: row.HasVersions,
				SignatureKnown: row.SignatureKnown, ArchivedCopies: row.ArchivedCopies,
				HealthyCopies: row.HealthyCopies, UncheckedCopies: row.UncheckedCopies, UnhealthyCopies: row.UnhealthyCopies,
				OriginalAvailability: availability, VersionCopies: row.VersionCopies, RestorableVersionCopies: row.RestorableVersionCopies,
				UnhealthyVersionCopies: row.UnhealthyVersionCopies, LatestVersionId: row.LatestVersionID,
				LatestVersionRestorableCopies: row.LatestVersionRestorableCopies, RestorableCurrentCopies: row.RestorableCurrentCopies}
		}
	}

	// Attach presentation facts only after every batch succeeds.
	for _, file := range files {
		if file != nil {
			file.ContentSummary = found[file.ID]
		}
	}
	return nil
}

// Match Restore preparation: an incomplete baseline or contradictory eligible
// copy cannot be advertised as restorable, even when another Position matches.
// The aliases are internal query identifiers, never user input.
func consistentRestoreContentSQL(alias string) string {
	return fmt.Sprintf(`LENGTH(%[1]s.hash) = 32 AND %[1]s.size >= 0 AND NOT EXISTS (
		SELECT 1 FROM positions candidate LEFT JOIN media ON media.id = candidate.media_id
		WHERE candidate.signature = %[1]s.signature AND candidate.is_dir = @directory
		AND candidate.health IN (@healthy, @unchecked)
		AND (candidate.size != %[1]s.size OR candidate.hash IS NULL OR candidate.hash != %[1]s.hash OR media.id IS NULL))`, alias)
}
