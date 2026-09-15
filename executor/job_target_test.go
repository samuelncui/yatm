package executor

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func init() {
	// Targeted catalog tests use an in-memory runner contract without touching any Media.
	RegisterJobType(entity.JobKind_SCAN, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
		return &testRunner{exe: exe, job: job}, nil
	}, func(grpc.ServiceRegistrar, *Executor) {})
}

func TestJobTargetFiltersBeforePagingAndRetainsFrozenLabels(t *testing.T) {
	// Interleave unrelated scan targets so filtering after page selection would return wrong pages.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	var wanted []*Job
	for index, target := range []JobTarget{
		{LocationID: 7, TargetName: "Original name"}, {LocationID: 8, TargetName: "Other"},
		{LocationID: 7, TargetName: "Original name"}, {MediaID: 7, TargetName: "Archive"},
		{LocationID: 8, TargetName: "Other"},
	} {
		job, err := exe.CreateTargetJob(ctx, entity.JobKind_SCAN, int64(index), target, func(*gorm.DB) error { return nil }, nil)
		require.NoError(t, err)
		waitJobStatus(t, exe, job.ID, entity.JobStatus_PENDING)
		if target.LocationID == 7 {
			wanted = append(wanted, job)
		}
	}

	// Stable filtered pages contain only this Location and retain the Create-time name.
	page, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(7), Limit: proto.Int64(1)})
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	require.True(t, page.HasMore)
	require.Equal(t, wanted[1].ID, page.Jobs[0].ID)
	require.Equal(t, "Original name", page.Jobs[0].ToEntity().GetTargetName())
	last, err := exe.ListJob(ctx, &entity.JobFilter{
		LocationId: proto.Int64(7), Limit: proto.Int64(1), BeforeId: proto.Int64(page.Jobs[0].ID), SnapshotRevision: proto.Int64(page.Revision),
	})
	require.NoError(t, err)
	require.Len(t, last.Jobs, 1)
	require.False(t, last.HasMore)
	require.Equal(t, wanted[0].ID, last.Jobs[0].ID)
	media, err := exe.ListJob(ctx, &entity.JobFilter{MediaId: proto.Int64(7), Limit: proto.Int64(1)})
	require.NoError(t, err)
	require.Len(t, media.Jobs, 1)
	require.Equal(t, entity.JobKind_SCAN, media.Jobs[0].Kind)

	// A new Job is absent from the older snapshot, and filtered incremental polling preserves tombstones.
	newJob, err := exe.CreateTargetJob(ctx, entity.JobKind_SCAN, 0, JobTarget{LocationID: 7, TargetName: "New name"}, func(*gorm.DB) error { return nil }, nil)
	require.NoError(t, err)
	waitJobStatus(t, exe, newJob.ID, entity.JobStatus_PENDING)
	snapshot, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(7), SnapshotRevision: proto.Int64(page.Revision)})
	require.NoError(t, err)
	require.Len(t, snapshot.Jobs, 2)
	require.Equal(t, wanted[1].ID, snapshot.Jobs[0].ID)
	require.Equal(t, wanted[0].ID, snapshot.Jobs[1].ID)
	require.NoError(t, exe.DeleteJobs(ctx, wanted[0].ID))
	changes, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(7), ChangedAfterRevision: proto.Int64(page.Revision)})
	require.NoError(t, err)
	require.Len(t, changes.Jobs, 2)
	require.Equal(t, newJob.ID, changes.Jobs[0].ID)
	require.Equal(t, "New name", changes.Jobs[0].TargetName)
	require.Equal(t, wanted[0].ID, changes.Jobs[1].ID)
	require.NotZero(t, changes.Jobs[1].DeletedAt)
	require.Equal(t, "Original name", changes.Jobs[1].TargetName)
}

func TestJobTargetRejectsMismatchedKindsAndInvalidFilters(t *testing.T) {
	// A catalog target cannot claim a different execution domain or both source kinds.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	for _, target := range []JobTarget{{LocationID: -1}, {MediaID: -1}, {LocationID: 1, MediaID: 1}} {
		_, err := exe.CreateTargetJob(ctx, entity.JobKind_SCAN, 0, target, nil, nil)
		require.Error(t, err)
	}
	_, err := exe.CreateTargetJob(ctx, entity.JobKind_ARCHIVE, 0, JobTarget{LocationID: 1}, nil, nil)
	require.Error(t, err)
	_, err = exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(0)})
	require.Error(t, err)
	_, err = exe.ListJob(ctx, &entity.JobFilter{MediaId: proto.Int64(-1)})
	require.Error(t, err)
}

func TestJobResourcesAndStateFiltersKeepChangeFeedRemovals(t *testing.T) {
	// Multiple source/destination associations belong to the same deduplicated Job.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job, err := exe.CreateTargetJob(ctx, entity.JobKind_SCAN, 0, JobTarget{LocationID: 7}, func(*gorm.DB) error { return nil }, nil)
	require.NoError(t, err)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_PENDING)
	resources := []JobResource{
		{Kind: JobResourceLocation, ResourceID: 42, Role: JobResourceSource},
		{Kind: JobResourceLocation, ResourceID: 42, Role: JobResourceDestination},
		{Kind: JobResourceMedia, ResourceID: 9, Role: JobResourceSource},
	}
	require.NoError(t, exe.AddJobResources(ctx, job.ID, resources...))
	beforeRetry := exe.currentJobRevision()
	require.NoError(t, exe.AddJobResources(ctx, job.ID, resources...))
	require.Equal(t, beforeRetry, exe.currentJobRevision(), "a no-op must not issue an unpersisted polling cursor")
	var count int64
	require.NoError(t, exe.db.Model(&JobResource{}).Count(&count).Error)
	require.EqualValues(t, 3, count)

	// Snapshot filtering runs in SQL before pagination and does not duplicate the matching Job.
	pending, kind := entity.JobStatus_PENDING, entity.JobKind_SCAN
	filter := &entity.JobFilter{LocationId: proto.Int64(42), MediaId: proto.Int64(9), Status: &pending, Kind: &kind, Limit: proto.Int64(1)}
	page, err := exe.ListJob(ctx, filter)
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	require.False(t, page.HasMore)
	require.Equal(t, job.ID, page.Jobs[0].ID)

	// The new state is still delivered so a client can remove a Job leaving its status filter.
	require.NoError(t, exe.UpdateJobStatus(ctx, job.ID, entity.JobStatus_PENDING, entity.JobStatus_COMPLETED))
	filter.ChangedAfterRevision = proto.Int64(page.Revision)
	changed, err := exe.ListJob(ctx, filter)
	require.NoError(t, err)
	require.Len(t, changed.Jobs, 1)
	require.Equal(t, entity.JobStatus_COMPLETED, changed.Jobs[0].Status)
	filter.ChangedAfterRevision = nil
	empty, err := exe.ListJob(ctx, filter)
	require.NoError(t, err)
	require.Empty(t, empty.Jobs)
}

func TestJobResourceRetryCursorSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job, err := exe.CreateTargetJob(ctx, entity.JobKind_SCAN, 0, JobTarget{LocationID: 7}, func(*gorm.DB) error { return nil }, nil)
	require.NoError(t, err)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_PENDING)
	resource := JobResource{Kind: JobResourceLocation, ResourceID: 42, Role: JobResourceSource}
	require.NoError(t, exe.AddJobResources(ctx, job.ID, resource))
	require.NoError(t, exe.AddJobResources(ctx, job.ID, resource))
	page, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(42)})
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)

	// A fresh allocator must deliver the first subsequent change after the pre-restart cursor.
	restarted := New(exe.db, exe.lib, nil, exe.paths, exe.scripts, exe.previews)
	require.NoError(t, restarted.AutoMigrate())
	require.Equal(t, page.Revision, restarted.currentJobRevision())
	require.NoError(t, restarted.UpdateJobStatus(ctx, job.ID, entity.JobStatus_PENDING, entity.JobStatus_COMPLETED))
	changes, err := restarted.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(42), ChangedAfterRevision: proto.Int64(page.Revision)})
	require.NoError(t, err)
	require.Len(t, changes.Jobs, 1)
	require.Equal(t, entity.JobStatus_COMPLETED, changes.Jobs[0].Status)
}
