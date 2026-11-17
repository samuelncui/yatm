package legacy

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/executor/archive"
	"github.com/samuelncui/yatm/internal/executor/restore"
	"github.com/samuelncui/yatm/internal/library"
	legacypb "github.com/samuelncui/yatm/internal/migrate/legacy/pb"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// prepareLegacy prepares from the conventional captured-index directory without a Restore target.
func prepareLegacy(ctx context.Context, db *gorm.DB, root string) (*Report, error) {
	return PrepareWithLTFSIndex(ctx, db, root, filepath.Join(root, legacyLTFSIndexDirectory), "")
}

func TestPrepareCommitAndCleanup(t *testing.T) {
	// Build representative legacy Archive and Restore rows, including an interrupted item.
	ctx := context.Background()
	db := newLegacyTestDB(t)

	root := t.TempDir()
	hash := sha256.Sum256([]byte("restore"))
	archiveState := &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{
		Sources: []*legacypb.SourceState{
			{Source: &legacypb.Source{Base: root, Path: []string{"a.txt"}}, Size: 1, Status: legacypb.CopyStatus_SUBMITED},
			{Source: &legacypb.Source{Base: root, Path: []string{"b.txt"}}, Size: 2, Status: legacypb.CopyStatus_RUNNING},
		},
	}}}
	restoreState := &legacypb.JobState{State: &legacypb.JobState_Restore{Restore: &legacypb.JobRestoreState{
		Tapes: []*legacypb.RestoreTape{{
			TapeId: 10,
			Files: []*legacypb.RestoreFile{{
				FileId: 20, TapeId: 10, PositionId: 30, Status: legacypb.CopyStatus_SUBMITED,
				Size: 7, Hash: hash[:], TapePath: "directory/restore.txt", TargetPath: "restore.txt",
			}},
		}},
	}}}
	jobs := []*legacyJob{
		{
			ID: 1, Status: legacypb.JobStatus_PROCESSING, Priority: 3, CreateTime: time.Unix(1, 0),
			State: archiveState,
		},
		{
			ID: 2, Status: legacypb.JobStatus_COMPLETED, Priority: 5, CreateTime: time.Unix(2, 0),
			State: restoreState,
		},
	}
	require.NoError(t, db.Create(jobs).Error)
	require.NoError(t, db.Create(&legacyLibraryFile{
		ID: 20, Name: "restore.txt", Mode: 0o644, Size: 7, Hash: hash[:],
	}).Error)
	require.NoError(t, db.Create(&legacyLibraryTape{ID: 10, Barcode: "ABC010", CreateTime: time.Unix(3, 0)}).Error)
	require.NoError(t, db.Create(&legacyLibraryPosition{
		ID: 30, FileID: 20, TapeID: 10, Path: "directory/restore.txt", Mode: 0o644, Size: 7, Hash: hash[:],
	}).Error)
	indexRoot := filepath.Join(root, legacyLTFSIndexDirectory)
	require.NoError(t, os.MkdirAll(indexRoot, 0o755))
	index := `<ltfsindex><directory><name>ABC010</name><contents>` +
		`<directory><name>directory</name><contents>` +
		`<file><name>restore.txt</name><length>7</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>40</startblock><byteoffset>3</byteoffset>` +
		`<bytecount>7</bytecount><fileoffset>0</fileoffset>` +
		`</extent></extentinfo></file></contents></directory>` +
		`</contents></directory></ltfsindex>`
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "ABC010.schema"), []byte(index), 0o644))

	// Prepare a complete current catalog and one independent state database per Job.
	restoreRoot := filepath.Join(t.TempDir(), "configured-restore-output")
	backup := preserveTestBackup(t, db, root)
	backup.RestoreRoot = restoreRoot
	report, err := PrepareWithLTFSIndex(ctx, db, root, indexRoot, restoreRoot)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Equal(t, 2, report.MigratedJobs)
	require.Equal(t, 2, report.ArchiveItems)
	require.Equal(t, 1, report.RestoreItems)
	require.Equal(t, 1, report.LibraryFiles)
	require.Equal(t, 1, report.LibraryTapes)
	require.Equal(t, 1, report.LibraryPositions)
	require.Equal(t, 1, report.LibraryDirectories)
	require.Equal(t, 1, report.LibraryStoragePositions)
	require.DirExists(t, filepath.Join(root, "jobs", "1", "tapes"))
	require.NoDirExists(t, filepath.Join(root, "jobs", "1", "logs"))
	require.NoDirExists(t, filepath.Join(root, "jobs", "1", "reports"))

	// Verify Archive processing state rolls back to the last submitted item.
	archiveDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	var archiveRecord executor.JobRecord
	require.NoError(t, archiveDB.First(&archiveRecord, 1).Error)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, archiveRecord.Status)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, archiveRecord.Checkpoint)
	var archiveItems []*archive.Item
	require.NoError(t, archiveDB.Order("id").Find(&archiveItems).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, archiveItems[0].Status)
	require.Empty(t, archiveItems[0].MediaPath)
	require.Nil(t, archiveItems[0].MediaID)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, archiveItems[1].Status)
	require.Equal(t, filepath.Join(root, "b.txt"), archiveItems[1].Data.SourcePath)
	require.Nil(t, archiveItems[1].Data.Expected, "legacy pending content retains its explicit original source")
	require.Contains(t, report.Warnings, "job 1 reset 1 submitted archive items because no Media ID was recoverable from the legacy Job log")
	_ = resource.CloseDB(archiveDB)

	// Verify an already-complete Restore preserves its selected candidate.
	restoreDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "2", "state.db"))
	require.NoError(t, err)
	var restoreRecord executor.JobRecord
	require.NoError(t, restoreDB.First(&restoreRecord, 1).Error)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, restoreRecord.Status)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, restoreRecord.Checkpoint)
	var restoreCopy restore.Copy
	require.NoError(t, restoreDB.First(&restoreCopy, 1).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_COMPLETED, restoreCopy.Status)
	var restoreFile restore.File
	require.NoError(t, restoreDB.First(&restoreFile, "item_id = ?", restoreCopy.ItemID).Error)
	require.Equal(t, int64(20), restoreFile.FileID)
	require.Equal(t, int64(7), restoreFile.Size)
	require.Equal(t, hash[:], restoreFile.Hash)
	require.True(t, restoreFile.Completed)
	require.Equal(t, int64(10), restoreCopy.MediaID)
	require.Len(t, restoreCopy.StorageOrder, 17)
	require.Equal(t, byte('b'), restoreCopy.StorageOrder[0])
	require.Equal(t, uint64(40), binary.BigEndian.Uint64(restoreCopy.StorageOrder[1:9]))
	require.Equal(t, uint64(3), binary.BigEndian.Uint64(restoreCopy.StorageOrder[9:17]))
	var restoreConfig restore.Config
	require.NoError(t, restoreDB.First(&restoreConfig, 1).Error)
	canonicalRestoreRoot, err := executor.CanonicalConfiguredPath(restoreRoot)
	require.NoError(t, err)
	require.Equal(t, canonicalRestoreRoot, restoreConfig.LegacyRoot)
	require.Nil(t, restoreConfig.Spec.GetDestination(), "legacy frozen output is not an invented Location registration")
	require.Equal(t, "restore.txt", restoreFile.Path)
	require.Equal(t, restoreFile.Path, restoreFile.DesiredPath)
	require.NoDirExists(t, restoreRoot, "preparation must not create output directories")
	_ = resource.CloseDB(restoreDB)

	// Activate the staged data once; a repeated commit must converge on the same layout.
	require.NoError(t, Commit(ctx, db, root))
	require.NoError(t, Commit(ctx, db, root))
	empty, err := dataformat.CheckCatalog(db)
	require.NoError(t, err)
	require.False(t, empty)
	require.NoError(t, dataformat.CheckBundles(db, root))
	var revision int
	require.NoError(t, db.Table(dataformat.CatalogTable).Select("revision").Scan(&revision).Error)
	require.Equal(t, dataformat.CatalogRevision, revision)
	require.True(t, db.Migrator().HasTable("jobs"))
	require.True(t, db.Migrator().HasTable("job_properties"))
	for _, index := range []string{
		"idx_executor_id", "idx_job_updated_at", "idx_job_deleted_at", "idx_job_revision",
		"idx_jobs_location", "idx_jobs_media", "idx_jobs_kind",
	} {
		require.True(t, db.Migrator().HasIndex(executor.ModelJob, index), index)
	}
	var properties []executor.JobProperty
	require.NoError(t, db.Order("job_id").Find(&properties).Error)
	require.Equal(t, []executor.JobProperty{{JobID: 2, Key: executor.JobPropertyMedia, Value: 10}}, properties)
	require.True(t, db.Migrator().HasTable("jobs_legacy"))
	require.True(t, db.Migrator().HasTable("positions_legacy"))
	indexes, err := db.Migrator().GetIndexes(library.ModelPosition)
	require.NoError(t, err)
	require.True(t, db.Migrator().HasIndex(library.ModelPosition, "idx_positions_media_parent"), "%v", indexes)
	for _, index := range indexes {
		if index.Name() == "idx_positions_media_parent" {
			require.Equal(t, []string{"media_id", "parent_path", "path"}, index.Columns())
		}
	}
	require.DirExists(t, filepath.Join(root, "jobs"))
	var catalogs []*catalogJob
	require.NoError(t, db.Table("jobs").Order("id").Find(&catalogs).Error)
	require.Equal(t, []int64{1, 2}, []int64{catalogs[0].Revision, catalogs[1].Revision})
	positions, _, err := library.New(db).ListPositionsPage(ctx, 10, "", "", 1000)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "directory/", positions[0].Path)

	// The activated catalog must continue allocating IDs after the migrated range.
	created := &catalogJob{ExecutorID: executorID}
	require.NoError(t, db.Table("jobs").Create(created).Error)
	require.Equal(t, int64(3), created.ID)
	require.NoError(t, db.Table("jobs").Delete(created).Error)

	// Validation compares shared Restore content and search properties, not only candidate counts.
	restoreDB, err = resource.OpenSQLite(filepath.Join(root, "jobs", "2", "state.db"))
	require.NoError(t, err)
	require.NoError(t, restoreDB.Model(&restore.File{}).Where("item_id = ?", restoreFile.ItemID).Update("size", 99).Error)
	_, err = Validate(ctx, db, root, backup)
	require.ErrorContains(t, err, "field size differs")
	require.NoError(t, restoreDB.Model(&restore.File{}).Where("item_id = ?", restoreFile.ItemID).Update("size", restoreFile.Size).Error)
	_ = resource.CloseDB(restoreDB)
	require.NoError(t, db.Where("job_id = ?", 2).Delete(&executor.JobProperty{}).Error)
	_, err = Validate(ctx, db, root, backup)
	require.ErrorContains(t, err, "Media search properties differ")
	require.NoError(t, db.Create(&properties).Error)

	// Cleanup removes only the user-confirmed legacy table backup.
	require.NoError(t, Cleanup(ctx, db, root, backup))
	require.NoError(t, Cleanup(ctx, db, root, backup))
	require.False(t, db.Migrator().HasTable("jobs_legacy"))
	require.False(t, db.Migrator().HasTable("files_legacy"))
	require.False(t, db.Migrator().HasTable("tapes_legacy"))
	require.False(t, db.Migrator().HasTable("positions_legacy"))
	require.DirExists(t, filepath.Join(root, "jobs"))
}

func TestPrepareRecoversArchiveMediaFromLegacyJobLog(t *testing.T) {
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	jobLog := "create tape success, tape_id= 9\ncreate tape success, tape_id= 10\narchive complete\n"

	require.NoError(t, db.Create(&legacyJob{
		ID: 1, Status: legacypb.JobStatus_COMPLETED, CreateTime: time.Unix(1, 0),
		State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{
			Sources: []*legacypb.SourceState{
				{Source: &legacypb.Source{Base: root, Path: []string{"file.txt"}}, Size: 7, Status: legacypb.CopyStatus_SUBMITED},
				{Source: &legacypb.Source{Base: root, Path: []string{"missing.txt"}}, Size: 3, Status: legacypb.CopyStatus_SUBMITED},
				{Source: &legacypb.Source{Base: root, Path: []string{"ambiguous.txt"}}, Size: 5, Status: legacypb.CopyStatus_SUBMITED},
			},
		}}},
	}).Error)
	require.NoError(t, db.Create([]*legacyLibraryFile{
		{ID: 20, Name: "file.txt", Mode: 0o644, Size: 7},
		{ID: 21, Name: "ambiguous.txt", Mode: 0o644, Size: 5},
	}).Error)
	require.NoError(t, db.Create([]*legacyLibraryTape{
		{ID: 9, Barcode: "ABC009"},
		{ID: 10, Barcode: "ABC010"},
	}).Error)
	require.NoError(t, db.Create([]*legacyLibraryPosition{
		{ID: 30, FileID: 20, TapeID: 10, Path: "file.txt", Mode: 0o644, Size: 7},
		{ID: 31, FileID: 21, TapeID: 9, Path: "ambiguous.txt", Mode: 0o644, Size: 5},
		{ID: 32, FileID: 21, TapeID: 10, Path: "ambiguous.txt", Mode: 0o644, Size: 5},
	}).Error)
	indexRoot := filepath.Join(root, legacyLTFSIndexDirectory)
	require.NoError(t, os.MkdirAll(indexRoot, 0o755))
	index9 := `<ltfsindex><directory><name>ABC009</name><contents>` +
		`<file><name>ambiguous.txt</name><length>5</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>30</startblock><byteoffset>0</byteoffset>` +
		`<bytecount>5</bytecount><fileoffset>0</fileoffset>` +
		`</extent></extentinfo></file></contents></directory></ltfsindex>`
	index10 := `<ltfsindex><directory><name>ABC010</name><contents>` +
		`<file><name>file.txt</name><length>7</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>40</startblock><byteoffset>0</byteoffset>` +
		`<bytecount>7</bytecount><fileoffset>0</fileoffset>` +
		`</extent></extentinfo></file>` +
		`<file><name>ambiguous.txt</name><length>5</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>50</startblock><byteoffset>0</byteoffset>` +
		`<bytecount>5</bytecount><fileoffset>0</fileoffset>` +
		`</extent></extentinfo></file></contents></directory></ltfsindex>`
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "ABC009.schema"), []byte(index9), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "ABC010.schema"), []byte(index10), 0o644))
	logRoot := filepath.Join(root, legacyJobLogsDirectory)
	require.NoError(t, os.MkdirAll(logRoot, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(logRoot, "1.log"), []byte(jobLog), 0o644))

	report, err := prepareLegacy(ctx, db, root)
	require.NoError(t, err)
	require.Contains(t, report.Warnings, "job 1 reset 2 submitted archive items absent from or ambiguous across logged Media [9 10]")
	copiedLog, err := os.ReadFile(filepath.Join(root, "jobs", "1", "job.log"))
	require.NoError(t, err)
	require.Equal(t, jobLog, string(copiedLog))

	jobDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	defer resource.CloseDB(jobDB)
	var items []*archive.Item
	require.NoError(t, jobDB.Order("id").Find(&items).Error)
	require.Len(t, items, 3)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_SUBMITTED, items[0].Status)
	require.NotNil(t, items[0].MediaID)
	require.Equal(t, int64(10), *items[0].MediaID)
	require.Equal(t, "file.txt", items[0].MediaPath)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, items[1].Status)
	require.Nil(t, items[1].MediaID)
	require.Empty(t, items[1].MediaPath)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, items[2].Status)
	require.Nil(t, items[2].MediaID)
	require.Empty(t, items[2].MediaPath)
	var record executor.JobRecord
	require.NoError(t, jobDB.First(&record, 1).Error)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, record.Status)
}

func TestPrepareMigratesLibraryAcrossBatches(t *testing.T) {
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()

	// Cross every migration batch boundary with stable legacy identities and paths.
	const physicalCount = 257
	files := make([]*legacyLibraryFile, 0, physicalCount+1)
	files = append(files, &legacyLibraryFile{ID: library.TrashFileID, Name: ".Trash", Mode: uint32(os.ModeDir | 0o777)})
	tapes := make([]*legacyLibraryTape, 0, physicalCount)
	positions := make([]*legacyLibraryPosition, 0, physicalCount)
	for id := int64(1); id <= physicalCount; id++ {
		files = append(files, &legacyLibraryFile{ID: id, Name: fmt.Sprintf("file-%03d", id), Mode: 0o644, Size: id})
		tapes = append(tapes, &legacyLibraryTape{ID: id, Barcode: fmt.Sprintf("T%06d", id)})
		positions = append(positions, &legacyLibraryPosition{
			ID: id, FileID: id, TapeID: 1, Path: fmt.Sprintf("directory/file-%03d", id), Mode: 0o644, Size: id,
		})
	}
	require.NoError(t, db.CreateInBatches(files, 100).Error)
	require.NoError(t, db.CreateInBatches(tapes, 100).Error)
	require.NoError(t, db.CreateInBatches(positions, 100).Error)

	// Prepare and activate all Library tables with a rebuilt Position directory index.
	report, err := prepareLegacy(ctx, db, root)
	require.NoError(t, err)
	require.Equal(t, physicalCount+1, report.LibraryFiles)
	require.Equal(t, physicalCount, report.LibraryTapes)
	require.Equal(t, physicalCount, report.LibraryPositions)
	require.Equal(t, 1, report.LibraryDirectories)
	// Rebuilding the directory index keeps each physical row's legacy owner.
	var owner int64
	require.NoError(t, db.Raw("SELECT file_id FROM positions_staging WHERE id = ?", physicalCount).Scan(&owner).Error)
	require.Equal(t, int64(physicalCount), owner)
	require.NoError(t, Commit(ctx, db, root))

	// Verify the last rows crossed the cursor boundary and remain directly listable.
	var fileCount, tapeCount, positionCount int64
	require.NoError(t, db.Model(library.ModelFile).Count(&fileCount).Error)
	require.NoError(t, db.Model(library.ModelMedia).Count(&tapeCount).Error)
	require.NoError(t, db.Model(&library.Position{}).Count(&positionCount).Error)
	require.Equal(t, int64(physicalCount+1), fileCount)
	require.Equal(t, int64(physicalCount), tapeCount)
	require.Equal(t, int64(physicalCount+1), positionCount)
	lib := library.New(db)
	rootPositions, _, err := lib.ListPositionsPage(ctx, 1, "", "", 1000)
	require.NoError(t, err)
	require.Len(t, rootPositions, 1)
	require.Equal(t, int64(physicalCount*(physicalCount+1)/2), rootPositions[0].Size)
	children, _, err := lib.ListPositionsPage(ctx, 1, "directory/", "", 1000)
	require.NoError(t, err)
	require.Len(t, children, physicalCount)
	require.Equal(t, int64(physicalCount), children[physicalCount-1].ID)
	require.Equal(t, "directory/file-257", children[physicalCount-1].Path)
	var storageType string
	require.NoError(t, db.Raw("SELECT typeof(storage_order) FROM positions WHERE id = ?", 1).Scan(&storageType).Error)
	require.Equal(t, "blob", storageType)
	require.Empty(t, children[0].StorageOrder)
}

func TestPrepareImportsCapturedLTFSIndex(t *testing.T) {
	// Build one legacy Tape and a captured index that also contains an untracked file.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	require.NoError(t, db.Create(&legacyLibraryTape{ID: 10, Barcode: "ABC010"}).Error)
	require.NoError(t, db.Create(&legacyLibraryPosition{
		ID: 30, FileID: 20, TapeID: 10, Path: "directory/file.txt", Mode: 0o644, Size: 7,
	}).Error)
	indexRoot := filepath.Join(root, legacyLTFSIndexDirectory)
	require.NoError(t, os.MkdirAll(indexRoot, 0o755))
	index := `<ltfsindex><directory><name>ABC010</name><contents>` +
		`<directory><name>directory</name><contents>` +
		`<file><name>file.txt</name><length>7</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>40</startblock><byteoffset>3</byteoffset>` +
		`<bytecount>7</bytecount><fileoffset>0</fileoffset>` +
		`</extent></extentinfo></file></contents></directory>` +
		`<file><name>untracked.txt</name><length>0</length></file>` +
		`</contents></directory></ltfsindex>`
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "ABC010.schema"), []byte(index), 0o644))

	// Prepare the current Library and attach the matching physical Tape position.
	report, err := prepareLegacy(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Equal(t, 1, report.LibraryStoragePositions)
	require.Empty(t, report.Warnings)
	position := new(stagedLibraryPosition)
	require.NoError(t, db.Where("id = ?", 30).First(position).Error)
	require.Len(t, position.StorageOrder, 17)
	require.Equal(t, byte('b'), position.StorageOrder[0])
	require.Equal(t, uint64(40), binary.BigEndian.Uint64(position.StorageOrder[1:9]))
	require.Equal(t, uint64(3), binary.BigEndian.Uint64(position.StorageOrder[9:17]))
	require.Len(t, position.StorageMetadata.GetLtfs().Extents, 1)
	tape := new(stagedLibraryMedia)
	require.NoError(t, db.First(tape, 10).Error)
	require.Equal(t, library.TapeFormatLTFSV1, tape.Profile.GetTape().Format)
}

func TestPrepareOmitsCapturedLTFSIndexSizeMismatch(t *testing.T) {
	// Give one logical File a bad indexed copy and a valid legacy fallback copy.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	hash := sha256.Sum256([]byte("file"))
	require.NoError(t, db.Create(&legacyLibraryFile{
		ID: 20, Name: "file.txt", Mode: 0o644, Size: 8, Hash: hash[:],
	}).Error)
	require.NoError(t, db.Create([]*legacyLibraryTape{
		{ID: 10, Barcode: "abc010", WritenBytes: 8},
		{ID: 11, Barcode: " ABC011 ", WritenBytes: 8},
	}).Error)
	require.NoError(t, db.Create([]*legacyLibraryPosition{
		{ID: 30, FileID: 20, TapeID: 10, Path: "file.txt", Mode: 0o644, Size: 8, Hash: hash[:]},
		{ID: 31, FileID: 20, TapeID: 11, Path: "file.txt", Mode: 0o644, Size: 8, Hash: hash[:]},
	}).Error)
	require.NoError(t, db.Create(&legacyJob{
		ID: 1, Status: legacypb.JobStatus_JOB_PENDING,
		State: &legacypb.JobState{State: &legacypb.JobState_Restore{Restore: &legacypb.JobRestoreState{
			Tapes: []*legacypb.RestoreTape{
				{TapeId: 10, Files: []*legacypb.RestoreFile{{
					FileId: 20, TapeId: 10, PositionId: 30, Size: 8, Hash: hash[:],
					TapePath: "file.txt", TargetPath: "file.txt",
				}}},
				{TapeId: 11, Files: []*legacypb.RestoreFile{{
					FileId: 20, TapeId: 11, PositionId: 31, Size: 8, Hash: hash[:],
					TapePath: "file.txt", TargetPath: "file.txt",
				}}},
			},
		}}},
	}).Error)
	indexRoot := filepath.Join(root, legacyLTFSIndexDirectory)
	require.NoError(t, os.MkdirAll(indexRoot, 0o755))
	index := `<ltfsindex><directory><name>ABC010</name><contents>` +
		`<file><name>file.txt</name><length>7</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>40</startblock><byteoffset>0</byteoffset>` +
		`<bytecount>7</bytecount><fileoffset>0</fileoffset>` +
		`</extent></extentinfo></file></contents></directory></ltfsindex>`
	// Legacy scripts named the capture after the barcode as recorded, before normalization.
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "abc010.schema"), []byte(index), 0o644))

	// Keep the File and its valid copy while removing the physically inconsistent Position.
	report, err := prepareLegacy(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Equal(t, 1, report.LibraryFiles)
	require.Equal(t, 2, report.LibraryTapes)
	require.Equal(t, 1, report.LibraryPositions)
	require.Zero(t, report.LibraryStoragePositions)
	require.Contains(t, report.Warnings, `Tape ABC010 Position "file.txt" was omitted: legacy size=8 LTFS size=7`)
	var positions []*stagedLibraryPosition
	require.NoError(t, db.Where("is_dir = ?", false).Order("id").Find(&positions).Error)
	require.Len(t, positions, 1)
	require.Equal(t, int64(31), positions[0].ID)
	expectedSignature, err := library.NewFileSignature(hash[:], 8)
	require.NoError(t, err)
	require.Equal(t, expectedSignature, positions[0].Signature)
	require.Empty(t, positions[0].StorageOrder)
	require.Nil(t, positions[0].StorageMetadata)

	// The unchecked fallback remains the File's confirmed saved content without retaining the mismatched copy.
	var versions []*stagedLibraryVersion
	require.NoError(t, db.Order("id").Find(&versions).Error)
	require.Len(t, versions, 1)
	require.Equal(t, int64(20), versions[0].FileID)
	require.Equal(t, expectedSignature, versions[0].Signature)
	require.Equal(t, hash[:], versions[0].Hash)
	require.Equal(t, int64(8), versions[0].Size)
	var tapes []*stagedLibraryMedia
	require.NoError(t, db.Order("id").Find(&tapes).Error)
	require.Zero(t, tapes[0].WrittenBytes)
	require.Equal(t, int64(8), tapes[1].WrittenBytes)
	require.Equal(t, []string{"ABC010", "ABC011"}, []string{tapes[0].Identity, tapes[1].Identity})
	restoreDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	var copies []*restore.Copy
	require.NoError(t, restoreDB.Find(&copies).Error)
	require.Len(t, copies, 1)
	require.Equal(t, int64(11), copies[0].MediaID)
	_ = resource.CloseDB(restoreDB)

	// Activation retains the current health fields with their unchecked defaults.
	require.NoError(t, Commit(ctx, db, root))
	require.NoError(t, library.New(db).AutoMigrate())
	position := new(library.Position)
	require.NoError(t, db.First(position, 31).Error)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, position.Health)
	require.Zero(t, position.CheckedAtNS)
	require.ErrorIs(t, db.First(new(library.Position), 30).Error, gorm.ErrRecordNotFound)
}

func TestPrepareRejectsPendingRestoreWithoutValidPosition(t *testing.T) {
	// Build one pending Restore whose sole candidate disagrees with the captured Index.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	hash := sha256.Sum256([]byte("file"))
	require.NoError(t, db.Create(&legacyLibraryTape{ID: 10, Barcode: "ABC010"}).Error)
	require.NoError(t, db.Create(&legacyLibraryPosition{
		ID: 30, FileID: 20, TapeID: 10, Path: "file.txt", Mode: 0o644, Size: 8, Hash: hash[:],
	}).Error)
	require.NoError(t, db.Create(&legacyJob{
		ID: 1, Status: legacypb.JobStatus_JOB_PENDING,
		State: &legacypb.JobState{State: &legacypb.JobState_Restore{Restore: &legacypb.JobRestoreState{
			Tapes: []*legacypb.RestoreTape{{TapeId: 10, Files: []*legacypb.RestoreFile{{
				FileId: 20, TapeId: 10, PositionId: 30, Size: 8, Hash: hash[:],
				TapePath: "file.txt", TargetPath: "file.txt",
			}}}},
		}}},
	}).Error)
	indexRoot := filepath.Join(root, legacyLTFSIndexDirectory)
	require.NoError(t, os.MkdirAll(indexRoot, 0o755))
	index := `<ltfsindex><directory><name>ABC010</name><contents>` +
		`<file><name>file.txt</name><length>7</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>40</startblock><byteoffset>0</byteoffset>` +
		`<bytecount>7</bytecount><fileoffset>0</fileoffset>` +
		`</extent></extentinfo></file></contents></directory></ltfsindex>`
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "ABC010.schema"), []byte(index), 0o644))

	// Refuse to create a pending Job that has no restorable physical candidate.
	report, err := prepareLegacy(ctx, db, root)
	require.ErrorContains(t, err, "pending Restore file has no valid migrated Position, file_id=20 tapes=[10]")
	require.False(t, report.Success)
}

func TestPrepareOmitsCapturedLTFSIndexMissingLibraryPosition(t *testing.T) {
	// Build a captured index that covers only the first of two Library Positions.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	require.NoError(t, db.Create(&legacyLibraryTape{ID: 10, Barcode: "ABC010"}).Error)
	require.NoError(t, db.Create([]*legacyLibraryPosition{
		{ID: 30, FileID: 20, TapeID: 10, Path: "first.txt", Mode: 0o644, Size: 0},
		{ID: 31, FileID: 21, TapeID: 10, Path: "missing.txt", Mode: 0o644, Size: 7},
	}).Error)
	indexRoot := filepath.Join(root, legacyLTFSIndexDirectory)
	require.NoError(t, os.MkdirAll(indexRoot, 0o755))
	index := `<ltfsindex><directory><name>ABC010</name><contents>` +
		`<file><name>first.txt</name><length>0</length></file>` +
		`</contents></directory></ltfsindex>`
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "ABC010.schema"), []byte(index), 0o644))

	// Publish only the Position confirmed by the captured index.
	report, err := prepareLegacy(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Equal(t, 1, report.LibraryPositions)
	require.Equal(t, 1, report.LibraryStoragePositions)
	require.Contains(t, report.Warnings, `Tape ABC010 Position "missing.txt" was omitted because it is absent from the captured LTFS index`)
	position := new(stagedLibraryPosition)
	require.NoError(t, db.Where("id = ?", 30).First(position).Error)
	require.NotNil(t, position.StorageMetadata)
	require.ErrorIs(t, db.Where("id = ?", 31).First(new(stagedLibraryPosition)).Error, gorm.ErrRecordNotFound)
}

func TestPrepareRejectsBarcodesThatNormalizeAlike(t *testing.T) {
	// Two legacy Tapes differing only by case and spacing cannot share one current identity.
	db := newLegacyTestDB(t)
	require.NoError(t, db.Create([]*legacyLibraryTape{{ID: 10, Barcode: "abc010"}, {ID: 11, Barcode: "ABC010 "}}).Error)
	_, err := prepareLegacy(context.Background(), db, t.TempDir())
	require.ErrorContains(t, err, `legacy Tapes "abc010" and "ABC010 " both normalize to barcode ABC010`)
}

func TestPrepareRejectsLegacyPathsBeyondFilesystemLimits(t *testing.T) {
	// A name or path no filesystem can hold stops the import and names the offending row.
	name := strings.Repeat("n", entity.MaxNameBytes+1)
	path := strings.Repeat(strings.Repeat("p", 200)+"/", 21)[:entity.MaxPathBytes+1]
	for _, test := range []struct {
		name    string
		fixture []any
		message string
	}{
		{name: "File name", fixture: []any{&legacyLibraryFile{ID: 1, Name: name, Mode: 0o644}},
			message: "legacy File 1 name exceeds 255 bytes"},
		{name: "Position path", fixture: []any{
			&legacyLibraryFile{ID: 1, Name: "file", Mode: 0o644},
			&legacyLibraryTape{ID: 1, Barcode: "ABC001"},
			&legacyLibraryPosition{ID: 1, FileID: 1, TapeID: 1, Path: path, Mode: 0o644},
		}, message: "convert legacy Position 1 path failed, path exceeds 4096 bytes"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := newLegacyTestDB(t)
			for _, row := range test.fixture {
				require.NoError(t, db.Create(row).Error)
			}
			_, err := prepareLegacy(context.Background(), db, t.TempDir())
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestPrepareMigratesEmptyJobsAsCompleted(t *testing.T) {
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	require.NoError(t, db.Create([]*legacyJob{
		{
			ID: 1, Status: legacypb.JobStatus_JOB_PENDING,
			State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{}}},
		},
		{
			ID: 2, Status: legacypb.JobStatus_JOB_PENDING,
			State: &legacypb.JobState{State: &legacypb.JobState_Restore{Restore: &legacypb.JobRestoreState{}}},
		},
	}).Error)

	report, err := prepareLegacy(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Equal(t, 2, report.MigratedJobs)
	require.Contains(t, report.Warnings, "job 1 has an empty archive manifest; migrated as completed")
	require.Contains(t, report.Warnings, "job 2 has an empty restore manifest; migrated as completed")

	for id, kind := range map[int64]entity.JobKind{1: entity.JobKind_JOB_KIND_ARCHIVE, 2: entity.JobKind_JOB_KIND_RESTORE} {
		jobDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", fmt.Sprint(id), "state.db"))
		require.NoError(t, err)
		var record executor.JobRecord
		require.NoError(t, jobDB.First(&record, 1).Error)
		require.Equal(t, kind, record.Kind)
		require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, record.Status)
		var count int64
		if kind == entity.JobKind_JOB_KIND_ARCHIVE {
			require.NoError(t, jobDB.Model(&archive.Item{}).Count(&count).Error)
		} else {
			require.NoError(t, jobDB.Model(&restore.Copy{}).Count(&count).Error)
		}
		require.Zero(t, count)
		_ = resource.CloseDB(jobDB)
	}
}

func TestAbortRemovesOnlyPreparedCurrentData(t *testing.T) {
	// Build one legacy Job without any legacy Job directory.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	require.NoError(t, db.Create(&legacyJob{
		ID: 1, Status: legacypb.JobStatus_PROCESSING,
		State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{
			Sources: []*legacypb.SourceState{{
				Source: &legacypb.Source{Base: root, Path: []string{"a.txt"}}, Size: 1,
			}},
		}}},
	}).Error)

	// Prepare writes final-path Bundles without changing the active legacy table.
	report, err := prepareLegacy(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Len(t, report.Warnings, 1)
	var catalog catalogJob
	require.NoError(t, db.First(&catalog, 1).Error)
	require.Positive(t, catalog.CreatedAtNS)
	require.Equal(t, catalog.CreatedAtNS, catalog.UpdatedAtNS)
	require.True(t, db.Migrator().HasTable("jobs"))
	require.True(t, db.Migrator().HasTable("jobs_staging"))
	require.True(t, db.Migrator().HasTable("positions_staging"))
	require.DirExists(t, filepath.Join(root, "jobs"))

	// Abort is retry-safe and removes only data owned by the current staging table.
	require.NoError(t, Abort(db, root))
	require.NoError(t, Abort(db, root))
	require.True(t, db.Migrator().HasTable("jobs"))
	require.False(t, db.Migrator().HasTable("jobs_staging"))
	require.False(t, db.Migrator().HasTable("positions_staging"))
	require.NoDirExists(t, filepath.Join(root, "jobs"))
	require.NoFileExists(t, filepath.Join(root, reportFilename))
}

func TestEmptyLegacyInstanceCreatesFinalJobDirectory(t *testing.T) {
	// Prepare an empty legacy catalog with no legacy Job directory.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	require.ErrorIs(t, Cleanup(ctx, db, root, Backup{}), dataformat.ErrUnsupportedCatalog)

	// Prepare creates the final empty directory while leaving the legacy table active.
	report, err := prepareLegacy(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Zero(t, report.MigratedJobs)
	require.DirExists(t, filepath.Join(root, "jobs"))
	require.True(t, db.Migrator().HasTable("jobs_staging"))

	// Commit switches only the catalog tables and remains retry-safe.
	require.NoError(t, Commit(ctx, db, root))
	require.NoError(t, Commit(ctx, db, root))
	require.True(t, db.Migrator().HasTable("jobs"))
	require.True(t, db.Migrator().HasTable("jobs_legacy"))
	require.DirExists(t, filepath.Join(root, "jobs"))
}

func newLegacyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "main.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&legacyJob{}, &legacyLibraryFile{}, &legacyLibraryTape{}, &legacyLibraryPosition{},
	))
	return db
}

func TestPrepareRefusesAnExistingJobDirectory(t *testing.T) {
	// v0.1.x keeps no Job directory, so one with content is never adopted or moved aside.
	db := newLegacyTestDB(t)
	root := t.TempDir()
	marker := filepath.Join(root, "jobs", "keep")
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0o644))
	_, err := prepareLegacy(context.Background(), db, root)
	require.ErrorContains(t, err, "a Job directory already exists")
	require.FileExists(t, marker)
	require.False(t, db.Migrator().HasTable("jobs_staging"))
}
