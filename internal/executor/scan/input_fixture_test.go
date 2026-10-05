package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type inputFixture struct {
	exe       *executor.Executor
	db        *gorm.DB
	locations []*library.Location
	root      *library.File
}

// newInputFixture uses real files and catalog associations with an interleaved logical selection.
// Tests and paired benchmarks use the same public creation/execution entrypoints.
func newInputFixture(t testing.TB, count, locations int) *inputFixture {
	// Isolate filesystem inputs, catalog and Job resources under the test's owned directory.
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	settings := settingspkg.New(db, settingspkg.PreviewDefinition{})
	lib := library.NewWithSettings(db, settings)
	require.NoError(t, lib.AutoMigrate())
	access := filepath.Join(root, "sources")
	require.NoError(t, os.Mkdir(access, 0755))
	exe := executor.New(db, lib, nil, executor.Paths{Source: access, Work: filepath.Join(root, "work"),
		Access: []executor.AccessRange{{Root: access}}}, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	t.Cleanup(func() {
		for _, id := range exe.RunningJobIDs() {
			_ = exe.Cancel(id)
		}
		require.Eventually(t, func() bool { return len(exe.RunningJobIDs()) == 0 }, 10*time.Second, time.Millisecond)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	})

	// One logical directory spans the selected Locations without physical root nesting.
	ctx := context.Background()
	logical, err := lib.MkdirAll(ctx, 0, "selected", 0755)
	require.NoError(t, err)
	fixture := &inputFixture{exe: exe, db: db, root: logical}
	for i := 0; i < locations; i++ {
		source := &library.Location{Name: fmt.Sprintf("Location-%02d", i), ExecutorID: "local",
			RootPath: filepath.Join(access, fmt.Sprintf("%02d", i))}
		require.NoError(t, os.MkdirAll(filepath.Join(source.RootPath, "selected"), 0755))
		require.NoError(t, lib.CreateLocation(ctx, source))
		fixture.locations = append(fixture.locations, source)
	}

	// Fixture setup is bounded and remains outside every measured execution window.
	for start := 0; start < count; start += batchSize {
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			local := library.New(tx)
			originals := make([]*library.FileLocation, 0, batchSize)
			var keys []*library.FileTrackingKey
			for i := start; i < min(start+batchSize, count); i++ {
				source := fixture.locations[i%locations]
				name := fmt.Sprintf("file-%08d", i)
				filename := filepath.Join(source.RootPath, "selected", name)
				if err := os.WriteFile(filename, []byte("content"), 0644); err != nil {
					return err
				}
				info, err := os.Stat(filename)
				if err != nil {
					return err
				}
				file := &library.File{ParentID: logical.ID, Name: name, Kind: entity.FileKind_FILE_KIND_REGULAR}
				if err := local.SaveFile(ctx, file); err != nil {
					return err
				}
				originals = append(originals, &library.FileLocation{FileID: file.ID, LocationID: source.ID,
					Path: "selected/" + name, Mode: uint32(info.Mode()), Size: info.Size(), MtimeNS: info.ModTime().UnixNano()})
				// The pre-repair reader accepted the filename too; both use the same real stat facts.
				var observed []*library.FileTrackingKey
				switch observe := any(executor.ObserveTracking).(type) {
				case func(*library.Location, os.FileInfo) []*library.FileTrackingKey:
					observed = observe(source, info)
				case func(*library.Location, string, os.FileInfo) ([]*library.FileTrackingKey, error):
					observed, err = observe(source, filename, info)
					if err != nil {
						return err
					}
				default:
					return fmt.Errorf("unsupported tracking reader in performance fixture")
				}
				for _, key := range observed {
					key.FileID, key.LocationID = file.ID, source.ID
					keys = append(keys, key)
				}
			}
			if err := tx.Create(&originals).Error; err != nil {
				return err
			}
			if len(keys) > 0 {
				return tx.Create(&keys).Error
			}
			return nil
		}))
	}
	return fixture
}

func (f *inputFixture) spec() *entity.ScanJobSpec {
	return &entity.ScanJobSpec{Selections: []*entity.FileSelection{{
		Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: f.root.ID}},
		Scope:  entity.FileScope_FILE_SCOPE_ALL}}, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY}
}
