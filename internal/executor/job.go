package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/plugin/soft_delete"
)

const (
	localExecutorID    = "local"
	singletonJobID     = 1
	defaultJobPageSize = 20
	maxJobPageSize     = 200
)

// ModelJob is the opaque jobs-catalog model used by schema migration and inspection code.
var ModelJob = &jobCatalogRow{}

var ErrJobNotFound = errors.New("job not found")

// jobCatalogRow is the Executor catalog's query projection. Authoritative lifecycle state remains
// in JobRecord and is copied into a Job snapshot when the catalog row is read.
type jobCatalogRow struct {
	ID         int64  `gorm:"primaryKey;autoIncrement;index:idx_executor_id,priority:2"`
	ExecutorID string `gorm:"type:varchar(128);not null;index:idx_executor_id,priority:1"`

	CreatedAtNS int64                 `gorm:"not null;autoCreateTime:nano" json:"created_at_ns,string"`
	UpdatedAtNS int64                 `gorm:"not null;autoUpdateTime:nano;index:idx_job_updated_at" json:"updated_at_ns,string"`
	DeletedAtNS soft_delete.DeletedAt `gorm:"type:bigint;softDelete:nano,DeletedAtField:UpdatedAtNS,DeletedAtFieldUnit:nano;index:idx_job_deleted_at" json:"deleted_at_ns,string"`
	Revision    int64                 `gorm:"not null;default:0;index:idx_job_revision"`
	TargetName  string
	LocationID  int64          `gorm:"index:idx_jobs_location"`
	MediaID     int64          `gorm:"index:idx_jobs_media"`
	CatalogKind entity.JobKind `gorm:"not null;default:0;index:idx_jobs_kind"`
}

func (jobCatalogRow) TableName() string {
	return "jobs"
}

func (row *jobCatalogRow) snapshot() *Job {
	return &Job{
		ID:          row.ID,
		CreatedAtNS: row.CreatedAtNS,
		UpdatedAtNS: row.UpdatedAtNS,
		DeletedAtNS: int64(row.DeletedAtNS),
		Revision:    row.Revision,
		JobTarget: JobTarget{
			TargetName: row.TargetName,
			LocationID: row.LocationID,
			MediaID:    row.MediaID,
		},
	}
}

// Job is one runtime snapshot assembled from the Executor catalog, its durable JobRecord, and the
// attached runner's live phase. It is not a GORM model.
type Job struct {
	ID          int64
	CreatedAtNS int64 `json:"created_at_ns,string"`
	UpdatedAtNS int64 `json:"updated_at_ns,string"`
	DeletedAtNS int64 `json:"deleted_at_ns,string"`
	Revision    int64
	JobTarget

	Error    string
	Kind     entity.JobKind
	Status   entity.JobStatus
	Priority int64
	Phase    entity.JobPhase
	// Checkpoint retains the last published manifest position for runner reconstruction and result
	// inspection. Admission uses Status; a complete manifest does not make a FAILED Job executable.
	Checkpoint entity.JobStatus
}

// CheckpointState reports the last published manifest position for retained result inspection.
func (job *Job) CheckpointState() entity.JobStatus {
	return manifestPosition(job.Checkpoint)
}

// manifestPosition reports the manifest position one durable state records. Only a state a runner
// publishes while its manifest exists is a position; a failure or a cancellation is not.
func manifestPosition(state entity.JobStatus) entity.JobStatus {
	switch state {
	case entity.JobStatus_JOB_STATUS_PREPARING, entity.JobStatus_JOB_STATUS_READY, entity.JobStatus_JOB_STATUS_COMPLETED:
		return state
	default:
		return entity.JobStatus_JOB_STATUS_UNSPECIFIED
	}
}

// JobTarget is immutable catalog navigation metadata, not execution state.
type JobTarget struct {
	TargetName string
	LocationID int64
	MediaID    int64
}

type JobRecord struct {
	Error    string
	ID       int64            `gorm:"primaryKey;autoIncrement:false;check:id = 1"`
	Kind     entity.JobKind   `gorm:"not null"`
	Status   entity.JobStatus `gorm:"not null"`
	Priority int64            `gorm:"not null"`

	LatestAttemptStartedAtNS  *int64 `json:"latest_attempt_started_at_ns,string,omitempty"`
	LatestAttemptFinishedAtNS *int64 `json:"latest_attempt_finished_at_ns,string,omitempty"`
	// Checkpoint retains the manifest position across a failed or stopped attempt.
	Checkpoint entity.JobStatus
}

type JobPage struct {
	Jobs     []*Job
	Revision int64
	HasMore  bool
}

// ToEntity converts the internal catalog projection to its public Job representation.
func (job *Job) ToEntity() *entity.Job {
	value := &entity.Job{
		Id:          job.ID,
		Kind:        job.Kind,
		Status:      job.Status,
		Priority:    job.Priority,
		CreatedAtNs: job.CreatedAtNS,
		UpdatedAtNs: job.UpdatedAtNS,
		DeletedAtNs: job.DeletedAtNS,
		Phase:       job.Phase,
		Error:       job.Error,
		Revision:    job.Revision,
	}
	if job.TargetName != "" {
		value.TargetName = proto.String(job.TargetName)
	}
	if job.LocationID != 0 {
		value.LocationId = proto.Int64(job.LocationID)
	}
	if job.MediaID != 0 {
		value.MediaId = proto.Int64(job.MediaID)
	}
	return value
}

func (JobRecord) TableName() string {
	return "job"
}

// DeleteJobs removes the selected Job bundles. A dry run validates the same selection and
// reports how many Jobs it would delete without removing a bundle or a catalog row.
func (e *Executor) DeleteJobs(ctx context.Context, dryRun bool, ids ...int64) (int64, error) {
	var deleted int64
	for _, id := range uniqueIDs(ids) {
		if err := e.validateDeletable(ctx, id); err != nil {
			return 0, err
		}
		if !dryRun {
			if err := e.deleteJob(ctx, id); err != nil {
				return 0, err
			}
		}
		deleted++
	}
	return deleted, nil
}

// validateDeletable reports the running or missing conditions deletion rejects.
func (e *Executor) validateDeletable(ctx context.Context, id int64) error {
	if e.IsRunning(id) {
		return fmt.Errorf("delete running job failed, id=%d, %w", id, ErrJobBusy)
	}
	if _, err := e.GetJob(ctx, id); err != nil {
		return err
	}
	return nil
}

func (e *Executor) deleteJob(ctx context.Context, id int64) error {
	unlockJob := e.lockJob(id)
	defer unlockJob()

	if err := e.validateDeletable(ctx, id); err != nil {
		return err
	}

	// Close cached resources before removing the bundle.
	if runner := e.removeRunner(id); runner != nil {
		if err := runner.Close(); err != nil {
			return fmt.Errorf("close job runner failed, id=%d, %w", id, err)
		}
	}
	if err := os.RemoveAll(e.jobWorkPath(id)); err != nil {
		return fmt.Errorf("remove job bundle failed, id=%d, %w", id, err)
	}

	// Publish the tombstone and its polling revision in one catalog transaction.
	if err := e.withNextJobRevision(func(revision int64) (bool, error) {
		err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			updated := tx.Model(&jobCatalogRow{}).Where("id = ?", id).Update("revision", revision)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return ErrJobNotFound
			}
			deleted := tx.Delete(&jobCatalogRow{}, id)
			if deleted.Error != nil {
				return deleted.Error
			}
			if deleted.RowsAffected != 1 {
				return ErrJobNotFound
			}
			return nil
		})
		return err == nil, err
	}); err != nil {
		return fmt.Errorf("delete job catalog failed, id=%d, %w", id, err)
	}
	return nil
}

func (e *Executor) MGetJob(ctx context.Context, ids ...int64) (map[int64]*Job, error) {
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return map[int64]*Job{}, nil
	}

	// Load catalog rows in one query, then hydrate each local Job DB.
	rows := make([]*jobCatalogRow, 0, len(ids))
	if err := e.readDB().WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("query job catalog failed, %w", err)
	}
	result := make(map[int64]*Job, len(rows))
	for _, row := range rows {
		job := row.snapshot()
		if err := e.hydrateJob(ctx, job); err != nil {
			return nil, err
		}
		result[job.ID] = job
	}
	for _, id := range ids {
		if result[id] == nil {
			return nil, fmt.Errorf("get job failed, id=%d, %w", id, ErrJobNotFound)
		}
	}
	return result, nil
}

func (e *Executor) GetJob(ctx context.Context, id int64) (*Job, error) {
	jobs, err := e.MGetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	return jobs[id], nil
}

func (e *Executor) ListJob(ctx context.Context, filter *entity.JobFilter) (*JobPage, error) {
	// Select either incremental polling or one snapshot page.
	if filter == nil {
		filter = &entity.JobFilter{}
	}
	if (filter.LocationId != nil && *filter.LocationId <= 0) || (filter.MediaId != nil && *filter.MediaId <= 0) {
		return nil, fmt.Errorf("Job target IDs must be positive")
	}
	if filter.Kind != nil {
		if _, valid := entity.JobKind_name[int32(*filter.Kind)]; !valid || *filter.Kind == entity.JobKind_JOB_KIND_UNSPECIFIED {
			return nil, fmt.Errorf("invalid Job kind filter")
		}
	}
	if filter.ChangedAfterRevision != nil {
		return e.listChangedJobs(ctx, filter)
	}
	if filter.BeforeId != nil && filter.Offset != nil {
		return nil, fmt.Errorf("Job listing cannot combine before_id and offset")
	}

	// Freeze initial pagination at one valid catalog revision.
	snapshot := e.currentJobRevision()
	if filter.SnapshotRevision != nil {
		snapshot = *filter.SnapshotRevision
		if snapshot < 0 || snapshot > e.currentJobRevision() {
			return nil, fmt.Errorf("Job listing snapshot revision is invalid, revision=%d", snapshot)
		}
	}
	limit, err := jobPageSize(filter.Limit)
	if err != nil {
		return nil, err
	}

	// Read only one catalog page before hydrating the matching Job DBs.
	query := e.readDB().WithContext(ctx).Where("revision > 0 AND revision <= ?", snapshot).Order("id DESC")
	query = filterJobTarget(query, filter)
	if filter.BeforeId != nil {
		if *filter.BeforeId <= 0 {
			return nil, fmt.Errorf("Job listing before_id must be positive")
		}
		query = query.Where("id < ?", *filter.BeforeId)
	}
	if filter.Offset != nil && *filter.Offset > 0 {
		query = query.Offset(int(*filter.Offset))
	}
	var rows []*jobCatalogRow
	if err := query.Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list job catalog failed, %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	jobs := jobSnapshots(rows)
	if err := e.hydrateJobs(ctx, jobs); err != nil {
		return nil, err
	}
	return &JobPage{Jobs: jobs, Revision: snapshot, HasMore: hasMore}, nil
}

func (e *Executor) listChangedJobs(ctx context.Context, filter *entity.JobFilter) (*JobPage, error) {
	// Incremental polling returns tombstones and cannot apply state stored in a removed Job Bundle.
	if filter.Offset != nil || filter.BeforeId != nil || filter.SnapshotRevision != nil {
		return nil, fmt.Errorf("changed Job listing supports only a revision cursor and limit")
	}
	if *filter.ChangedAfterRevision < 0 {
		return nil, fmt.Errorf("changed Job listing requires a non-negative revision")
	}
	limit, err := jobPageSize(filter.Limit)
	if err != nil {
		return nil, err
	}

	// Page through the unique revision order so concurrent same-millisecond changes remain distinct.
	query := e.readDB().WithContext(ctx).Unscoped().
		Where("revision > ?", *filter.ChangedAfterRevision).
		Order("revision ASC, id ASC").Limit(limit + 1)
	query = filterJobTarget(query, filter)
	var rows []*jobCatalogRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list changed Job catalog failed, %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	jobs := jobSnapshots(rows)

	// Deleted rows are complete tombstones; active rows still hydrate from their Job DB.
	for _, job := range jobs {
		if job.DeletedAtNS != 0 {
			continue
		}
		if err := e.hydrateJob(ctx, job); err != nil {
			return nil, err
		}
	}
	revision := *filter.ChangedAfterRevision
	if len(jobs) > 0 {
		revision = jobs[len(jobs)-1].Revision
	}
	return &JobPage{Jobs: jobs, Revision: revision, HasMore: hasMore}, nil
}

func filterJobTarget(query *gorm.DB, filter *entity.JobFilter) *gorm.DB {
	if filter.LocationId != nil {
		query = query.Where("id IN (SELECT job_id FROM job_properties WHERE `key` = ? AND value = ?)", JobPropertyLocation, *filter.LocationId)
	}
	if filter.MediaId != nil {
		query = query.Where("id IN (SELECT job_id FROM job_properties WHERE `key` = ? AND value = ?)", JobPropertyMedia, *filter.MediaId)
	}
	if filter.Kind != nil {
		query = query.Where("catalog_kind = ?", *filter.Kind)
	}
	return query
}

func (e *Executor) hydrateJobs(ctx context.Context, jobs []*Job) error {
	for _, job := range jobs {
		if err := e.hydrateJob(ctx, job); err != nil {
			return err
		}
	}
	return nil
}

func jobSnapshots(rows []*jobCatalogRow) []*Job {
	jobs := make([]*Job, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, row.snapshot())
	}
	return jobs
}

func (e *Executor) hydrateJob(ctx context.Context, job *Job) error {
	db, closeDB, err := e.openStateDB(job.ID)
	if err != nil {
		return err
	}
	defer closeDB()

	record := new(JobRecord)
	if err := db.WithContext(ctx).First(record, singletonJobID).Error; err != nil {
		return fmt.Errorf("read Job DB failed, id=%d, %w", job.ID, err)
	}
	job.Error = record.Error
	job.Kind = record.Kind
	job.Status = record.Status
	job.Priority = record.Priority
	job.Checkpoint = record.Checkpoint
	job.Phase = e.jobPhase(job.ID)
	return nil
}

// jobPhase reports the live stage of the runner that owns this Job. A Job nobody is running has no
// phase: its durable state is the whole answer, and inventing a stage for it would put two
// vocabularies on one fact.
func (e *Executor) jobPhase(jobID int64) entity.JobPhase {
	if !e.IsRunning(jobID) {
		return entity.JobPhase_JOB_PHASE_UNSPECIFIED
	}
	e.runnersLock.Lock()
	runner := e.runners[jobID]
	e.runnersLock.Unlock()
	if runner == nil {
		return entity.JobPhase_JOB_PHASE_UNSPECIFIED
	}
	return runner.Phase()
}

func (e *Executor) EnsureJobWorkPath(_ context.Context, jobID int64) (string, error) {
	workPath := e.jobWorkPath(jobID)
	if err := os.MkdirAll(workPath, 0o755); err != nil {
		return "", fmt.Errorf("create job directory failed, path=%q, %w", workPath, err)
	}
	return workPath, nil
}

func (e *Executor) jobWorkPath(jobID int64) string {
	return filepath.Join(e.paths.Work, "jobs", strconv.FormatInt(jobID, 10))
}

func uniqueIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func jobPageSize(limit *int64) (int, error) {
	if limit == nil {
		return defaultJobPageSize, nil
	}
	if *limit <= 0 || *limit > maxJobPageSize {
		return 0, fmt.Errorf("Job page limit must be between 1 and %d, limit=%d", maxJobPageSize, *limit)
	}
	return int(*limit), nil
}

func (e *Executor) cleanupFailedJob(ctx context.Context, jobID int64) {
	// Remove the unpublished bundle and its catalog search entries together during failed creation.
	ctx = context.WithoutCancel(ctx)
	if runner := e.removeRunner(jobID); runner != nil {
		_ = runner.Close()
	}
	_ = os.RemoveAll(e.jobWorkPath(jobID))
	_ = e.db.WithContext(ctx).Where("job_id = ?", jobID).Delete(&JobProperty{}).Error
	_ = e.db.WithContext(ctx).Unscoped().Delete(&jobCatalogRow{}, jobID).Error
}
