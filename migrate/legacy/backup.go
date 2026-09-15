package legacy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/internal/dataformat"
	legacypb "github.com/samuelncui/yatm/migrate/legacy/pb"
	"github.com/samuelncui/yatm/resource"
	"gorm.io/gorm"
)

// Backup identifies read-only legacy evidence and the original runtime Restore destination.
type Backup struct {
	Root        string
	CatalogPath string
	WorkRoot    string
	IndexRoot   string
	RestoreRoot string
}

func (backup Backup) open() (*gorm.DB, func(), error) {
	// Work on a disposable database copy so SQLite cannot create or update backup sidecars.
	if backup.CatalogPath == "" || backup.WorkRoot == "" {
		return nil, nil, fmt.Errorf("a complete legacy backup is required")
	}
	db, cleanup, err := openSnapshotCatalog(backup.CatalogPath)
	if err != nil {
		return nil, nil, err
	}
	schema, err := DetectSchema(db)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("inspect backup catalog failed, %w", err)
	}
	if schema != SchemaLegacy {
		cleanup()
		return nil, nil, fmt.Errorf("backup must contain a legacy catalog, found %q", schema)
	}
	return db, cleanup, nil
}

func openSnapshotCatalog(filename string) (*gorm.DB, func(), error) {
	// Copy the database and any retained WAL, never open the preserved source with SQLite.
	dir, err := os.MkdirTemp("", "yatm-migration-check-")
	if err != nil {
		return nil, nil, err
	}
	remove := func() { _ = os.RemoveAll(dir) }
	target := filepath.Join(dir, "state.db")
	for _, suffix := range []string{"", "-wal"} {
		if err := copyEvidence(filename+suffix, target+suffix); err != nil {
			if suffix != "" && errors.Is(err, fs.ErrNotExist) {
				continue
			}
			remove()
			return nil, nil, fmt.Errorf("copy backup catalog failed, %w", err)
		}
	}

	// Opening only the copy also supports legacy transitional Job databases with WAL state.
	db, err := resource.OpenSQLite(target)
	if err != nil {
		remove()
		return nil, nil, err
	}
	return db, func() { closeDB(db); remove() }, nil
}

func copyEvidence(sourcePath, targetPath string) error {
	// Require a regular evidence file; links cannot redirect validation into the active tree.
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("backup evidence is not a regular file: %s", sourcePath)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	// The caller owns the temporary destination and its restrictive permissions.
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, source)
	return errors.Join(err, target.Close())
}

// Validate compares migration results with independently rebuilt backup facts before activation cleanup.
func Validate(ctx context.Context, db *gorm.DB, workRoot string, backup Backup) (_ *Report, rerr error) {
	// Validate the active format before reading any backup or creating temporary output.
	if db == nil {
		return nil, fmt.Errorf("active migration catalog is missing")
	}
	if _, err := dataformat.CheckCatalog(db); err != nil {
		return nil, err
	}
	if err := dataformat.CheckBundles(db, workRoot); err != nil {
		return nil, err
	}
	expected, cleanup, err := backup.open()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// Reuse the migration's content reconciliation against captured backup indexes.
	report := &Report{CreatedAt: time.Now()}
	defer func() {
		if rerr != nil {
			report.Error = rerr.Error()
		}
	}()
	if err := prepareLibrary(ctx, expected, backup.IndexRoot, report); err != nil {
		return report, fmt.Errorf("rebuild expected Library failed, %w", err)
	}
	if err := finishLibraryStaging(ctx, expected); err != nil {
		return report, err
	}
	for _, tables := range [][2]string{
		{"files_staging", "files"}, {"media_staging", "media"},
		{"positions_staging", "positions"}, {"file_versions_staging", "file_versions"},
	} {
		var ignored []string
		if tables[1] == "files" {
			ignored = []string{"created_at", "updated_at"}
		}
		if err := compareTable(ctx, expected, tables[0], db, tables[1], ignored...); err != nil {
			return report, err
		}
	}

	// Compare each complete historical manifest independently, including logs and frozen targets.
	jobsRoot, err := os.MkdirTemp("", "yatm-migration-jobs-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(jobsRoot)
	var after int64
	for {
		var job legacyJob
		result := expected.WithContext(ctx).Where("id > ?", after).Order("id").Limit(1).Find(&job)
		if result.Error != nil {
			return report, result.Error
		}
		if result.RowsAffected == 0 {
			break
		}
		after = job.ID
		if job.Status == legacypb.JobStatus_DELETED {
			report.DeletedJobs = append(report.DeletedJobs, job.ID)
			continue
		}
		counts, _, err := prepareJob(ctx, expected, "positions_staging", restorePositionsRequired,
			jobsRoot, filepath.Join(backup.WorkRoot, "jobs"), backup.WorkRoot, &job, backup.RestoreRoot)
		if err != nil {
			return report, fmt.Errorf("rebuild expected Job %d failed, %w", job.ID, err)
		}
		if err := compareJob(ctx, db, workRoot, jobsRoot, &job); err != nil {
			return report, err
		}
		report.MigratedJobs++
		report.ArchiveItems += counts.archive
		report.RestoreItems += counts.restore
		if err := os.RemoveAll(filepath.Join(jobsRoot, strconv.FormatInt(job.ID, 10))); err != nil {
			return report, err
		}
	}
	report.Success = true
	return report, nil
}

func compareJob(ctx context.Context, db *gorm.DB, workRoot, expectedRoot string, job *legacyJob) error {
	// Bind the historical row to the same catalog identity and authoritative Job status.
	var catalog catalogJob
	if err := db.WithContext(ctx).Table("jobs").First(&catalog, job.ID).Error; err != nil {
		return fmt.Errorf("historical Job %d is missing, %w", job.ID, err)
	}
	id := strconv.FormatInt(job.ID, 10)
	actualDir, expectedDir := filepath.Join(workRoot, "jobs", id), filepath.Join(expectedRoot, id)
	actual, closeActual, err := openSnapshotCatalog(filepath.Join(actualDir, "state.db"))
	if err != nil {
		return err
	}
	defer closeActual()
	expected, closeExpected, err := openSnapshotCatalog(filepath.Join(expectedDir, "state.db"))
	if err != nil {
		return err
	}
	defer closeExpected()
	var record executor.JobRecord
	if err := expected.First(&record, 1).Error; err != nil {
		return err
	}
	if catalog.DeletedAt != 0 || catalog.ExecutorID != executorID ||
		catalog.CatalogKind != record.Kind || catalog.CatalogStatus != record.Status {
		return fmt.Errorf("historical Job %d catalog identity or status differs", job.ID)
	}

	// Field-level table comparison covers every item, candidate, status and frozen output path.
	tables := []string{"job", "config", "items"}
	if record.Kind == entity.JobKind_RESTORE {
		tables[2] = "copies"
	}
	for _, table := range tables {
		if err := compareTable(ctx, expected, table, actual, table); err != nil {
			return fmt.Errorf("historical Job %d: %w", job.ID, err)
		}
	}
	if err := compareEvidence(filepath.Join(actualDir, "job.log"), filepath.Join(expectedDir, "job.log")); err != nil {
		return fmt.Errorf("historical Job %d log differs, %w", job.ID, err)
	}
	return nil
}

func compareTable(ctx context.Context, expected *gorm.DB, source string, actual *gorm.DB, target string, ignored ...string) error {
	// Select the expected columns explicitly so runtime-added defaults are not migration evidence.
	columns, err := expected.Migrator().ColumnTypes(source)
	if err != nil {
		return err
	}
	var names []string
	for _, column := range columns {
		skip := false
		for _, name := range ignored {
			skip = skip || column.Name() == name
		}
		if !skip {
			names = append(names, column.Name())
		}
	}
	left, err := expected.WithContext(ctx).Table(source).Select(names).Order("id").Rows()
	if err != nil {
		return err
	}
	defer left.Close()
	right, err := actual.WithContext(ctx).Table(target).Select(names).Order("id").Rows()
	if err != nil {
		return err
	}
	defer right.Close()

	// Stream both sides in primary-key order; equal counts alone cannot validate migrated content.
	for row := 1; ; row++ {
		foundLeft, foundRight := left.Next(), right.Next()
		if foundLeft != foundRight {
			return fmt.Errorf("migration table %s has different rows at item %d", target, row)
		}
		if !foundLeft {
			return errors.Join(left.Err(), right.Err())
		}
		valuesLeft, valuesRight := make([]any, len(names)), make([]any, len(names))
		ptrLeft, ptrRight := make([]any, len(names)), make([]any, len(names))
		for i := range names {
			ptrLeft[i], ptrRight[i] = &valuesLeft[i], &valuesRight[i]
		}
		if err := errors.Join(left.Scan(ptrLeft...), right.Scan(ptrRight...)); err != nil {
			return err
		}
		for i, name := range names {
			if !reflect.DeepEqual(valuesLeft[i], valuesRight[i]) {
				return fmt.Errorf("migration table %s item %d field %s differs", target, row, name)
			}
		}
	}
}

func compareEvidence(actual, expected string) error {
	// Missing logs are equivalent only when neither source recorded one.
	a, errA := os.Open(actual)
	b, errB := os.Open(expected)
	if a != nil {
		defer a.Close()
	}
	if b != nil {
		defer b.Close()
	}
	if errors.Is(errA, fs.ErrNotExist) && errors.Is(errB, fs.ErrNotExist) {
		return nil
	}
	if err := errors.Join(errA, errB); err != nil {
		return err
	}

	// Bound memory even for large archived logs and old state databases.
	left, right := make([]byte, 64*1024), make([]byte, 64*1024)
	for {
		an, ae := io.ReadFull(a, left)
		bn, be := io.ReadFull(b, right)
		if an != bn || !bytes.Equal(left[:an], right[:bn]) {
			return fmt.Errorf("preserved file differs: %s", actual)
		}
		if ae == io.EOF || ae == io.ErrUnexpectedEOF {
			if be == ae {
				return nil
			}
		}
		if err := errors.Join(ae, be); err != nil {
			return err
		}
	}
}

func removePreservedTree(active, preserved string) error {
	// Validate every entry before removal so newly added or changed user files are retained.
	if !exists(active) {
		return nil
	}
	if err := filepath.WalkDir(active, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(active, path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(filepath.Join(preserved, rel))
		if err != nil {
			return fmt.Errorf("cleanup entry has no backup: %s, %w", path, err)
		}
		if entry.Type() != info.Mode().Type() || entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("cleanup entry type differs or is a link: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		return compareEvidence(path, filepath.Join(preserved, rel))
	}); err != nil {
		return err
	}

	// The service is stopped; only the validated obsolete subtree is removed.
	return os.RemoveAll(active)
}
