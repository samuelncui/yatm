package executor

import (
	"context"
	"os"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestBundleValidationReleasesStateDatabase(t *testing.T) {
	for _, valid := range []bool{false, true} {
		name := "missing manifest"
		if valid {
			name = "complete"
		}
		t.Run(name, func(t *testing.T) {
			// A committed fixture exercises the validation connection, never a running Job.
			exe := setupTestExecutor(t)
			t.Cleanup(func() { require.NoError(t, closeGORMDB(exe.db)) })
			job := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE)
			require.NoError(t, exe.createBundle(context.Background(), job, &JobRecord{
				ID: singletonJobID, Kind: entity.JobKind_JOB_KIND_ARCHIVE, Status: entity.JobStatus_JOB_STATUS_COMPLETED,
			}, func(db *gorm.DB) error {
				if valid {
					return db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY)").Error
				}
				return nil
			}))
			before, err := os.ReadDir("/dev/fd")
			if err != nil {
				t.Skip("descriptor inventory unavailable")
			}

			// Both validation exits must close their SQLite pool and preserve the original bundle.
			for range 8 {
				complete, err := exe.validateBundle(job.ID)
				require.Equal(t, valid, complete)
				if valid {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, "missing manifest table")
				}
			}
			after, err := os.ReadDir("/dev/fd")
			require.NoError(t, err)
			require.LessOrEqual(t, len(after), len(before))
			require.FileExists(t, exe.stateDBPath(job.ID))
		})
	}
}
