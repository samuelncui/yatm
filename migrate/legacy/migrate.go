package legacy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/samuelncui/yatm/library"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/executor/archive"
	"github.com/samuelncui/yatm/executor/restore"
	"github.com/samuelncui/yatm/internal/dataformat"
	legacypb "github.com/samuelncui/yatm/migrate/legacy/pb"
	"github.com/samuelncui/yatm/resource"
	"github.com/samuelncui/yatm/tools"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

const (
	reportFilename           = "migration-report.json"
	legacyJobsDirectory      = "jobs-legacy"
	legacyJobLogsDirectory   = "job-logs"
	legacyLTFSIndexDirectory = "captured_indices"
	executorID               = "local"
)

var legacyCreatedTapePattern = regexp.MustCompile(`create tape success, tape_id=\s*([1-9][0-9]*)\b`)

type legacyJob struct {
	ID         int64 `gorm:"primaryKey;autoIncrement"`
	Status     legacypb.JobStatus
	Priority   int64
	State      *legacypb.JobState
	CreateTime time.Time
	UpdateTime time.Time
}

func (legacyJob) TableName() string { return "jobs" }

type catalogJob struct {
	executor.JobTarget
	ID         int64  `gorm:"primaryKey;autoIncrement;index:idx_executor_id,priority:2"`
	ExecutorID string `gorm:"type:varchar(128);not null;index:idx_executor_id,priority:1"`

	CreatedAt     int64                 `gorm:"not null;autoCreateTime:milli"`
	UpdatedAt     int64                 `gorm:"not null;autoUpdateTime:milli;index:idx_job_updated_at"`
	DeletedAt     soft_delete.DeletedAt `gorm:"softDelete:milli,DeletedAtField:UpdatedAt,DeletedAtFieldUnit:milli;index:idx_job_deleted_at"`
	Revision      int64                 `gorm:"not null;index:idx_job_revision"`
	CatalogKind   entity.JobKind        `gorm:"not null;default:0;index"`
	CatalogStatus entity.JobStatus      `gorm:"not null;default:0;index"`
}

func (catalogJob) TableName() string { return "jobs_staging" }

type Report struct {
	Success                 bool      `json:"success"`
	CreatedAt               time.Time `json:"created_at"`
	MigratedJobs            int       `json:"migrated_jobs"`
	DeletedJobs             []int64   `json:"deleted_jobs,omitempty"`
	ArchiveItems            int       `json:"archive_items"`
	RestoreItems            int       `json:"restore_items"`
	LibraryFiles            int       `json:"library_files"`
	LibraryTapes            int       `json:"library_tapes"`
	LibraryPositions        int       `json:"library_positions"`
	LibraryDirectories      int       `json:"library_directories"`
	LibraryStoragePositions int       `json:"library_storage_positions"`
	Warnings                []string  `json:"warnings,omitempty"`
	Error                   string    `json:"error,omitempty"`
}

// Prepare converts legacy data using the conventional captured-index directory.
func Prepare(ctx context.Context, db *gorm.DB, workRoot string) (*Report, error) {
	return PrepareWithLTFSIndex(
		ctx,
		db,
		workRoot,
		filepath.Join(workRoot, legacyLTFSIndexDirectory),
	)
}

// PrepareWithLTFSIndex converts legacy data and imports physical positions from indexRoot.
func PrepareWithLTFSIndex(
	ctx context.Context,
	db *gorm.DB,
	workRoot string,
	indexRoot string,
	restoreRoot ...string,
) (_ *Report, rerr error) {
	// Reject unsupported inputs before reports, staging tables or Job directories are written.
	schema, err := DetectSchema(db)
	if err != nil {
		return nil, err
	}
	if schema != SchemaLegacy {
		return nil, fmt.Errorf("prepare requires a legacy Catalog, found %s", schema)
	}

	// Publish a report for both successful and failed preparations.
	report := &Report{CreatedAt: time.Now()}
	defer func() {
		if rerr != nil {
			report.Error = rerr.Error()
		}
		if err := writeReport(workRoot, report); err != nil {
			rerr = errors.Join(rerr, err)
		}
	}()

	// Discard only migration-owned output from an interrupted prior prepare.
	jobsRoot := filepath.Join(workRoot, "jobs")
	legacyJobsRoot := filepath.Join(workRoot, legacyJobsDirectory)
	if hasStaging(db) {
		if err := Abort(db, workRoot); err != nil {
			return report, fmt.Errorf("discard interrupted current preparation failed, %w", err)
		}
	}
	for _, table := range stagingTableSwaps {
		if db.Migrator().HasTable(table.backup) {
			return report, fmt.Errorf("migration is already committed, table=%q", table.backup)
		}
	}

	// Preserve an existing legacy Job directory before creating current Bundles at the final path.
	if exists(legacyJobsRoot) && exists(jobsRoot) {
		return report, fmt.Errorf("both legacy and active Job directories exist; inspect them before preparing")
	}
	if !exists(legacyJobsRoot) && exists(jobsRoot) {
		entries, err := os.ReadDir(jobsRoot)
		if err != nil {
			return report, fmt.Errorf("inspect legacy Job directory failed, %w", err)
		}
		if len(entries) == 0 {
			if err := os.Remove(jobsRoot); err != nil {
				return report, fmt.Errorf("remove empty legacy Job directory failed, %w", err)
			}
		} else if err := os.Rename(jobsRoot, legacyJobsRoot); err != nil {
			return report, fmt.Errorf("preserve legacy Job directory failed, %w", err)
		}
	}

	// Create the staging table first so Abort can identify ownership of the final directory.
	if err := db.WithContext(ctx).AutoMigrate(&catalogJob{}); err != nil {
		return report, fmt.Errorf("create current catalog staging table failed, %w", err)
	}
	if err := os.MkdirAll(jobsRoot, 0o755); err != nil {
		return report, fmt.Errorf("create current Job directory failed, %w", err)
	}
	if err := prepareLibrary(ctx, db, indexRoot, report); err != nil {
		return report, fmt.Errorf("migrate legacy Library failed, %w", err)
	}

	// Convert one legacy Job at a time so large protobuf states do not accumulate.
	var cursor int64
	for {
		var jobs []*legacyJob
		if err := db.WithContext(ctx).Where("id > ?", cursor).Order("id").Limit(1).Find(&jobs).Error; err != nil {
			return report, fmt.Errorf("read legacy job failed, after_id=%d, %w", cursor, err)
		}
		if len(jobs) == 0 {
			break
		}
		job := jobs[0]
		cursor = job.ID
		if job.Status == legacypb.JobStatus_DELETED {
			report.DeletedJobs = append(report.DeletedJobs, job.ID)
			continue
		}
		if job.CreateTime.IsZero() {
			job.CreateTime = job.UpdateTime
			if job.CreateTime.IsZero() {
				job.CreateTime = report.CreatedAt
			}
			report.Warnings = append(report.Warnings, fmt.Sprintf(
				"job %d has no legacy creation time; using %s",
				job.ID,
				job.CreateTime.Format(time.RFC3339Nano),
			))
		}
		counts, warnings, err := prepareJob(
			ctx,
			db,
			"positions_staging",
			restorePositionsRequired,
			jobsRoot,
			legacyJobsRoot,
			job,
			restoreRoot...,
		)
		if err != nil {
			return report, fmt.Errorf("migrate legacy job failed, id=%d, %w", job.ID, err)
		}
		report.ArchiveItems += counts.archive
		report.RestoreItems += counts.restore
		report.Warnings = append(report.Warnings, warnings...)
		updatedAt := job.UpdateTime
		if updatedAt.Before(job.CreateTime) {
			updatedAt = job.CreateTime
		}
		catalog := &catalogJob{
			ID: job.ID, ExecutorID: executorID,
			CreatedAt: job.CreateTime.UnixMilli(), UpdatedAt: updatedAt.UnixMilli(),
			Revision: int64(report.MigratedJobs + 1),
		}
		stateDB, err := resource.OpenSQLite(filepath.Join(jobsRoot, strconv.FormatInt(job.ID, 10), "state.db"))
		if err != nil {
			return report, err
		}
		var record executor.JobRecord
		readErr := stateDB.WithContext(ctx).First(&record, 1).Error
		closeDB(stateDB)
		if readErr != nil {
			return report, fmt.Errorf("read migrated Job projection failed, id=%d, %w", job.ID, readErr)
		}
		catalog.CatalogKind, catalog.CatalogStatus = record.Kind, record.Status
		if err := db.WithContext(ctx).Create(catalog).Error; err != nil {
			return report, fmt.Errorf("create current catalog entry failed, id=%d, %w", job.ID, err)
		}
		report.MigratedJobs++
	}
	report.Success = true
	return report, nil
}

type itemCounts struct {
	archive int
	restore int
}

type restorePositionPolicy uint8

const (
	restorePositionsRequired restorePositionPolicy = iota
	restorePositionsOptional
)

func prepareJob(
	ctx context.Context,
	libraryDB *gorm.DB,
	positionsTable string,
	positionPolicy restorePositionPolicy,
	jobsRoot string,
	legacyJobsRoot string,
	job *legacyJob,
	restoreRoot ...string,
) (itemCounts, []string, error) {
	// Reject an undecodable legacy row before creating staging artifacts.
	if job.State == nil {
		return itemCounts{}, nil, fmt.Errorf("JobState is missing")
	}

	// Create the complete current Bundle shell at its final path.
	dir := filepath.Join(jobsRoot, fmt.Sprintf("%d", job.ID))
	if err := os.MkdirAll(filepath.Join(dir, "tapes"), 0o755); err != nil {
		return itemCounts{}, nil, fmt.Errorf("create current Tape directory failed, %w", err)
	}

	// Persist immutable identity before writing the kind-specific database.
	metadata, err := json.Marshal(dataformat.NewBundle(job.ID, job.CreateTime))
	if err != nil {
		return itemCounts{}, nil, fmt.Errorf("encode current bundle metadata failed, %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.json"), metadata, 0o644); err != nil {
		return itemCounts{}, nil, fmt.Errorf("write current bundle metadata failed, %w", err)
	}
	legacyMediaIDs, err := migrateLegacyJobLog(filepath.Dir(jobsRoot), dir, job.ID)
	if err != nil {
		return itemCounts{}, nil, err
	}

	// Route the frozen legacy state into exactly one current manifest schema.
	jobDB, err := resource.OpenSQLite(filepath.Join(dir, "state.db"))
	if err != nil {
		return itemCounts{}, nil, fmt.Errorf("create current Job DB failed, %w", err)
	}
	defer closeDB(jobDB)

	switch state := job.State.State.(type) {
	case *legacypb.JobState_Archive:
		var transitionalDB *gorm.DB
		var table string
		if state.Archive != nil && len(state.Archive.Sources) == 0 {
			transitionalDB, table, err = openTransitionalJobDB(legacyJobsRoot, job.ID, "files")
			if err != nil {
				return itemCounts{}, nil, err
			}
			if transitionalDB != nil {
				defer closeDB(transitionalDB)
			}
		}
		count, err := migrateArchive(ctx, jobDB, transitionalDB, table, job, state.Archive)
		if err != nil {
			return itemCounts{}, nil, err
		}
		reconcileWarnings, err := reconcileMigratedArchive(
			ctx,
			jobDB,
			libraryDB,
			positionsTable,
			job.ID,
			legacyMediaIDs,
		)
		if err != nil {
			return itemCounts{}, nil, err
		}
		warnings := reconcileWarnings
		if count == 0 {
			warnings = append(warnings, fmt.Sprintf("job %d has an empty archive manifest; migrated as completed", job.ID))
		}
		return itemCounts{archive: count}, warnings, nil
	case *legacypb.JobState_Restore:
		var transitionalDB *gorm.DB
		var table string
		if state.Restore != nil && len(state.Restore.Tapes) == 0 {
			transitionalDB, table, err = openTransitionalJobDB(legacyJobsRoot, job.ID, "files", "restore_files")
			if err != nil {
				return itemCounts{}, nil, err
			}
			if transitionalDB != nil {
				defer closeDB(transitionalDB)
			}
		}
		count, err := migrateRestore(
			ctx,
			jobDB,
			libraryDB,
			positionsTable,
			positionPolicy,
			transitionalDB,
			table,
			job,
			state.Restore,
		)
		var warnings []string
		if err == nil && len(restoreRoot) > 0 && restoreRoot[0] != "" {
			var root string
			root, err = executor.CanonicalConfiguredPath(restoreRoot[0])
			if err == nil {
				err = jobDB.Model(&restore.Config{}).Where("id = ?", 1).Update("legacy_root", root).Error
			}
		}
		if err == nil && count == 0 {
			warnings = append(warnings, fmt.Sprintf("job %d has an empty restore manifest; migrated as completed", job.ID))
		}
		if count > 0 && (len(restoreRoot) == 0 || restoreRoot[0] == "") {
			warnings = append(warnings, fmt.Sprintf("job %d has no configured legacy restore target; its history is readable but execution requires a prepared target binding", job.ID))
		}
		return itemCounts{restore: count}, warnings, err
	default:
		return itemCounts{}, nil, fmt.Errorf("unsupported JobState type %T", job.State.State)
	}
}

func openTransitionalJobDB(root string, jobID int64, tables ...string) (*gorm.DB, string, error) {
	filename := filepath.Join(root, fmt.Sprint(jobID), "state.db")
	if !exists(filename) {
		return nil, "", nil
	}
	db, err := resource.OpenSQLite(filename)
	if err != nil {
		return nil, "", fmt.Errorf("open transitional Job DB failed, %w", err)
	}
	for _, table := range tables {
		if db.Migrator().HasTable(table) {
			return db, table, nil
		}
	}
	closeDB(db)
	return nil, "", nil
}

func migrateLegacyJobLog(workRoot, jobDir string, jobID int64) ([]int64, error) {
	sourcePath := filepath.Join(workRoot, legacyJobLogsDirectory, fmt.Sprintf("%d.log", jobID))
	source, err := os.Open(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open legacy Job log failed, id=%d, %w", jobID, err)
	}
	defer source.Close()

	var mediaIDs []int64
	seenMedia := make(map[int64]struct{})
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		match := legacyCreatedTapePattern.FindStringSubmatch(scanner.Text())
		if len(match) == 0 {
			continue
		}
		id, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse Media ID from legacy Job log failed, id=%d, %w", jobID, err)
		}
		if _, exists := seenMedia[id]; exists {
			continue
		}
		seenMedia[id] = struct{}{}
		mediaIDs = append(mediaIDs, id)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read legacy Job log failed, id=%d, %w", jobID, err)
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind legacy Job log failed, id=%d, %w", jobID, err)
	}

	targetPath := filepath.Join(jobDir, "job.log")
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create current Job log failed, id=%d, %w", jobID, err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return nil, fmt.Errorf("copy legacy Job log failed, id=%d, %w", jobID, err)
	}
	return mediaIDs, nil
}

type transitionalArchiveItem struct {
	ID     int64 `gorm:"primaryKey"`
	Base   string
	Path   tools.SortPath
	Size   int64
	Status legacypb.CopyStatus
}

type transitionalRestoreCopy struct {
	ID         int64 `gorm:"primaryKey"`
	FileID     int64
	TapeID     int64
	Path       tools.SortPath
	PositionID int64
	TargetPath string
	Size       int64
	Hash       []byte
	Status     legacypb.CopyStatus
}

func migrateArchive(ctx context.Context, db, transitionalDB *gorm.DB, transitionalTable string, job *legacyJob, state *legacypb.JobArchiveState) (int, error) {
	if state == nil {
		return 0, fmt.Errorf("archive state is missing")
	}
	if transitionalDB != nil {
		return migrateTransitionalArchive(ctx, db, transitionalDB, transitionalTable, job)
	}

	// Stream bounded item batches into one complete Archive Job database.
	allSubmitted := true
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&executor.JobRecord{}, &archive.Config{}, &archive.Item{}); err != nil {
			return err
		}
		items := make([]*archive.Item, 0, 256)
		flush := func() error {
			if len(items) == 0 {
				return nil
			}
			if err := tx.CreateInBatches(items, 256).Error; err != nil {
				return err
			}
			items = items[:0]
			return nil
		}
		for index, source := range state.Sources {
			if source == nil || source.Source == nil {
				return fmt.Errorf("archive source is missing, item_index=%d", index)
			}
			if source.Size < 0 {
				return fmt.Errorf("archive source has invalid size, item_index=%d size=%d", index, source.Size)
			}
			tapePath := path.Join(source.Source.Path...)
			if err := entity.ValidateRelativePath(tapePath); err != nil {
				return fmt.Errorf("invalid archive tape path, item_index=%d, %w", index, err)
			}
			sourceParts := append([]string{source.Source.Base}, source.Source.Path...)
			data := &entity.ArchiveManifestFile{SourcePath: filepath.Clean(filepath.Join(sourceParts...))}
			if err := data.Validate(); err != nil {
				return fmt.Errorf("invalid archive source, item_index=%d, %w", index, err)
			}

			status := migratedCopyStatus(source.Status)
			if status != entity.CopyStatus_SUBMITTED {
				allSubmitted = false
			}
			item := &archive.Item{
				ID: int64(index + 1), Status: status, Size: source.Size, TargetPath: tapePath, Data: data,
			}
			if status == entity.CopyStatus_SUBMITTED {
				item.MediaPath = tapePath
			}
			items = append(items, item)
			if len(items) == cap(items) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := flush(); err != nil {
			return err
		}

		status := entity.JobStatus_PENDING
		if allSubmitted {
			status = entity.JobStatus_COMPLETED
		}
		if err := tx.Create(&archive.Config{ID: 1, Spec: &entity.ArchiveJobSpec{}}).Error; err != nil {
			return err
		}
		return tx.Create(&executor.JobRecord{
			ID: 1, Kind: entity.JobKind_ARCHIVE, Status: status, Priority: job.Priority,
		}).Error
	})
	if err != nil {
		return 0, fmt.Errorf("write archive Job DB failed, %w", err)
	}
	return len(state.Sources), nil
}

func migrateTransitionalArchive(ctx context.Context, db, transitionalDB *gorm.DB, table string, job *legacyJob) (int, error) {
	// Stream the pre-current transitional manifest without retaining all items in memory.
	rows, err := transitionalDB.WithContext(ctx).Table(table).Order("id").Rows()
	if err != nil {
		return 0, fmt.Errorf("read transitional Archive Job DB failed, %w", err)
	}
	defer rows.Close()

	count := 0
	allSubmitted := true
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&executor.JobRecord{}, &archive.Config{}, &archive.Item{}); err != nil {
			return err
		}
		items := make([]*archive.Item, 0, 256)
		flush := func() error {
			if len(items) == 0 {
				return nil
			}
			if err := tx.CreateInBatches(items, 256).Error; err != nil {
				return err
			}
			items = items[:0]
			return nil
		}
		for rows.Next() {
			var source transitionalArchiveItem
			if err := transitionalDB.ScanRows(rows, &source); err != nil {
				return err
			}
			if source.Size < 0 {
				return fmt.Errorf("archive source has invalid size, item_id=%d size=%d", source.ID, source.Size)
			}
			tapePath := source.Path.String()
			if err := entity.ValidateRelativePath(tapePath); err != nil {
				return fmt.Errorf("invalid archive tape path, item_id=%d, %w", source.ID, err)
			}
			data := &entity.ArchiveManifestFile{SourcePath: filepath.Clean(filepath.Join(source.Base, filepath.FromSlash(tapePath)))}
			if err := data.Validate(); err != nil {
				return fmt.Errorf("invalid archive source, item_id=%d, %w", source.ID, err)
			}

			status := migratedCopyStatus(source.Status)
			if status != entity.CopyStatus_SUBMITTED {
				allSubmitted = false
			}
			item := &archive.Item{
				ID: source.ID, Status: status, Size: source.Size, TargetPath: tapePath, Data: data,
			}
			if status == entity.CopyStatus_SUBMITTED {
				item.MediaPath = tapePath
			}
			items = append(items, item)
			count++
			if len(items) == cap(items) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}

		status := entity.JobStatus_PENDING
		if allSubmitted {
			status = entity.JobStatus_COMPLETED
		}
		if err := tx.Create(&archive.Config{ID: 1, Spec: &entity.ArchiveJobSpec{}}).Error; err != nil {
			return err
		}
		return tx.Create(&executor.JobRecord{ID: 1, Kind: entity.JobKind_ARCHIVE, Status: status, Priority: job.Priority}).Error
	})
	if err != nil {
		return 0, fmt.Errorf("write archive Job DB failed, %w", err)
	}
	return count, nil
}

func reconcileMigratedArchive(
	ctx context.Context,
	jobDB *gorm.DB,
	libraryDB *gorm.DB,
	positionsTable string,
	jobID int64,
	mediaIDs []int64,
) ([]string, error) {
	var submitted int64
	if err := jobDB.WithContext(ctx).Model(&archive.Item{}).
		Where("status = ?", entity.CopyStatus_SUBMITTED).
		Count(&submitted).Error; err != nil {
		return nil, fmt.Errorf("count submitted Archive items failed, %w", err)
	}
	if submitted == 0 {
		return nil, updateMigratedArchiveStatus(ctx, jobDB)
	}
	if len(mediaIDs) == 0 {
		if err := resetMigratedArchiveItems(
			ctx,
			jobDB,
			"status = ?",
			entity.CopyStatus_SUBMITTED,
		).Error; err != nil {
			return nil, fmt.Errorf("reset unverified Archive items failed, %w", err)
		}
		if err := updateMigratedArchiveStatus(ctx, jobDB); err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf(
			"job %d reset %d submitted archive items because no Media ID was recoverable from the legacy Job log",
			jobID,
			submitted,
		)}, nil
	}

	type archiveCandidate struct {
		ID        int64
		MediaPath string
		Size      int64
	}
	type positionFact struct {
		MediaID int64
		Path    string
		Size    int64
	}
	type positionKey struct {
		Path string
		Size int64
	}

	var cursor int64
	var reset int64
	for {
		var items []archiveCandidate
		if err := jobDB.WithContext(ctx).Model(&archive.Item{}).
			Select("id", "media_path", "size").
			Where("status = ? AND id > ?", entity.CopyStatus_SUBMITTED, cursor).
			Order("id").
			Limit(256).
			Find(&items).Error; err != nil {
			return nil, fmt.Errorf("read submitted Archive items failed, after_id=%d, %w", cursor, err)
		}
		if len(items) == 0 {
			break
		}
		cursor = items[len(items)-1].ID

		paths := make([]string, 0, len(items))
		for _, item := range items {
			paths = append(paths, item.MediaPath)
		}
		var positions []positionFact
		if err := libraryDB.WithContext(ctx).Table(positionsTable).
			Select("media_id", "path", "size").
			Where("media_id IN ? AND is_dir = ? AND path IN ?", mediaIDs, false, paths).
			Find(&positions).Error; err != nil {
			return nil, fmt.Errorf("read migrated Archive Positions failed, media_ids=%v, %w", mediaIDs, err)
		}
		verified := make(map[positionKey]int64, len(positions))
		for _, position := range positions {
			key := positionKey{Path: position.Path, Size: position.Size}
			if existing, exists := verified[key]; exists && existing != position.MediaID {
				verified[key] = 0
				continue
			}
			verified[key] = position.MediaID
		}

		verifiedByMedia := make(map[int64][]int64)
		resetIDs := make([]int64, 0, len(items))
		for _, item := range items {
			key := positionKey{Path: item.MediaPath, Size: item.Size}
			mediaID := verified[key]
			if mediaID > 0 {
				verifiedByMedia[mediaID] = append(verifiedByMedia[mediaID], item.ID)
				continue
			}
			resetIDs = append(resetIDs, item.ID)
		}
		for mediaID, ids := range verifiedByMedia {
			if err := jobDB.WithContext(ctx).Model(&archive.Item{}).
				Where("id IN ?", ids).
				Update("media_id", mediaID).Error; err != nil {
				return nil, fmt.Errorf("recover Archive Media ID failed, media_id=%d, %w", mediaID, err)
			}
		}
		if len(resetIDs) > 0 {
			if err := resetMigratedArchiveItems(ctx, jobDB, "id IN ?", resetIDs).Error; err != nil {
				return nil, fmt.Errorf("reset Archive items absent from logged Media failed, media_ids=%v, %w", mediaIDs, err)
			}
			reset += int64(len(resetIDs))
		}
	}
	if err := updateMigratedArchiveStatus(ctx, jobDB); err != nil {
		return nil, err
	}
	if reset == 0 {
		return nil, nil
	}
	return []string{fmt.Sprintf(
		"job %d reset %d submitted archive items absent from or ambiguous across logged Media %v",
		jobID,
		reset,
		mediaIDs,
	)}, nil
}

func resetMigratedArchiveItems(ctx context.Context, db *gorm.DB, query string, args ...any) *gorm.DB {
	return db.WithContext(ctx).Model(&archive.Item{}).
		Where(query, args...).
		Updates(map[string]any{
			"status":     entity.CopyStatus_PENDING,
			"media_path": "",
			"media_id":   nil,
			"result":     nil,
		})
}

func updateMigratedArchiveStatus(ctx context.Context, db *gorm.DB) error {
	var pending int64
	if err := db.WithContext(ctx).Model(&archive.Item{}).
		Where("status <> ?", entity.CopyStatus_SUBMITTED).
		Count(&pending).Error; err != nil {
		return fmt.Errorf("count pending Archive items failed, %w", err)
	}
	status := entity.JobStatus_COMPLETED
	if pending > 0 {
		status = entity.JobStatus_PENDING
	}
	if err := db.WithContext(ctx).Model(&executor.JobRecord{}).
		Where("id = ?", 1).
		Update("status", status).Error; err != nil {
		return fmt.Errorf("update migrated Archive Job status failed, %w", err)
	}
	return nil
}

func migrateRestore(
	ctx context.Context,
	jobDB *gorm.DB,
	libraryDB *gorm.DB,
	positionsTable string,
	positionPolicy restorePositionPolicy,
	transitionalDB *gorm.DB,
	transitionalTable string,
	job *legacyJob,
	state *legacypb.JobRestoreState,
) (int, error) {
	if state == nil {
		return 0, fmt.Errorf("restore state is missing")
	}
	if transitionalDB != nil {
		return migrateTransitionalRestore(
			ctx,
			jobDB,
			libraryDB,
			positionsTable,
			positionPolicy,
			transitionalDB,
			transitionalTable,
			job,
		)
	}

	// Group duplicate physical candidates under their logical Library file.
	byFile := make(map[int64][]*legacypb.RestoreFile)
	for tapeIndex, tape := range state.Tapes {
		if tape == nil {
			return 0, fmt.Errorf("restore tape is missing, index=%d", tapeIndex)
		}
		for fileIndex, file := range tape.Files {
			if file == nil {
				return 0, fmt.Errorf("restore file is missing, tape_id=%d index=%d", tape.TapeId, fileIndex)
			}
			if file.TapeId == 0 {
				file.TapeId = tape.TapeId
			}
			if file.FileId <= 0 || file.TapeId <= 0 {
				return 0, fmt.Errorf("restore candidate has invalid identity, file_id=%d tape_id=%d", file.FileId, file.TapeId)
			}
			byFile[file.FileId] = append(byFile[file.FileId], file)
		}
	}

	// Stabilize logical file ordering across repeated migrations.
	fileIDs := make([]int64, 0, len(byFile))
	for fileID := range byFile {
		fileIDs = append(fileIDs, fileID)
	}
	sort.Slice(fileIDs, func(i, j int) bool { return fileIDs[i] < fileIDs[j] })
	// Stream bounded copy batches into one complete Restore Job database.
	allCompleted := true
	err := jobDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&executor.JobRecord{}, &restore.Config{}, &restore.Copy{}); err != nil {
			return err
		}
		copies := make([]*restore.Copy, 0, 256)
		flush := func() error {
			if len(copies) == 0 {
				return nil
			}
			if err := writeRestoreCopies(ctx, tx, libraryDB, positionsTable, positionPolicy, copies); err != nil {
				return err
			}
			copies = copies[:0]
			return nil
		}
		seenTargets := make(map[string]struct{}, len(fileIDs))
		for _, fileID := range fileIDs {
			files := byFile[fileID]
			sort.Slice(files, func(i, j int) bool {
				if files[i].TapeId != files[j].TapeId {
					return files[i].TapeId < files[j].TapeId
				}
				return files[i].TapePath < files[j].TapePath
			})
			first := files[0]
			if first.Size < 0 {
				return fmt.Errorf("restore source has invalid size, file_id=%d size=%d", fileID, first.Size)
			}
			if len(first.Hash) != 32 {
				return fmt.Errorf("invalid restore SHA-256, file_id=%d", fileID)
			}
			if err := entity.ValidateRelativePath(first.TargetPath); err != nil {
				return fmt.Errorf("invalid restore target path, %w", err)
			}
			if _, exists := seenTargets[first.TargetPath]; exists {
				return fmt.Errorf("duplicate restore target path, path=%q", first.TargetPath)
			}
			seenTargets[first.TargetPath] = struct{}{}

			status := entity.CopyStatus_PENDING
			for _, file := range files {
				if file.Status == legacypb.CopyStatus_SUBMITED {
					status = entity.CopyStatus_COMPLETED
					break
				}
			}
			if status != entity.CopyStatus_COMPLETED {
				allCompleted = false
			}

			seenTapes := make(map[int64]struct{}, len(files))
			group := make([]*restore.Copy, 0, len(files))
			for _, file := range files {
				if file.TargetPath != first.TargetPath || file.Size != first.Size || !bytes.Equal(file.Hash, first.Hash) {
					return fmt.Errorf("inconsistent restore candidates, file_id=%d", fileID)
				}
				if err := entity.ValidateRelativePath(file.TapePath); err != nil {
					return fmt.Errorf("invalid restore candidate, file_id=%d, %w", fileID, err)
				}
				if _, exists := seenTapes[file.TapeId]; exists {
					continue
				}
				seenTapes[file.TapeId] = struct{}{}
				group = append(group, &restore.Copy{
					FileID: fileID, Status: status, Size: first.Size, Hash: append([]byte(nil), first.Hash...),
					TargetPath: first.TargetPath, MediaID: file.TapeId, MediaPath: file.TapePath,
				})
			}
			if len(copies) > 0 && len(copies)+len(group) > cap(copies) {
				if err := flush(); err != nil {
					return err
				}
			}
			copies = append(copies, group...)
			if len(copies) >= cap(copies) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		if err := flush(); err != nil {
			return err
		}

		status := entity.JobStatus_PENDING
		if allCompleted {
			status = entity.JobStatus_COMPLETED
		}
		if err := tx.Create(&restore.Config{ID: 1, Spec: &entity.RestoreJobSpec{}}).Error; err != nil {
			return err
		}
		return tx.Create(&executor.JobRecord{
			ID: 1, Kind: entity.JobKind_RESTORE, Status: status, Priority: job.Priority,
		}).Error
	})
	if err != nil {
		return 0, fmt.Errorf("write restore Job DB failed, %w", err)
	}
	return len(fileIDs), nil
}

func migrateTransitionalRestore(
	ctx context.Context,
	jobDB *gorm.DB,
	libraryDB *gorm.DB,
	positionsTable string,
	positionPolicy restorePositionPolicy,
	transitionalDB *gorm.DB,
	table string,
	job *legacyJob,
) (int, error) {
	// Keep only one logical file's physical candidates in memory at a time.
	rows, err := transitionalDB.WithContext(ctx).Table(table).Order("file_id, tape_id, id").Rows()
	if err != nil {
		return 0, fmt.Errorf("read transitional Restore Job DB failed, %w", err)
	}
	defer rows.Close()

	logicalFiles := 0
	allCompleted := true
	err = jobDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(&executor.JobRecord{}, &restore.Config{}, &restore.Copy{}); err != nil {
			return err
		}
		copies := make([]*restore.Copy, 0, 256)
		flush := func() error {
			if len(copies) == 0 {
				return nil
			}
			if err := writeRestoreCopies(ctx, tx, libraryDB, positionsTable, positionPolicy, copies); err != nil {
				return err
			}
			copies = copies[:0]
			return nil
		}
		var candidates []transitionalRestoreCopy
		writeFile := func() error {
			if len(candidates) == 0 {
				return nil
			}
			first := candidates[0]
			if first.FileID <= 0 || first.Size < 0 || len(first.Hash) != sha256.Size {
				return fmt.Errorf("invalid restore source, file_id=%d", first.FileID)
			}
			if err := entity.ValidateRelativePath(first.TargetPath); err != nil {
				return fmt.Errorf("invalid restore target path, file_id=%d, %w", first.FileID, err)
			}
			status := entity.CopyStatus_PENDING
			for _, candidate := range candidates {
				if candidate.Status == legacypb.CopyStatus_SUBMITED {
					status = entity.CopyStatus_COMPLETED
					break
				}
			}
			if status != entity.CopyStatus_COMPLETED {
				allCompleted = false
			}
			seenTapes := make(map[int64]struct{}, len(candidates))
			group := make([]*restore.Copy, 0, len(candidates))
			for _, candidate := range candidates {
				if candidate.TapeID <= 0 ||
					candidate.TargetPath != first.TargetPath ||
					candidate.Size != first.Size ||
					!bytes.Equal(candidate.Hash, first.Hash) {
					return fmt.Errorf("inconsistent restore candidates, file_id=%d", first.FileID)
				}
				tapePath := candidate.Path.String()
				if err := entity.ValidateRelativePath(tapePath); err != nil {
					return fmt.Errorf("invalid restore candidate, file_id=%d tape_id=%d, %w", first.FileID, candidate.TapeID, err)
				}
				if _, exists := seenTapes[candidate.TapeID]; exists {
					continue
				}
				seenTapes[candidate.TapeID] = struct{}{}
				group = append(group, &restore.Copy{
					ID: candidate.ID, FileID: first.FileID, Status: status, Size: first.Size,
					Hash: append([]byte(nil), first.Hash...), TargetPath: first.TargetPath,
					MediaID: candidate.TapeID, MediaPath: tapePath,
				})
			}
			if len(copies) > 0 && len(copies)+len(group) > cap(copies) {
				if err := flush(); err != nil {
					return err
				}
			}
			copies = append(copies, group...)
			if len(copies) >= cap(copies) {
				if err := flush(); err != nil {
					return err
				}
			}
			logicalFiles++
			candidates = candidates[:0]
			return nil
		}

		for rows.Next() {
			var candidate transitionalRestoreCopy
			if err := transitionalDB.ScanRows(rows, &candidate); err != nil {
				return err
			}
			if len(candidates) > 0 && candidate.FileID != candidates[0].FileID {
				if err := writeFile(); err != nil {
					return err
				}
			}
			candidates = append(candidates, candidate)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := writeFile(); err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}

		status := entity.JobStatus_PENDING
		if allCompleted {
			status = entity.JobStatus_COMPLETED
		}
		if err := tx.Create(&restore.Config{ID: 1, Spec: &entity.RestoreJobSpec{}}).Error; err != nil {
			return err
		}
		return tx.Create(&executor.JobRecord{
			ID: 1, Kind: entity.JobKind_RESTORE, Status: status, Priority: job.Priority,
		}).Error
	})
	if err != nil {
		return 0, fmt.Errorf("write restore Job DB failed, %w", err)
	}
	return logicalFiles, nil
}

func writeRestoreCopies(
	ctx context.Context,
	tx *gorm.DB,
	libraryDB *gorm.DB,
	positionsTable string,
	positionPolicy restorePositionPolicy,
	batch []*restore.Copy,
) error {
	if len(batch) == 0 {
		return nil
	}

	// Resolve the bounded candidate set against the reconciled physical Positions.
	type positionKey struct {
		TapeID int64
		Path   string
		Size   int64
	}
	type positionOrder struct {
		MediaID      int64
		Path         string
		Size         int64
		StorageOrder []byte
	}
	want := make(map[positionKey]struct{}, len(batch))
	values := make([][]any, 0, len(batch))
	for _, candidate := range batch {
		key := positionKey{TapeID: candidate.MediaID, Path: candidate.MediaPath, Size: candidate.Size}
		if _, exists := want[key]; exists {
			continue
		}
		want[key] = struct{}{}
		values = append(values, []any{key.TapeID, key.Path, key.Size})
	}
	orders := make([]positionOrder, 0, len(values))
	for offset := 0; offset < len(values); offset += 256 {
		end := offset + 256
		if end > len(values) {
			end = len(values)
		}
		var page []positionOrder
		if err := libraryDB.WithContext(ctx).Table(positionsTable).
			Select("media_id", "path", "size", "storage_order").
			Where("is_dir = ?", false).
			Where("(media_id, path, size) IN ?", values[offset:end]).
			Find(&page).Error; err != nil {
			return fmt.Errorf("read migrated Library Position order failed, %w", err)
		}
		orders = append(orders, page...)
	}
	found := make(map[positionKey][]byte, len(orders))
	for _, position := range orders {
		found[positionKey{TapeID: position.MediaID, Path: position.Path, Size: position.Size}] = position.StorageOrder
	}

	// Preserve completed history and require each pending logical File to retain a physical candidate.
	copies := make([]*restore.Copy, 0, len(batch))
	for index := 0; index < len(batch); {
		end := index + 1
		for end < len(batch) && batch[end].FileID == batch[index].FileID {
			end++
		}
		kept := 0
		missingTapes := make([]int64, 0, end-index)
		for _, candidate := range batch[index:end] {
			key := positionKey{TapeID: candidate.MediaID, Path: candidate.MediaPath, Size: candidate.Size}
			order, exists := found[key]
			if !exists && candidate.Status != entity.CopyStatus_COMPLETED && positionPolicy == restorePositionsRequired {
				missingTapes = append(missingTapes, candidate.MediaID)
				continue
			}
			candidate.StorageOrder = append([]byte{}, order...)
			candidate.ItemID = candidate.FileID
			// Preserve the migrated Media identity before numeric catalog IDs can be reused.
			mediaTable := "media_staging"
			if positionsTable != "positions_staging" {
				mediaTable = "media"
			}
			if libraryDB.Migrator().HasTable(mediaTable) {
				var media library.Media
				if err := libraryDB.WithContext(ctx).Table(mediaTable).Where("id = ?", candidate.MediaID).Limit(1).Find(&media).Error; err != nil {
					return err
				}
				candidate.MediaIdentity, candidate.MediaProfile = media.Identity, media.Profile
			}
			// legacy Jobs retain their frozen content even when no surviving Library version exists.
			versionTable := "file_versions_staging"
			if positionsTable != "positions_staging" {
				versionTable = "file_versions"
			}
			if libraryDB.Migrator().HasTable(versionTable) {
				var version library.FileVersion
				if err := libraryDB.WithContext(ctx).Table(versionTable).Where("file_id = ? AND hash = ? AND size = ?", candidate.FileID, candidate.Hash, candidate.Size).
					Order("id").Limit(1).Find(&version).Error; err != nil {
					return err
				}
				candidate.FileVersionID, candidate.Signature = version.ID, version.Signature
				candidate.Mode, candidate.MtimeNS = version.Mode, version.MtimeNS
			}
			copies = append(copies, candidate)
			kept++
		}
		if kept == 0 {
			return fmt.Errorf(
				"pending Restore file has no valid migrated Position, file_id=%d tapes=%v",
				batch[index].FileID,
				missingTapes,
			)
		}
		index = end
	}
	if err := tx.CreateInBatches(copies, 256).Error; err != nil {
		return fmt.Errorf("write migrated Restore copies failed, %w", err)
	}
	return nil
}

type tableSwap struct {
	active string
	staged string
	final  string
	backup string
}

var stagingTableSwaps = []tableSwap{
	{active: "jobs", staged: "jobs_staging", final: "jobs", backup: "jobs_legacy"},
	{active: "files", staged: "files_staging", final: "files", backup: "files_legacy"},
	{active: "tapes", staged: "media_staging", final: "media", backup: "tapes_legacy"},
	{active: "positions", staged: "positions_staging", final: "positions", backup: "positions_legacy"},
}

func Commit(ctx context.Context, db *gorm.DB, workRoot string) error {
	// Never activate staged data over an unknown or future Catalog.
	if _, err := DetectSchema(db); err != nil {
		return err
	}

	// Require a successful prepare phase before switching durable data.
	report, err := readReport(workRoot)
	if err != nil {
		return err
	}
	if !report.Success {
		return fmt.Errorf("migration report is not successful")
	}

	// The current Bundles are already at their final path; refuse to switch an incomplete preparation.
	if !exists(filepath.Join(workRoot, "jobs")) {
		return fmt.Errorf("current Job directory is missing")
	}

	// Switch the Job catalog and complete Library together after validating every table.
	if hasStaging(db) {
		var jobs []catalogJob
		if err := db.WithContext(ctx).Select("id").FindInBatches(&jobs, 100, func(_ *gorm.DB, _ int) error {
			for _, job := range jobs {
				if err := dataformat.CheckBundle(filepath.Join(workRoot, "jobs", fmt.Sprint(job.ID)), job.ID); err != nil {
					return err
				}
			}
			return nil
		}).Error; err != nil {
			return fmt.Errorf("validate prepared Job formats failed, %w", err)
		}
		if !db.Migrator().HasTable(&stagedLibraryVersion{}) {
			return fmt.Errorf("prepared FileVersion table is missing")
		}
		for _, table := range stagingTableSwaps {
			if !db.Migrator().HasTable(table.active) || !db.Migrator().HasTable(table.staged) {
				return fmt.Errorf("migration table set is incomplete, active=%q staged=%q", table.active, table.staged)
			}
			if db.Migrator().HasTable(table.backup) {
				return fmt.Errorf("legacy backup table already exists, table=%q", table.backup)
			}
		}

		if err := finishLibraryStaging(ctx, db); err != nil {
			return fmt.Errorf("finalize prepared Library model: %w", err)
		}
		var err error
		if db.Dialector.Name() == "mysql" {
			err = db.WithContext(ctx).Exec(
				"RENAME TABLE jobs TO jobs_legacy, jobs_staging TO jobs, " +
					"files TO files_legacy, files_staging TO files, " +
					"tapes TO tapes_legacy, media_staging TO media, " +
					"positions TO positions_legacy, positions_staging TO positions, file_versions_staging TO file_versions",
			).Error
		} else {
			err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				for _, table := range stagingTableSwaps {
					if err := tx.Migrator().RenameTable(table.active, table.backup); err != nil {
						return err
					}
				}
				for _, table := range stagingTableSwaps {
					if err := tx.Migrator().RenameTable(table.staged, table.final); err != nil {
						return err
					}
				}
				if err := tx.Migrator().RenameTable("file_versions_staging", "file_versions"); err != nil {
					return err
				}
				return dataformat.MarkCatalog(tx)
			})
		}
		if err != nil {
			return fmt.Errorf("activate current data failed, %w", err)
		}
		if db.Dialector.Name() == "mysql" {
			if err := dataformat.MarkCatalog(db.WithContext(ctx)); err != nil {
				return err
			}
		}
	}

	// Validate the final table names before reporting a successful commit.
	if !db.Migrator().HasTable("file_versions") {
		return fmt.Errorf("FileVersion table is missing after commit")
	}
	for _, table := range stagingTableSwaps {
		if !db.Migrator().HasTable(table.final) || !db.Migrator().HasTable(table.backup) {
			return fmt.Errorf("unexpected table state after commit, active=%q backup=%q", table.final, table.backup)
		}
	}
	_, err = dataformat.CheckCatalog(db)
	return err
}

func RepairJob(ctx context.Context, db *gorm.DB, workRoot string, jobID int64, restoreRoot ...string) error {
	// Repair operates only on a recognized published installation.
	if _, err := dataformat.CheckCatalog(db); err != nil {
		return err
	}
	if err := dataformat.CheckBundles(db, workRoot); err != nil {
		return err
	}

	// Only committed, retained legacy Jobs can be rebuilt by this offline operation.
	if jobID <= 0 {
		return fmt.Errorf("invalid Job ID %d", jobID)
	}
	if !db.Migrator().HasTable("jobs_legacy") {
		return fmt.Errorf("legacy Job table backup is missing")
	}

	// Load the frozen legacy catalog row retained by the committed migration.
	var job legacyJob
	result := db.WithContext(ctx).Table("jobs_legacy").First(&job, jobID)
	if result.Error != nil {
		return fmt.Errorf("read legacy Job backup failed, id=%d, %w", jobID, result.Error)
	}
	if job.Status == legacypb.JobStatus_DELETED {
		return fmt.Errorf("cannot repair deleted legacy Job, id=%d", jobID)
	}

	// Rebuild the Bundle beside the active directory and retain the replaced Bundle for inspection.
	jobsRoot := filepath.Join(workRoot, "jobs")
	legacyJobsRoot := filepath.Join(workRoot, legacyJobsDirectory)
	repairRoot := filepath.Join(workRoot, ".migration-repair")
	preparedDir := filepath.Join(repairRoot, fmt.Sprint(jobID))
	activeDir := filepath.Join(jobsRoot, fmt.Sprint(jobID))
	backupDir := filepath.Join(workRoot, "jobs-before-repair", fmt.Sprint(jobID))
	if !exists(activeDir) {
		return fmt.Errorf("active current Job Bundle is missing, id=%d", jobID)
	}
	if exists(backupDir) {
		return fmt.Errorf("current Job repair backup already exists, path=%q", backupDir)
	}

	// A repaired Restore keeps its original destination even after the startup configuration changes.
	if job.State.GetRestore() != nil {
		activeDB, err := resource.OpenSQLite(filepath.Join(activeDir, "state.db"))
		if err != nil {
			return fmt.Errorf("open active Restore Bundle failed, id=%d, %w", jobID, err)
		}
		var config restore.Config
		err = activeDB.WithContext(ctx).Select("legacy_root").First(&config, 1).Error
		closeDB(activeDB)
		if err != nil {
			return fmt.Errorf("read frozen Restore destination failed, id=%d, %w", jobID, err)
		}
		if config.LegacyRoot != "" {
			restoreRoot = []string{config.LegacyRoot}
		}
	}

	// Build the replacement without altering the active Bundle or its physical output.
	if err := os.RemoveAll(preparedDir); err != nil {
		return fmt.Errorf("discard prior repair output failed, id=%d, %w", jobID, err)
	}
	counts, _, err := prepareJob(
		ctx,
		db,
		"positions",
		restorePositionsOptional,
		repairRoot,
		legacyJobsRoot,
		&job,
		restoreRoot...,
	)
	if err != nil {
		return fmt.Errorf("rebuild current Job Bundle failed, id=%d, %w", jobID, err)
	}
	if counts.archive+counts.restore == 0 {
		return fmt.Errorf("transitional Job DB has no recoverable items, id=%d", jobID)
	}

	// Switch only this Job after the replacement database is complete.
	if err := os.MkdirAll(filepath.Dir(backupDir), 0o755); err != nil {
		return fmt.Errorf("create Job repair backup directory failed, %w", err)
	}
	if err := os.Rename(activeDir, backupDir); err != nil {
		return fmt.Errorf("preserve active current Job Bundle failed, id=%d, %w", jobID, err)
	}
	if err := os.Rename(preparedDir, activeDir); err != nil {
		rollbackErr := os.Rename(backupDir, activeDir)
		return errors.Join(fmt.Errorf("activate repaired current Job Bundle failed, id=%d, %w", jobID, err), rollbackErr)
	}
	if err := os.Remove(repairRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove empty Job repair directory failed, %w", err)
	}
	return nil
}

func Cleanup(db *gorm.DB, workRoot string) error {
	// Never remove retained legacy data from an unsupported installation.
	if _, err := dataformat.CheckCatalog(db); err != nil {
		return err
	}
	if err := dataformat.CheckBundles(db, workRoot); err != nil {
		return err
	}

	// Refuse cleanup while any current data is still staged.
	if hasStaging(db) {
		return fmt.Errorf("current staging tables still exist; commit migration first")
	}
	for _, table := range stagingTableSwaps {
		if !db.Migrator().HasTable(table.backup) {
			return fmt.Errorf("legacy backup table is missing; commit migration first, table=%q", table.backup)
		}
	}

	// Remove legacy backups only after Commit has activated all current tables.
	for index := len(stagingTableSwaps) - 1; index >= 0; index-- {
		backup := stagingTableSwaps[index].backup
		if db.Migrator().HasTable(backup) {
			if err := db.Migrator().DropTable(backup); err != nil {
				return fmt.Errorf("drop legacy backup table failed, table=%q, %w", backup, err)
			}
		}
	}
	if err := os.RemoveAll(filepath.Join(workRoot, legacyJobsDirectory)); err != nil {
		return fmt.Errorf("remove legacy Job directory backup failed, %w", err)
	}

	// Remove the report after all migration-only database state is gone.
	return removeReport(workRoot)
}

func Abort(db *gorm.DB, workRoot string) error {
	// Unsupported data must remain unchanged even when the requested operation is cleanup.
	if _, err := DetectSchema(db); err != nil {
		return err
	}

	// A committed migration owns the active Job directory and legacy backup tables.
	for _, table := range stagingTableSwaps {
		if db.Migrator().HasTable(table.backup) {
			return fmt.Errorf("migration is already committed; use cleanup after validation")
		}
	}

	// Remove current output and its ownership tables before restoring any preserved legacy directory.
	jobsRoot := filepath.Join(workRoot, "jobs")
	legacyJobsRoot := filepath.Join(workRoot, legacyJobsDirectory)
	if hasStaging(db) {
		if err := os.RemoveAll(jobsRoot); err != nil {
			return fmt.Errorf("remove current Job directory failed, %w", err)
		}
		for index := len(stagingTableSwaps) - 1; index >= 0; index-- {
			staged := stagingTableSwaps[index].staged
			if !db.Migrator().HasTable(staged) {
				continue
			}
			if err := db.Migrator().DropTable(staged); err != nil {
				return fmt.Errorf("drop current staging table failed, table=%q, %w", staged, err)
			}
		}
	}

	// Complete a pending directory restore after all current ownership evidence is gone.
	if db.Migrator().HasTable(&stagedLibraryVersion{}) {
		if err := db.Migrator().DropTable(&stagedLibraryVersion{}); err != nil {
			return err
		}
	}
	if exists(legacyJobsRoot) {
		if exists(jobsRoot) {
			return fmt.Errorf("cannot restore legacy Job directory because the active path exists")
		}
		if err := os.Rename(legacyJobsRoot, jobsRoot); err != nil {
			return fmt.Errorf("restore legacy Job directory failed, %w", err)
		}
	}
	return removeReport(workRoot)
}

func hasStaging(db *gorm.DB) bool {
	if db.Migrator().HasTable(&stagedLibraryVersion{}) {
		return true
	}
	for _, table := range stagingTableSwaps {
		if db.Migrator().HasTable(table.staged) {
			return true
		}
	}
	return false
}

func removeReport(workRoot string) error {
	if err := os.Remove(filepath.Join(workRoot, reportFilename)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove migration report failed, %w", err)
	}
	return nil
}

func migratedCopyStatus(status legacypb.CopyStatus) entity.CopyStatus {
	if status == legacypb.CopyStatus_SUBMITED {
		return entity.CopyStatus_SUBMITTED
	}
	return entity.CopyStatus_PENDING
}

func writeReport(workRoot string, report *Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode migration report failed, %w", err)
	}
	if err := os.WriteFile(filepath.Join(workRoot, reportFilename), data, 0o644); err != nil {
		return fmt.Errorf("write migration report failed, %w", err)
	}
	return nil
}

func readReport(workRoot string) (*Report, error) {
	data, err := os.ReadFile(filepath.Join(workRoot, reportFilename))
	if err != nil {
		return nil, fmt.Errorf("read migration report failed, %w", err)
	}
	report := new(Report)
	if err := json.Unmarshal(data, report); err != nil {
		return nil, fmt.Errorf("decode migration report failed, %w", err)
	}
	return report, nil
}

func closeDB(db *gorm.DB) {
	sqlDB, err := db.DB()
	if err == nil {
		_ = sqlDB.Close()
	}
}

func exists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}
