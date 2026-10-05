package apis_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/executor/archive"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func TestJobAPICatalogPollingAndDelete(t *testing.T) {
	// Construct a historical raw-source fixture for the common catalog lifecycle.
	ctx := context.Background()
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	source := filepath.Join(root, "source")
	paths := executor.Paths{Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: root}}}
	exe := executor.New(executorDB, lib, nil, paths, executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, os.MkdirAll(source, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(source, "file.txt"), []byte("fixture"), 0o644))
	api := apis.New(lib, exe)

	// Create a typed Bundle and observe its durable projection through the common Job service.
	job, err := exe.CreateJob(ctx, entity.JobKind_JOB_KIND_ARCHIVE, 7, func(db *gorm.DB) error {
		if err := db.AutoMigrate(&archive.Config{}, &archive.Item{}); err != nil {
			return err
		}
		if err := db.Create(&archive.Item{TargetPath: "file.txt", Size: 7, Data: &entity.ArchiveManifestFile{SourcePath: filepath.Join(source, "file.txt")}}).Error; err != nil {
			return err
		}
		return db.Create(&archive.Config{ID: 1, Spec: &entity.ArchiveJobSpec{}}).Error
	})
	require.NoError(t, err)
	var indexed *entity.Job
	require.Eventually(t, func() bool {
		reply, getErr := api.Get(ctx, &entity.GetJobRequest{Id: job.ID})
		if getErr != nil || reply.Job == nil {
			return false
		}
		indexed = reply.Job
		return indexed.Status == entity.JobStatus_JOB_STATUS_READY && indexed.Phase == entity.JobPhase_JOB_PHASE_UNSPECIFIED
	}, time.Second, time.Millisecond)
	require.Equal(t, entity.JobKind_JOB_KIND_ARCHIVE, indexed.Kind)
	require.Equal(t, int64(7), indexed.Priority)
	require.Positive(t, indexed.CreatedAtNs)
	require.Positive(t, indexed.UpdatedAtNs)

	// Both full and incremental list requests use the same catalog method.
	listed, err := api.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{}})
	require.NoError(t, err)
	require.Len(t, listed.Jobs, 1)
	cursor := int64(0)
	changed, err := api.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{ChangedAfterRevision: &cursor}})
	require.NoError(t, err)
	require.Len(t, changed.Jobs, 1)
	cursor = changed.Revision

	// Deletion removes the Bundle and leaves one incremental-polling tombstone.
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, time.Second, time.Millisecond,
		"catalog waiting phase is published before the indexing attempt releases its lifetime")
	_, err = api.Cancel(ctx, &entity.CancelJobRequest{Id: job.ID})
	require.ErrorIs(t, err, executor.ErrJobNotRunning)
	_, err = api.Delete(ctx, &entity.DeleteJobsRequest{Ids: []int64{job.ID}})
	require.NoError(t, err)
	listed, err = api.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{}})
	require.NoError(t, err)
	require.Empty(t, listed.Jobs)
	changed, err = api.List(ctx, &entity.ListJobsRequest{Filter: &entity.JobFilter{ChangedAfterRevision: &cursor}})
	require.NoError(t, err)
	require.Len(t, changed.Jobs, 1)
	require.Positive(t, changed.Jobs[0].DeletedAtNs)
	_, err = api.Get(ctx, &entity.GetJobRequest{Id: job.ID})
	require.Equal(t, codes.NotFound, status.Code(err), "deleted Jobs need a typed absence signal, not a transport failure")
}
