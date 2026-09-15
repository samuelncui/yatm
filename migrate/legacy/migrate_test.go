package legacy

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/executor/archive"
	"github.com/samuelncui/yatm/executor/restore"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/library"
	legacypb "github.com/samuelncui/yatm/migrate/legacy/pb"
	"github.com/samuelncui/yatm/resource"
	"github.com/samuelncui/yatm/tools"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
	legacyMarker := filepath.Join(root, "jobs", "11", "state.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(legacyMarker), 0o755))
	require.NoError(t, os.WriteFile(legacyMarker, []byte("legacy"), 0o644))

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
	require.FileExists(t, filepath.Join(root, legacyJobsDirectory, "11", "state.db"))

	// Verify Archive processing state rolls back to the last submitted item.
	archiveDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	var archiveRecord executor.JobRecord
	require.NoError(t, archiveDB.First(&archiveRecord, 1).Error)
	require.Equal(t, entity.JobStatus_PENDING, archiveRecord.Status)
	var archiveItems []*archive.Item
	require.NoError(t, archiveDB.Order("id").Find(&archiveItems).Error)
	require.Equal(t, entity.CopyStatus_PENDING, archiveItems[0].Status)
	require.Empty(t, archiveItems[0].MediaPath)
	require.Nil(t, archiveItems[0].MediaID)
	require.Equal(t, entity.CopyStatus_PENDING, archiveItems[1].Status)
	require.Contains(t, report.Warnings, "job 1 reset 1 submitted archive items because no Media ID was recoverable from the legacy Job log")
	closeDB(archiveDB)

	// Verify an already-complete Restore preserves its selected candidate.
	restoreDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "2", "state.db"))
	require.NoError(t, err)
	var restoreRecord executor.JobRecord
	require.NoError(t, restoreDB.First(&restoreRecord, 1).Error)
	require.Equal(t, entity.JobStatus_COMPLETED, restoreRecord.Status)
	var restoreCopy restore.Copy
	require.NoError(t, restoreDB.First(&restoreCopy, 1).Error)
	require.Equal(t, entity.CopyStatus_COMPLETED, restoreCopy.Status)
	require.Equal(t, int64(20), restoreCopy.FileID)
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
	require.Equal(t, "restore.txt", restoreCopy.TargetPath)
	require.NoDirExists(t, restoreRoot, "preparation must not create output directories")
	closeDB(restoreDB)

	// Activate the staged data once; a repeated commit must converge on the same layout.
	require.NoError(t, Commit(ctx, db, root))
	require.NoError(t, Commit(ctx, db, root))
	empty, err := dataformat.CheckCatalog(db)
	require.NoError(t, err)
	require.False(t, empty)
	require.NoError(t, dataformat.CheckBundles(db, root))
	require.True(t, db.Migrator().HasTable("jobs"))
	require.True(t, db.Migrator().HasTable("jobs_legacy"))
	require.True(t, db.Migrator().HasTable("positions_legacy"))
	require.DirExists(t, filepath.Join(root, "jobs"))
	require.FileExists(t, filepath.Join(root, legacyJobsDirectory, "11", "state.db"))
	var catalogs []*catalogJob
	require.NoError(t, db.Table("jobs").Order("id").Find(&catalogs).Error)
	require.Equal(t, []int64{1, 2}, []int64{catalogs[0].Revision, catalogs[1].Revision})
	positions, err := library.New(db).ListPositions(ctx, 10, "")
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, "directory/", positions[0].Path)

	// The activated catalog must continue allocating IDs after the migrated range.
	created := &executor.Job{ExecutorID: executorID}
	require.NoError(t, db.Create(created).Error)
	require.Equal(t, int64(3), created.ID)
	require.NoError(t, db.Delete(created).Error)

	// Cleanup removes only the user-confirmed legacy table backup.
	require.NoError(t, Cleanup(ctx, db, root, backup))
	require.NoError(t, Cleanup(ctx, db, root, backup))
	require.False(t, db.Migrator().HasTable("jobs_legacy"))
	require.False(t, db.Migrator().HasTable("files_legacy"))
	require.False(t, db.Migrator().HasTable("tapes_legacy"))
	require.False(t, db.Migrator().HasTable("positions_legacy"))
	require.DirExists(t, filepath.Join(root, "jobs"))
	require.NoDirExists(t, filepath.Join(root, legacyJobsDirectory))
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

	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.Contains(t, report.Warnings, "job 1 reset 2 submitted archive items absent from or ambiguous across logged Media [9 10]")
	copiedLog, err := os.ReadFile(filepath.Join(root, "jobs", "1", "job.log"))
	require.NoError(t, err)
	require.Equal(t, jobLog, string(copiedLog))

	jobDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	defer closeDB(jobDB)
	var items []*archive.Item
	require.NoError(t, jobDB.Order("id").Find(&items).Error)
	require.Len(t, items, 3)
	require.Equal(t, entity.CopyStatus_SUBMITTED, items[0].Status)
	require.NotNil(t, items[0].MediaID)
	require.Equal(t, int64(10), *items[0].MediaID)
	require.Equal(t, "file.txt", items[0].MediaPath)
	require.Equal(t, entity.CopyStatus_PENDING, items[1].Status)
	require.Nil(t, items[1].MediaID)
	require.Empty(t, items[1].MediaPath)
	require.Equal(t, entity.CopyStatus_PENDING, items[2].Status)
	require.Nil(t, items[2].MediaID)
	require.Empty(t, items[2].MediaPath)
	var record executor.JobRecord
	require.NoError(t, jobDB.First(&record, 1).Error)
	require.Equal(t, entity.JobStatus_PENDING, record.Status)
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
	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.Equal(t, physicalCount+1, report.LibraryFiles)
	require.Equal(t, physicalCount, report.LibraryTapes)
	require.Equal(t, physicalCount, report.LibraryPositions)
	require.Equal(t, 1, report.LibraryDirectories)
	require.NoError(t, Commit(ctx, db, root))

	// Verify the last rows crossed the cursor boundary and remain directly listable.
	var fileCount, tapeCount, positionCount int64
	require.NoError(t, db.Model(&library.File{}).Count(&fileCount).Error)
	require.NoError(t, db.Model(library.ModelMedia).Count(&tapeCount).Error)
	require.NoError(t, db.Model(&library.Position{}).Count(&positionCount).Error)
	require.Equal(t, int64(physicalCount+1), fileCount)
	require.Equal(t, int64(physicalCount), tapeCount)
	require.Equal(t, int64(physicalCount+1), positionCount)
	lib := library.New(db)
	rootPositions, err := lib.ListPositions(ctx, 1, "")
	require.NoError(t, err)
	require.Len(t, rootPositions, 1)
	require.Equal(t, int64(physicalCount*(physicalCount+1)/2), rootPositions[0].Size)
	children, err := lib.ListPositions(ctx, 1, "directory/")
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
	report, err := Prepare(ctx, db, root)
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
		{ID: 10, Barcode: "ABC010", WritenBytes: 8},
		{ID: 11, Barcode: "ABC011", WritenBytes: 8},
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
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "ABC010.schema"), []byte(index), 0o644))

	// Keep the File and its valid copy while removing the physically inconsistent Position.
	report, err := Prepare(ctx, db, root)
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
	var tapes []*stagedLibraryMedia
	require.NoError(t, db.Order("id").Find(&tapes).Error)
	require.Zero(t, tapes[0].WrittenBytes)
	require.Equal(t, int64(8), tapes[1].WrittenBytes)
	restoreDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	defer closeDB(restoreDB)
	var copies []*restore.Copy
	require.NoError(t, restoreDB.Find(&copies).Error)
	require.Len(t, copies, 1)
	require.Equal(t, int64(11), copies[0].MediaID)
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
	report, err := Prepare(ctx, db, root)
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
	report, err := Prepare(ctx, db, root)
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

	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Equal(t, 2, report.MigratedJobs)
	require.Contains(t, report.Warnings, "job 1 has an empty archive manifest; migrated as completed")
	require.Contains(t, report.Warnings, "job 2 has an empty restore manifest; migrated as completed")

	for id, kind := range map[int64]entity.JobKind{1: entity.JobKind_ARCHIVE, 2: entity.JobKind_RESTORE} {
		jobDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", fmt.Sprint(id), "state.db"))
		require.NoError(t, err)
		var record executor.JobRecord
		require.NoError(t, jobDB.First(&record, 1).Error)
		require.Equal(t, kind, record.Kind)
		require.Equal(t, entity.JobStatus_COMPLETED, record.Status)
		var count int64
		if kind == entity.JobKind_ARCHIVE {
			require.NoError(t, jobDB.Model(&archive.Item{}).Count(&count).Error)
		} else {
			require.NoError(t, jobDB.Model(&restore.Copy{}).Count(&count).Error)
		}
		require.Zero(t, count)
		closeDB(jobDB)
	}
}

func TestPrepareRecoversTransitionalJobDatabases(t *testing.T) {
	// Build legacy jobs and Library positions referenced by the transitional manifests.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	hashA := sha256.Sum256([]byte("restore-a"))
	hashB := sha256.Sum256([]byte("restore-b"))
	restoreState := func() *legacypb.JobState {
		return &legacypb.JobState{State: &legacypb.JobState_Restore{Restore: &legacypb.JobRestoreState{}}}
	}
	records := []*legacyJob{
		{
			ID: 1, Status: legacypb.JobStatus_PROCESSING, Priority: 1, CreateTime: time.Unix(1, 0),
			State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{}}},
		},
		{ID: 2, Status: legacypb.JobStatus_PROCESSING, Priority: 2, CreateTime: time.Unix(2, 0), State: restoreState()},
		{ID: 3, Status: legacypb.JobStatus_PROCESSING, Priority: 3, CreateTime: time.Unix(3, 0), State: restoreState()},
	}
	require.NoError(t, db.Create(records).Error)
	require.NoError(t, db.Create([]*legacyLibraryFile{
		{ID: 100, Name: "a.txt", Mode: 0o644, Size: 3, Hash: hashA[:]},
		{ID: 101, Name: "b.txt", Mode: 0o644, Size: 4, Hash: hashB[:]},
	}).Error)
	require.NoError(t, db.Create([]*legacyLibraryTape{
		{ID: 7, Barcode: "TAPE007"},
		{ID: 8, Barcode: "TAPE008"},
	}).Error)
	require.NoError(t, db.Create([]*legacyLibraryPosition{
		{ID: 30, FileID: 100, TapeID: 7, Path: "a.txt", Mode: 0o644, Size: 3, Hash: hashA[:]},
		{ID: 31, FileID: 100, TapeID: 8, Path: "copies/a.txt", Mode: 0o644, Size: 3, Hash: hashA[:]},
		{ID: 32, FileID: 101, TapeID: 7, Path: "b.txt", Mode: 0o644, Size: 4, Hash: hashB[:]},
	}).Error)
	indexRoot := filepath.Join(root, legacyLTFSIndexDirectory)
	require.NoError(t, os.MkdirAll(indexRoot, 0o755))
	index := `<ltfsindex><directory><name>TAPE007</name><contents>` +
		`<file><name>a.txt</name><length>3</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>10</startblock><byteoffset>0</byteoffset>` +
		`<bytecount>3</bytecount><fileoffset>0</fileoffset></extent></extentinfo></file>` +
		`<file><name>b.txt</name><length>4</length><extentinfo><extent>` +
		`<partition>b</partition><startblock>20</startblock><byteoffset>0</byteoffset>` +
		`<bytecount>4</bytecount><fileoffset>0</fileoffset></extent></extentinfo></file>` +
		`</contents></directory></ltfsindex>`
	require.NoError(t, os.WriteFile(filepath.Join(indexRoot, "TAPE007.schema"), []byte(index), 0o644))

	// Store the auxiliary Archive manifest used by the empty legacy protobuf shell.
	archiveDB := newTransitionalJobTestDB(t, root, 1)
	require.NoError(t, archiveDB.Table("files").AutoMigrate(&transitionalArchiveItem{}))
	require.NoError(t, archiveDB.Table("files").Create([]*transitionalArchiveItem{
		{ID: 10, Base: root, Path: tools.SortPath("directory/a.txt"), Size: 1, Status: legacypb.CopyStatus_SUBMITED},
		{ID: 11, Base: root, Path: tools.SortPath("directory/b.txt"), Size: 2, Status: legacypb.CopyStatus_RUNNING},
	}).Error)
	closeDB(archiveDB)

	// Cover both transitional Restore table names with the same physical candidates.
	for _, fixture := range []struct {
		jobID int64
		table string
	}{
		{jobID: 2, table: "files"},
		{jobID: 3, table: "restore_files"},
	} {
		restoreDB := newTransitionalJobTestDB(t, root, fixture.jobID)
		require.NoError(t, restoreDB.Table(fixture.table).AutoMigrate(&transitionalRestoreCopy{}))
		require.NoError(t, restoreDB.Table(fixture.table).Create([]*transitionalRestoreCopy{
			{
				ID: 20, FileID: 100, TapeID: 7, Path: tools.SortPath("a.txt"), PositionID: 30,
				TargetPath: "target/a.txt", Size: 3, Hash: hashA[:], Status: legacypb.CopyStatus_PENDING,
			},
			{
				ID: 21, FileID: 100, TapeID: 8, Path: tools.SortPath("copies/a.txt"), PositionID: 31,
				TargetPath: "target/a.txt", Size: 3, Hash: hashA[:], Status: legacypb.CopyStatus_SUBMITED,
			},
			{
				ID: 22, FileID: 101, TapeID: 7, Path: tools.SortPath("b.txt"), PositionID: 32,
				TargetPath: "target/b.txt", Size: 4, Hash: hashB[:], Status: legacypb.CopyStatus_RUNNING,
			},
		}).Error)
		closeDB(restoreDB)
	}

	// Prefer the auxiliary manifests and preserve all physical Restore candidates.
	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Equal(t, 2, report.ArchiveItems)
	require.Equal(t, 4, report.RestoreItems)
	require.Equal(t, 2, report.LibraryStoragePositions)
	require.NotContains(t, report.Warnings, "job 1 has an empty archive manifest; migrated as completed")
	require.NotContains(t, report.Warnings, "job 2 has an empty restore manifest; migrated as completed")

	// Verify Archive item identity and checkpoint status.
	archiveCurrent, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	var archiveItems []*archive.Item
	require.NoError(t, archiveCurrent.Order("id").Find(&archiveItems).Error)
	require.Len(t, archiveItems, 2)
	require.Equal(t, "directory/a.txt", archiveItems[0].TargetPath)
	require.Empty(t, archiveItems[0].MediaPath)
	require.Equal(t, filepath.Join(root, "directory", "a.txt"), archiveItems[0].Data.SourcePath)
	require.Equal(t, entity.CopyStatus_PENDING, archiveItems[0].Status)
	require.Equal(t, "directory/b.txt", archiveItems[1].TargetPath)
	require.Empty(t, archiveItems[1].MediaPath)
	require.Equal(t, entity.CopyStatus_PENDING, archiveItems[1].Status)
	closeDB(archiveCurrent)

	// Verify both Restore schemas inherit indexed order and retain path fallback.
	for _, jobID := range []int64{2, 3} {
		restoreCurrent, err := resource.OpenSQLite(filepath.Join(root, "jobs", fmt.Sprint(jobID), "state.db"))
		require.NoError(t, err)
		var copies []*restore.Copy
		require.NoError(t, restoreCurrent.Order("id").Find(&copies).Error)
		require.Len(t, copies, 3)
		require.Equal(t, entity.CopyStatus_COMPLETED, copies[0].Status)
		require.Equal(t, entity.CopyStatus_COMPLETED, copies[1].Status)
		require.Equal(t, entity.CopyStatus_PENDING, copies[2].Status)
		require.Equal(t, "copies/a.txt", copies[1].MediaPath)
		require.Len(t, copies[0].StorageOrder, 17)
		require.Empty(t, copies[1].StorageOrder)
		require.Len(t, copies[2].StorageOrder, 17)
		closeDB(restoreCurrent)
	}
}

func TestPreparePrefersCanonicalLegacyProtobuf(t *testing.T) {
	// A non-empty origin/main protobuf remains authoritative even if an auxiliary directory exists.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	require.NoError(t, db.Create(&legacyJob{
		ID: 1, Status: legacypb.JobStatus_JOB_PENDING,
		State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{Sources: []*legacypb.SourceState{
			{Source: &legacypb.Source{Base: root, Path: []string{"protobuf.txt"}}, Size: 1},
		}}}},
	}).Error)
	transitionalDB := newTransitionalJobTestDB(t, root, 1)
	require.NoError(t, transitionalDB.Table("files").AutoMigrate(&transitionalArchiveItem{}))
	require.NoError(t, transitionalDB.Table("files").Create(&transitionalArchiveItem{
		ID: 10, Base: root, Path: tools.SortPath("database.txt"), Size: 2,
	}).Error)
	closeDB(transitionalDB)

	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.Equal(t, 1, report.ArchiveItems)
	jobDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	var items []*archive.Item
	require.NoError(t, jobDB.Find(&items).Error)
	require.Len(t, items, 1)
	require.Equal(t, "protobuf.txt", items[0].TargetPath)
	require.Empty(t, items[0].MediaPath)
	closeDB(jobDB)
}

func TestRepairJobRecoversCommittedTransitionalData(t *testing.T) {
	// Commit the same empty Restore Bundle produced by the original migration bug.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	hash := sha256.Sum256([]byte("restore"))
	require.NoError(t, db.Create(&legacyJob{
		ID: 1, Status: legacypb.JobStatus_PROCESSING, Priority: 4, CreateTime: time.Unix(1, 0),
		State: &legacypb.JobState{State: &legacypb.JobState_Restore{Restore: &legacypb.JobRestoreState{}}},
	}).Error)
	frozenRoot := filepath.Join(t.TempDir(), "frozen-output")
	backup := preserveTestBackup(t, db, root)
	backup.RestoreRoot = filepath.Join(t.TempDir(), "new-config-output")
	report, err := PrepareWithLTFSIndex(ctx, db, root, "", frozenRoot)
	require.NoError(t, err)
	require.Zero(t, report.RestoreItems)
	require.NoError(t, Commit(ctx, db, root))

	// Restore the preserved transitional manifest and repair only the affected Bundle.
	transitionalPath := filepath.Join(backup.WorkRoot, "jobs", "1", "state.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(transitionalPath), 0o755))
	transitionalDB, err := resource.OpenSQLite(transitionalPath)
	require.NoError(t, err)
	require.NoError(t, transitionalDB.Table("files").AutoMigrate(&transitionalRestoreCopy{}))
	require.NoError(t, transitionalDB.Table("files").Create([]*transitionalRestoreCopy{
		{ID: 10, FileID: 20, TapeID: 30, Path: tools.SortPath("file.txt"), TargetPath: "file.txt", Size: 7, Hash: hash[:], Status: legacypb.CopyStatus_PENDING},
		{ID: 11, FileID: 20, TapeID: 31, Path: tools.SortPath("copy/file.txt"), TargetPath: "file.txt", Size: 7, Hash: hash[:], Status: legacypb.CopyStatus_PENDING},
	}).Error)
	closeDB(transitionalDB)

	require.NoError(t, db.Migrator().DropTable("jobs_legacy"))
	require.NoError(t, RepairJob(ctx, db, root, 1, backup))
	require.DirExists(t, filepath.Join(filepath.Dir(backup.Root), "jobs-before-repair", "1"))
	repairedDB, err := resource.OpenSQLite(filepath.Join(root, "jobs", "1", "state.db"))
	require.NoError(t, err)
	var copies []*restore.Copy
	require.NoError(t, repairedDB.Order("id").Find(&copies).Error)
	require.Len(t, copies, 2)
	require.Equal(t, entity.CopyStatus_PENDING, copies[0].Status)
	require.Equal(t, int64(30), copies[0].MediaID)
	require.Equal(t, int64(31), copies[1].MediaID)
	var config restore.Config
	require.NoError(t, repairedDB.First(&config, 1).Error)
	canonicalRoot, err := executor.CanonicalConfiguredPath(frozenRoot)
	require.NoError(t, err)
	require.Equal(t, canonicalRoot, config.LegacyRoot)
	require.NoDirExists(t, frozenRoot)
	closeDB(repairedDB)
	require.ErrorContains(t, RepairJob(ctx, db, root, 1, backup), "repair backup already exists")
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
	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.Len(t, report.Warnings, 1)
	var catalog catalogJob
	require.NoError(t, db.First(&catalog, 1).Error)
	require.Positive(t, catalog.CreatedAt)
	require.Equal(t, catalog.CreatedAt, catalog.UpdatedAt)
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
	report, err := Prepare(ctx, db, root)
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

func TestAbortRestoresLegacyJobDirectory(t *testing.T) {
	// Simulate the auxiliary Job directory created by a later legacy deployment.
	db := newLegacyTestDB(t)
	root := t.TempDir()
	marker := filepath.Join(root, "jobs", "keep")
	require.NoError(t, os.MkdirAll(filepath.Dir(marker), 0o755))
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0o644))

	// Prepare preserves legacy files while creating the independent current Job directory.
	report, err := Prepare(context.Background(), db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	require.True(t, db.Migrator().HasTable("jobs_staging"))
	require.NoFileExists(t, marker)
	require.FileExists(t, filepath.Join(root, legacyJobsDirectory, "keep"))

	// Abort is retry-safe and restores the original path and contents.
	require.NoError(t, Abort(db, root))
	require.NoError(t, Abort(db, root))
	require.FileExists(t, marker)
	require.NoDirExists(t, filepath.Join(root, legacyJobsDirectory))
	require.NoFileExists(t, filepath.Join(root, reportFilename))
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

func newTransitionalJobTestDB(t *testing.T, root string, jobID int64) *gorm.DB {
	t.Helper()
	filename := filepath.Join(root, "jobs", fmt.Sprint(jobID), "state.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
	db, err := resource.OpenSQLite(filename)
	require.NoError(t, err)
	return db
}
