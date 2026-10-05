package scan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestScanLogicalSelectionVisitsLeavesOnceAcrossLocations(t *testing.T) {
	for _, locations := range []int{1, 3, 9} {
		t.Run(fmt.Sprint(locations), func(t *testing.T) {
			// Cross manifest pages with one logical directory spread across different Location counts.
			const count = batchSize*2 + 7
			f := newInputFixture(t, count, locations)
			var visits, originalQueries, originalRows atomic.Int64
			require.NoError(t, f.db.Callback().Query().After("gorm:query").Register("test:selection-visits", func(tx *gorm.DB) {
				if tx.Statement.Table == "file_locations" {
					originalQueries.Add(1)
					originalRows.Add(tx.RowsAffected)
				}
				value := tx.Statement.ReflectValue
				if tx.Statement.Table != "files" || value.Kind() != reflect.Slice {
					return
				}
				// The ordered Library page reads one sentinel without delivering it to the selection.
				end := value.Len()
				if limit, ok := tx.Statement.Clauses["LIMIT"].Expression.(clause.Limit); ok &&
					limit.Limit != nil && end == *limit.Limit {
					end--
				}
				for i := 0; i < end; i++ {
					row := reflect.Indirect(value.Index(i))
					if row.Kind() == reflect.Struct {
						kind := row.FieldByName("Kind")
						if kind.IsValid() && kind.Int() == int64(entity.FileKind_FILE_KIND_REGULAR) {
							visits.Add(1)
						}
					}
				}
			}))

			// Report-only execution includes input preparation and the shared content pipeline.
			r, _ := runAnalysis(t, f.exe, f.spec())
			job, err := f.exe.GetJob(context.Background(), r.job.ID)
			require.NoError(t, err)
			require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
			require.EqualValues(t, count, visits.Load(), "each logical leaf is visited once, regardless of Location count")
			require.EqualValues(t, 6, originalQueries.Load(), "original facts are read once per 100-leaf Library batch")
			require.EqualValues(t, count, originalRows.Load(), "enrichment never rereads each Location's full catalog")
			t.Logf("Locations=%d leaves=%d visits=%d original_queries=%d original_rows=%d", locations, count, visits.Load(), originalQueries.Load(), originalRows.Load())
			var total int64
			require.NoError(t, r.db.Model(&Entry{}).Count(&total).Error)
			require.EqualValues(t, count, total)
		})
	}
}

func TestScanManifestPrecedesContentAndRetainsEarlierPublication(t *testing.T) {
	// Use three selected Locations so a later content failure leaves a completed prefix and untouched suffix.
	f := newInputFixture(t, 3, 3)
	spec := f.spec()
	spec.SignaturePolicy = entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ
	spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS
	ctx := context.Background()
	var r *runner
	var inserted, started atomic.Int64
	var injected atomic.Bool
	job, err := f.exe.CreatePreparedJob(ctx, entity.JobKind_JOB_KIND_SCAN, 1, func(db *gorm.DB) error {
		if err := prepareSchema(db); err != nil {
			return err
		}
		return db.Create(&Config{ID: 1, Spec: spec}).Error
	}, func(value executor.Runner) error {
		r = value.(*runner)
		if err := r.db.Callback().Create().Before("gorm:create").Register("test:manifest-count", func(tx *gorm.DB) {
			var rows []*Entry
			switch value := tx.Statement.Dest.(type) {
			case []*Entry:
				rows = value
			case *[]*Entry:
				rows = *value
			}
			for _, row := range rows {
				if row.ID == 0 {
					inserted.Add(1)
				}
			}
		}); err != nil {
			return err
		}
		return r.db.Callback().Query().After("gorm:query").Register("test:content-barrier", func(tx *gorm.DB) {
			if tx.Statement.Table != "entries" || !strings.Contains(tx.Statement.SQL.String(), "needs_hash =") {
				return
			}
			if _, ok := tx.Statement.Dest.(*[]*Entry); !ok {
				return
			}
			started.Add(1)
			if inserted.Load() != 3 {
				tx.AddError(fmt.Errorf("content began with %d of 3 manifest entries", inserted.Load()))
				return
			}
			if injected.CompareAndSwap(false, true) {
				tx.AddError(os.Remove(filepath.Join(f.locations[1].RootPath, "selected/file-00000001")))
			}
		})
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !f.exe.IsRunning(job.ID) }, 15*time.Second, time.Millisecond)

	// A failed content read is terminal, retaining the full manifest and only earlier publication.
	failed, err := f.exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, failed.Status)
	require.Contains(t, failed.Error, "file-00000001")
	require.Positive(t, started.Load())
	var rows []*Entry
	require.NoError(t, r.db.Order("location_id").Find(&rows).Error)
	require.Len(t, rows, 3)
	require.True(t, rows[0].Published)
	require.Len(t, rows[0].SHA256, 32)
	require.False(t, rows[1].Published)
	require.False(t, rows[2].Published)
	for i, source := range f.locations {
		originals, err := f.exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
		require.NoError(t, err)
		require.Len(t, originals, 1)
		if i == 0 {
			require.Len(t, originals[0].Hash, 32)
		} else {
			require.Empty(t, originals[0].Hash, "later Location observations were not published")
		}
	}
	_, err = (&service{exe: f.exe}).ReadMedia(ctx, &entity.ReadScanMediaRequest{Id: job.ID})
	require.Error(t, err, "a retained complete manifest cannot restart a failed Scan")
}

func TestScanLogicalMissingOriginalFailsBeforeAnyPublication(t *testing.T) {
	// Leave a missing association after a complete input page, with an explicit physical selection too.
	f := newInputFixture(t, batchSize+1, 3)
	ctx := context.Background()
	var missing library.FileLocation
	require.NoError(t, f.db.Order("file_id DESC").First(&missing).Error)
	require.NoError(t, f.db.Delete(&missing).Error)
	spec := f.spec()
	spec.Selections = append(scanLocationSelections(f.locations[0].ID), spec.Selections...)
	spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS
	r, _ := runAnalysis(t, f.exe, spec)

	// Even observations already written to the Job must not admit the missing association or publish content.
	job, err := f.exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, job.Status)
	require.Contains(t, job.Error, "has no original")
	original, err := f.exe.Lib().GetFileLocation(ctx, missing.FileID)
	require.NoError(t, err)
	require.Nil(t, original)
	var published int64
	require.NoError(t, r.db.Model(&Entry{}).Where("published = ?", true).Count(&published).Error)
	require.Zero(t, published)
}

func TestScanPhysicalLogicalOverlapObservesEachEntryOnce(t *testing.T) {
	// The same leaf is selected by whole Location, nested physical roots and duplicate logical roots.
	f := newInputFixture(t, batchSize+1, 1)
	var enrichmentQueries atomic.Int64
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register("test:overlap-cost", func(tx *gorm.DB) {
		if tx.Statement.Table == "file_tracking_keys" {
			enrichmentQueries.Add(1)
		}
	}))
	spec := f.spec()
	spec.Selections = append(scanLocationSelections(f.locations[0].ID, "", "selected", "selected/file-00000000"),
		spec.Selections[0], spec.Selections[0])
	r, _ := runAnalysis(t, f.exe, spec)

	// One Location/path identity carries the observation without a staging table or duplicate content work.
	job, err := f.exe.GetJob(context.Background(), r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	var count int64
	require.NoError(t, r.db.Model(&Entry{}).Count(&count).Error)
	require.EqualValues(t, batchSize+1, count)
	require.EqualValues(t, 2, enrichmentQueries.Load(), "overlapping roots enrich two batches once, before any uniqueness conflict could hide repeated work")
	var tables []string
	require.NoError(t, r.db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name").Scan(&tables).Error)
	require.Equal(t, []string{"config", "entries", "job"}, tables)
}

func TestScanPartialPreparationAndCheckpointUseBoundedPathReads(t *testing.T) {
	// A one-file Scan in a much larger Location must not reconcile or backfill every original.
	f := newInputFixture(t, batchSize*5+3, 1)
	var readRows atomic.Int64
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register("test:partial-cost", func(tx *gorm.DB) {
		if tx.Statement.Table == "file_locations" {
			readRows.Add(tx.RowsAffected)
		}
	}))
	spec := f.spec()
	spec.Selections = scanLocationSelections(f.locations[0].ID, "selected/file-00000000")
	r, _ := runAnalysis(t, f.exe, spec)
	job, err := f.exe.GetJob(context.Background(), r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	require.LessOrEqual(t, readRows.Load(), int64(batchSize+2), "prefix seek may read one boundary page, never the whole Location")
	t.Logf("partial preparation read %d original rows out of %d", readRows.Load(), batchSize*5+3)

	// Publication backfill queries exactly the manifest paths and writes one bounded result batch.
	readRows.Store(0)
	writes := recordStatements(t, r.db)
	local := &locationStage{runner: r, source: f.locations[0]}
	require.NoError(t, local.matchRelocations(context.Background()))
	require.Zero(t, readRows.Load(), "settled exact paths never enumerate unrelated relocation candidates")
	require.NoError(t, local.recordPublished(context.Background(), f.locations[0].ID))
	require.EqualValues(t, 1, readRows.Load())
	require.Equal(t, 1, writes.createCount())
}

func TestScanSavedAllOverlapAndTrashKeepSelectionSemantics(t *testing.T) {
	// SAVED overlaps ALL while logical Trash retains an original that must remain outside Scan.
	f := newInputFixture(t, 6, 3)
	ctx := context.Background()
	var originals []*library.FileLocation
	require.NoError(t, f.db.Order("file_id").Find(&originals).Error)
	for _, old := range originals[:2] {
		require.NoError(t, f.db.Create(&library.FileVersion{FileID: old.FileID, Signature: []byte("saved")}).Error)
	}
	require.NoError(t, f.exe.Lib().SaveFile(ctx, &library.File{ID: library.TrashFileID, Name: "Trash", Kind: entity.FileKind_FILE_KIND_DIRECTORY}))
	trashed, err := f.exe.Lib().GetFile(ctx, originals[5].FileID)
	require.NoError(t, err)
	trashed.ParentID = library.TrashFileID
	require.NoError(t, f.exe.Lib().SaveFile(ctx, trashed))
	saved := f.spec().Selections[0]
	saved.Scope = entity.FileScope_FILE_SCOPE_SAVED
	spec := f.spec()
	spec.Selections[0].GetLibrary().FileId = 0
	spec.Selections = append([]*entity.FileSelection{saved}, spec.Selections...)
	r, _ := runAnalysis(t, f.exe, spec)

	// Later ALL still includes unbacked leaves, deduplicates saved leaves and excludes Trash.
	job, err := f.exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	var rows []*Entry
	require.NoError(t, r.db.Order("path").Find(&rows).Error)
	require.Len(t, rows, 5)
	for i, row := range rows {
		require.Equal(t, originals[i].FileID, row.Before.GetFileId())
	}
	original, err := f.exe.Lib().GetFileLocation(ctx, trashed.ID)
	require.NoError(t, err)
	require.Equal(t, originals[5], original)
}

func TestScanInvalidUTF8StopsAllLocationPreparation(t *testing.T) {
	// The first Location has newer physical facts; a later Location contains an unencodable filename.
	f := newInputFixture(t, 2, 2)
	ctx := context.Background()
	first := f.locations[0]
	before, err := f.exe.Lib().LocationOriginalsPage(ctx, first.ID, "", 10)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(first.RootPath, before[0].Path), []byte("changed content"), 0644))
	invalid := string([]byte{'b', 'a', 'd', '-', 0xff})
	err = os.WriteFile(filepath.Join(f.locations[1].RootPath, "selected", invalid), []byte("invalid name"), 0644)
	if errors.Is(err, syscall.EILSEQ) || errors.Is(err, syscall.EINVAL) {
		t.Skipf("filesystem rejects invalid UTF-8 names before Scan: %v", err)
	}
	require.NoError(t, err)
	spec := f.spec()
	spec.Selections = append(scanLocationSelections(first.ID), scanLocationSelections(f.locations[1].ID)...)
	spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS
	r, _ := runAnalysis(t, f.exe, spec)

	// Failure preserves earlier Library observations and never stores a lossy replacement path.
	job, err := f.exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, job.Status)
	require.Contains(t, job.Error, "UTF-8")
	after, err := f.exe.Lib().LocationOriginalsPage(ctx, first.ID, "", 10)
	require.NoError(t, err)
	require.Equal(t, before, after)
	var rows []*Entry
	require.NoError(t, r.db.Find(&rows).Error)
	for _, row := range rows {
		require.NoError(t, entity.ValidateRelativePath(row.Path))
		require.False(t, row.Published)
	}
}

func TestScanPhysicalRootsCollapseAcrossLexicalNeighbors(t *testing.T) {
	// A sibling that sorts between a root and its descendant must not defeat overlap removal.
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "a/file", "one")
	writeAnalyzeFile(t, source, "a!/file", "two")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(source.ID, "a", "a!", "a/file"),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY})

	// Only the two independent physical roots survive and supply one observation per path.
	job, err := exe.GetJob(context.Background(), r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	require.Len(t, r.scopes, 2)
	var rows []*Entry
	require.NoError(t, r.db.Order("path").Find(&rows).Error)
	require.Len(t, rows, 2)
}

func TestScanIgnoredExplicitPathDoesNotPublishAbsence(t *testing.T) {
	// Ignore excludes an existing original from observation without asserting that it disappeared.
	f := newInputFixture(t, 1, 1)
	ctx := context.Background()
	source := f.locations[0]
	before, err := f.exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	source.Config = &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "/selected/file-00000000"}}
	_, err = f.exe.Lib().UpdateLocation(ctx, source)
	require.NoError(t, err)
	spec := f.spec()
	spec.Selections = scanLocationSelections(source.ID, "selected/file-00000000")
	spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS
	r, _ := runAnalysis(t, f.exe, spec)

	// An empty partial manifest neither removes the original nor advances complete-Location coverage.
	job, err := f.exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	after, err := f.exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Equal(t, before, after)
	current, err := f.exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	require.Zero(t, current.LastSyncJobID)
	var count int64
	require.NoError(t, r.db.Model(&Entry{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestScanMatchingReadsOnlySelectedIdentitiesInGlobalOrder(t *testing.T) {
	// Many unrelated bound originals surround two selected candidates with equal content evidence.
	f := newInputFixture(t, batchSize*3, 1)
	ctx := context.Background()
	var old []*library.FileLocation
	require.NoError(t, f.db.Order("file_id DESC").Limit(2).Find(&old).Error)
	hash := make([]byte, 32)
	hash[0] = 1
	for _, row := range old {
		row.Hash, row.Signature = hash, []byte("opaque")
		require.NoError(t, f.db.Save(row).Error)
	}
	r := newProgressRunner(t)
	r.exe = f.exe
	r.logger = logrus.New()
	source := f.locations[0]
	require.NoError(t, r.db.Create([]*Entry{
		{LocationID: source.ID, Path: old[0].Path, Change: entity.ScanChange_SCAN_CHANGE_REMOVED, Before: old[0].Observation()},
		{LocationID: source.ID, Path: old[1].Path, Change: entity.ScanChange_SCAN_CHANGE_REMOVED, Before: old[1].Observation()},
		{LocationID: source.ID, Path: "new/a", Signature: []byte("opaque"), SHA256: hash, Size: old[0].Size},
		{LocationID: source.ID, Path: "new/b", Signature: []byte("opaque"), SHA256: hash, Size: old[0].Size},
	}).Error)
	var originalRows atomic.Int64
	require.NoError(t, f.db.Callback().Query().After("gorm:query").Register("test:matching-cost", func(tx *gorm.DB) {
		if tx.Statement.Table == "file_locations" {
			originalRows.Add(tx.RowsAffected)
		}
	}))

	// The old identities are intentionally opposite Entry order; each global round still uses File order.
	require.NoError(t, (&locationStage{runner: r, source: source}).matchRelocations(ctx))
	var matched []*Entry
	require.NoError(t, r.db.Where("path IN ?", []string{"new/a", "new/b"}).Order("path").Find(&matched).Error)
	require.Len(t, matched, 2)
	require.Equal(t, old[1].FileID, matched[0].FileID)
	require.Equal(t, old[0].FileID, matched[1].FileID)
	require.EqualValues(t, 6, originalRows.Load(), "three global rounds read only the two selected old identities")
}

func TestScanExactPathsWinBeforeRelocation(t *testing.T) {
	// The earlier File can match both observations by content, but the later File still owns its path.
	f := newInputFixture(t, 2, 1)
	ctx := context.Background()
	source := f.locations[0]
	var old []*library.FileLocation
	require.NoError(t, f.db.Order("file_id").Find(&old).Error)
	hash := sha256.Sum256([]byte("content"))
	signature, err := library.NewFileSignature(hash[:], 7)
	require.NoError(t, err)
	for _, row := range old {
		row.Hash, row.Signature = hash[:], signature
		require.NoError(t, f.db.Save(row).Error)
	}
	movedPath := "selected/zzz-relocated"
	require.NoError(t, os.Rename(filepath.Join(source.RootPath, old[0].Path), filepath.Join(source.RootPath, movedPath)))
	spec := f.spec()
	spec.Selections = scanLocationSelections(source.ID)
	spec.SignaturePolicy = entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ
	spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS
	r, _ := runAnalysis(t, f.exe, spec)

	// Preparation's exact-path assignments agree with the old complete global path round.
	job, err := f.exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	retained, err := f.exe.Lib().GetFileLocationAtPath(ctx, source.ID, old[1].Path)
	require.NoError(t, err)
	require.Equal(t, old[1].FileID, retained.FileID)
	relocated, err := f.exe.Lib().GetFileLocationAtPath(ctx, source.ID, movedPath)
	require.NoError(t, err)
	require.Equal(t, old[0].FileID, relocated.FileID)
	var row Entry
	require.NoError(t, r.db.Where("path = ?", old[1].Path).First(&row).Error)
	require.Equal(t, row.Before.GetFileId(), row.After.GetFileId())
}

func TestScanDetachedMatchingRequiresEarlierObservedAbsence(t *testing.T) {
	for _, observeOld := range []bool{false, true} {
		t.Run(fmt.Sprint(observeOld), func(t *testing.T) {
			// Move one physical original to another Location, retaining its scoped native identity.
			f := newInputFixture(t, 1, 2)
			ctx := context.Background()
			old, err := f.exe.Lib().LocationOriginalsPage(ctx, f.locations[0].ID, "", 10)
			require.NoError(t, err)
			keys, err := f.exe.Lib().ReadFileTracking(ctx, old[0].FileID)
			require.NoError(t, err)
			if len(keys) == 0 {
				t.Skip("filesystem provides no native tracking identity")
			}
			require.NoError(t, os.Rename(filepath.Join(f.locations[0].RootPath, old[0].Path),
				filepath.Join(f.locations[1].RootPath, "selected", "moved")))
			spec := f.spec()
			spec.Selections = scanLocationSelections(f.locations[1].ID)
			if observeOld {
				spec.Selections = append(scanLocationSelections(f.locations[0].ID), spec.Selections...)
			}
			spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS
			r, _ := runAnalysis(t, f.exe, spec)

			// Only earlier successful absence can release the binding for a later Location's native match.
			job, err := f.exe.GetJob(ctx, r.job.ID)
			require.NoError(t, err)
			require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
			moved, err := f.exe.Lib().GetFileLocationAtPath(ctx, f.locations[1].ID, "selected/moved")
			require.NoError(t, err)
			if observeOld {
				require.Equal(t, old[0].FileID, moved.FileID)
				complete, err := f.exe.Lib().GetLocation(ctx, f.locations[0].ID)
				require.NoError(t, err)
				require.Equal(t, job.ID, complete.LastSyncJobID, "an empty observed Location still publishes its complete range")
			} else {
				require.NotEqual(t, old[0].FileID, moved.FileID)
				retained, err := f.exe.Lib().GetFileLocation(ctx, old[0].FileID)
				require.NoError(t, err)
				require.Equal(t, old[0], retained, "an unobserved Location retains its original")
			}
		})
	}
}
