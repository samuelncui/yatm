package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func init() {
	// Targeted catalog tests use an in-memory runner contract without touching any Media.
	RegisterJobType(entity.JobKind_JOB_KIND_SCAN, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
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
		job, err := exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_SCAN, int64(index), target, func(*gorm.DB) error { return nil }, nil)
		require.NoError(t, err)
		waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
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
	require.Equal(t, entity.JobKind_JOB_KIND_SCAN, media.Jobs[0].Kind)

	// A new Job is absent from the older snapshot, and filtered incremental polling preserves tombstones.
	newJob, err := exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_SCAN, 0, JobTarget{LocationID: 7, TargetName: "New name"}, func(*gorm.DB) error { return nil }, nil)
	require.NoError(t, err)
	waitJobStatus(t, exe, newJob.ID, entity.JobStatus_JOB_STATUS_READY)
	snapshot, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(7), SnapshotRevision: proto.Int64(page.Revision)})
	require.NoError(t, err)
	require.Len(t, snapshot.Jobs, 2)
	require.Equal(t, wanted[1].ID, snapshot.Jobs[0].ID)
	require.Equal(t, wanted[0].ID, snapshot.Jobs[1].ID)
	_, err = exe.DeleteJobs(ctx, false, wanted[0].ID)
	require.NoError(t, err)
	changes, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(7), ChangedAfterRevision: proto.Int64(page.Revision)})
	require.NoError(t, err)
	require.Len(t, changes.Jobs, 2)
	require.Equal(t, newJob.ID, changes.Jobs[0].ID)
	require.Equal(t, "New name", changes.Jobs[0].TargetName)
	require.Equal(t, wanted[0].ID, changes.Jobs[1].ID)
	require.NotZero(t, changes.Jobs[1].DeletedAtNS)
	require.Equal(t, "Original name", changes.Jobs[1].TargetName)
}

func TestJobTargetRejectsMismatchedKindsAndInvalidFilters(t *testing.T) {
	// A catalog target cannot claim a different execution domain or both source kinds.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	for _, target := range []JobTarget{{LocationID: -1}, {MediaID: -1}, {LocationID: 1, MediaID: 1}} {
		_, err := exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_SCAN, 0, target, nil, nil)
		require.Error(t, err)
	}
	_, err := exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_ARCHIVE, 0, JobTarget{LocationID: 1}, nil, nil)
	require.Error(t, err)
	_, err = exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(0)})
	require.Error(t, err)
	_, err = exe.ListJob(ctx, &entity.JobFilter{MediaId: proto.Int64(-1)})
	require.Error(t, err)
}

func TestJobPropertiesAndFiltersKeepChangeFeedRemovals(t *testing.T) {
	// Primary targets and additional searchable values share one deduplicated index.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job, err := exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_SCAN, 0, JobTarget{LocationID: 7}, func(*gorm.DB) error { return nil }, nil)
	require.NoError(t, err)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	properties := []JobProperty{
		{Key: JobPropertyLocation, Value: 42},
		{Key: JobPropertyLocation, Value: 42},
		{Key: JobPropertyMedia, Value: 9},
	}
	require.NoError(t, exe.AddJobProperties(ctx, job.ID, properties...))
	beforeRetry := exe.currentJobRevision()
	require.NoError(t, exe.AddJobProperties(ctx, job.ID, properties...))
	require.Equal(t, beforeRetry, exe.currentJobRevision(), "a no-op must not issue an unpersisted polling cursor")
	var count int64
	require.NoError(t, exe.db.Model(&JobProperty{}).Count(&count).Error)
	require.EqualValues(t, 3, count)

	// Snapshot filtering runs in SQL before pagination and does not duplicate the matching Job.
	kind := entity.JobKind_JOB_KIND_SCAN
	filter := &entity.JobFilter{LocationId: proto.Int64(42), MediaId: proto.Int64(9), Kind: &kind, Limit: proto.Int64(1)}
	page, err := exe.ListJob(ctx, filter)
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)
	require.False(t, page.HasMore)
	require.Equal(t, job.ID, page.Jobs[0].ID)

	// A Job whose durable state changes stays in the same target and kind filters, and the
	// changefeed still delivers the change so a client can refresh its row.
	require.NoError(t, exe.UpdateJobStatus(ctx, job.ID, entity.JobStatus_JOB_STATUS_READY, entity.JobStatus_JOB_STATUS_COMPLETED))
	filter.ChangedAfterRevision = proto.Int64(page.Revision)
	changed, err := exe.ListJob(ctx, filter)
	require.NoError(t, err)
	require.Len(t, changed.Jobs, 1)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, changed.Jobs[0].Status)
	filter.ChangedAfterRevision = nil
	stillListed, err := exe.ListJob(ctx, filter)
	require.NoError(t, err)
	require.Len(t, stillListed.Jobs, 1)

	// A snapshot filter that the Job's indexed values do not match excludes it.
	other, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(43)})
	require.NoError(t, err)
	require.Empty(t, other.Jobs)

	// Soft deletion remains visible through additional properties as well as the primary target.
	_, err = exe.DeleteJobs(ctx, false, job.ID)
	require.NoError(t, err)
	filter.ChangedAfterRevision = proto.Int64(changed.Revision)
	deleted, err := exe.ListJob(ctx, filter)
	require.NoError(t, err)
	require.Len(t, deleted.Jobs, 1)
	require.Equal(t, job.ID, deleted.Jobs[0].ID)
	require.NotZero(t, deleted.Jobs[0].DeletedAtNS)
}

func TestJobPropertyRetryCursorSurvivesRestart(t *testing.T) {
	// Repeated indexing must leave a durable cursor that remains valid across restart.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job, err := exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_SCAN, 0, JobTarget{LocationID: 7}, func(*gorm.DB) error { return nil }, nil)
	require.NoError(t, err)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	property := JobProperty{Key: JobPropertyLocation, Value: 42}
	require.NoError(t, exe.AddJobProperties(ctx, job.ID, property))
	require.NoError(t, exe.AddJobProperties(ctx, job.ID, property))
	page, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(42)})
	require.NoError(t, err)
	require.Len(t, page.Jobs, 1)

	// A fresh allocator must deliver the first subsequent change after the pre-restart cursor.
	restarted := New(exe.db, exe.lib, nil, exe.paths, exe.scripts, exe.previews)
	require.NoError(t, restarted.AutoMigrate())
	require.Equal(t, page.Revision, restarted.currentJobRevision())
	require.NoError(t, restarted.UpdateJobStatus(ctx, job.ID, entity.JobStatus_JOB_STATUS_READY, entity.JobStatus_JOB_STATUS_COMPLETED))
	changes, err := restarted.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(42), ChangedAfterRevision: proto.Int64(page.Revision)})
	require.NoError(t, err)
	require.Len(t, changes.Jobs, 1)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, changes.Jobs[0].Status)
}

func TestJobPropertiesRejectInvalidBatchWithoutPublishing(t *testing.T) {
	// Invalid search keys and values must not partially publish an otherwise valid batch.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job, err := exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_SCAN, 0, JobTarget{MediaID: 7}, func(*gorm.DB) error { return nil }, nil)
	require.NoError(t, err)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	before := exe.currentJobRevision()
	for _, property := range []JobProperty{
		{Key: "arbitrary", Value: 42},
		{Key: JobPropertyLocation, Value: 0},
		{Key: JobPropertyLocation, Value: -1},
	} {
		require.Error(t, exe.AddJobProperties(ctx, job.ID,
			JobProperty{Key: JobPropertyLocation, Value: 42}, property))
	}
	require.Equal(t, before, exe.currentJobRevision())

	// Only the initial target is searchable after rejected batches.
	page, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: proto.Int64(42)})
	require.NoError(t, err)
	require.Empty(t, page.Jobs)
	media, err := exe.ListJob(ctx, &entity.JobFilter{MediaId: proto.Int64(7)})
	require.NoError(t, err)
	require.Len(t, media.Jobs, 1)
}

func TestFailedJobCreationRemovesProperties(t *testing.T) {
	// A failed bundle initializer must leave neither its catalog row nor a search property.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	_, err := exe.CreateTargetJob(ctx, entity.JobKind_JOB_KIND_SCAN, 0, JobTarget{LocationID: 7}, func(*gorm.DB) error {
		return errors.New("initializer failed")
	}, nil)
	require.ErrorContains(t, err, "initializer failed")
	var count int64
	require.NoError(t, exe.db.Model(&JobProperty{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, exe.db.Unscoped().Model(&jobCatalogRow{}).Count(&count).Error)
	require.Zero(t, count)
}
