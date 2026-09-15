package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/tools"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var (
	ErrJobBusy       = errors.New("job is running")
	ErrJobNotRunning = errors.New("job is not running")
)

type attempt struct {
	cancel    context.CancelFunc
	resources []attemptResource
}

type attemptResource struct {
	key           string
	device        string
	releaseDevice bool
}

type jobMutex struct {
	lock sync.Mutex
	refs int
}

// Previewer is the Preview behavior consumed by Job runners and public APIs.
type Previewer interface {
	Supports(sourcePath string) bool
	Generate(ctx context.Context, sourcePath string, sha256 []byte, size, mtimeNS int64, force bool) ([]byte, error)
	Manifest(signature []byte) (*entity.PreviewManifest, error)
	Open(signature []byte, role string) (io.ReadCloser, error)
}

type Executor struct {
	db  *gorm.DB
	lib *library.Library

	devicesLock      sync.Mutex
	devices          []string
	availableDevices mapset.Set[string]
	resourcesLock    sync.Mutex
	leasedResources  mapset.Set[string]

	runnersLock sync.Mutex
	runners     map[int64]Runner

	jobLocksLock sync.Mutex
	jobLocks     map[int64]*jobMutex

	attemptsLock sync.Mutex
	attempts     map[int64]*attempt
	quiescing    bool

	jobRevisionLock sync.Mutex
	jobRevision     int64

	paths                     Paths
	scripts                   Scripts
	previews                  Previewer
	onlineRuntimePaths        []string
	onlineRuntimePathProvider func() []string
	temporaryNamesLock        sync.RWMutex
	temporaryNames            map[string]struct{}
}

type Paths struct {
	Work    string        `yaml:"work"`
	Access  []AccessRange `yaml:"access"`
	Source  string        `yaml:"source,omitempty"`
	Target  string        `yaml:"target,omitempty"`
	Volumes []string      `yaml:"volumes"`
}

type AccessRange struct {
	Root   string `yaml:"root"`
	Ignore string `yaml:"ignore"`
}

type Scripts struct {
	Encrypt  string `yaml:"encrypt"`
	Mkfs     string `yaml:"mkfs"`
	Mount    string `yaml:"mount"`
	Umount   string `yaml:"umount"`
	ReadInfo string `yaml:"read_info"`
}

func New(
	db *gorm.DB, lib *library.Library,
	devices []string, paths Paths, scripts Scripts, previews Previewer,
) *Executor {
	return &Executor{
		db:               db,
		lib:              lib,
		devices:          devices,
		availableDevices: mapset.NewThreadUnsafeSet(devices...),
		leasedResources:  mapset.NewThreadUnsafeSet[string](),
		runners:          make(map[int64]Runner),
		jobLocks:         make(map[int64]*jobMutex),
		attempts:         make(map[int64]*attempt),
		paths:            paths,
		scripts:          scripts,
		previews:         previews,
	}
}

func (e *Executor) AutoMigrate() error {
	// Format validation precedes schema or query-projection changes.
	if err := dataformat.InitializeCatalog(e.db); err != nil {
		return err
	}
	if err := dataformat.CheckBundles(e.db, e.paths.Work); err != nil {
		return err
	}
	if err := e.db.AutoMigrate(ModelJob, &JobResource{}); err != nil {
		return err
	}

	// Continue the published revision sequence; migration already assigns initial revisions.
	var revision int64
	if err := e.db.Unscoped().Model(&Job{}).Select("COALESCE(MAX(revision), 0)").Scan(&revision).Error; err != nil {
		return fmt.Errorf("read current Job revision failed, %w", err)
	}
	e.jobRevision = revision
	return e.refreshJobProjections(context.Background())
}

// CreateJob persists a complete typed Job Bundle before starting asynchronous indexing.
func (e *Executor) CreateJob(
	ctx context.Context,
	kind entity.JobKind,
	priority int64,
	initialize func(*gorm.DB) error,
) (*Job, error) {
	return e.CreatePreparedJob(ctx, kind, priority, initialize, nil)
}

// CreatePreparedJob transfers operation admission to a complete runner before indexing can start.
func (e *Executor) CreatePreparedJob(
	ctx context.Context,
	kind entity.JobKind,
	priority int64,
	initialize func(*gorm.DB) error,
	prepare func(Runner) error,
) (_ *Job, rerr error) {
	return e.createPreparedJob(ctx, kind, priority, JobTarget{}, initialize, prepare)
}

// CreateTargetJob captures the immutable navigation target of a resource-scoped Job.
func (e *Executor) CreateTargetJob(ctx context.Context, kind entity.JobKind, priority int64, target JobTarget, initialize func(*gorm.DB) error, prepare func(Runner) error) (*Job, error) {
	// Scan may select a Location, a Media, or multiple resources through its frozen selections.
	if kind != entity.JobKind_SCAN || target.LocationID < 0 || target.MediaID < 0 || target.LocationID > 0 && target.MediaID > 0 {
		return nil, fmt.Errorf("Job target does not match its kind")
	}
	return e.createPreparedJob(ctx, kind, priority, target, initialize, prepare)
}

func (e *Executor) createPreparedJob(ctx context.Context, kind entity.JobKind, priority int64, target JobTarget, initialize func(*gorm.DB) error, prepare func(Runner) error) (_ *Job, rerr error) {
	// Validate the type before creating any durable data.
	if initialize == nil {
		return nil, fmt.Errorf("create job failed, initializer is nil")
	}
	if _, err := getRunnerFactory(kind); err != nil {
		return nil, err
	}

	// Allocate the catalog ID before creating the bundle that uses it as its path.
	job := &Job{Priority: priority, JobTarget: target, CatalogKind: kind, CatalogStatus: entity.JobStatus_INDEXING}
	job.ExecutorID = localExecutorID
	if err := e.db.WithContext(ctx).Create(job).Error; err != nil {
		return nil, fmt.Errorf("create job catalog failed, %w", err)
	}
	unlockJob := e.lockJob(job.ID)
	defer unlockJob()
	defer func() {
		if rerr == nil {
			return
		}
		e.cleanupFailedJob(ctx, job.ID)
	}()

	// Create the common Job DB before the kind-specific manifest.
	if err := e.createBundle(ctx, job, &JobRecord{
		ID:       singletonJobID,
		Kind:     kind,
		Status:   entity.JobStatus_INDEXING,
		Priority: job.Priority,
	}, initialize); err != nil {
		return nil, err
	}

	// Create the kind-specific schema before returning the RPC response.
	runner, err := e.GetJobRunner(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	created, err := e.GetJob(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if prepare != nil {
		if err := prepare(runner); err != nil {
			return nil, err
		}
	}
	if err := e.startIndex(job.ID, runner); err != nil {
		return nil, err
	}
	created.Phase = entity.JobPhase_JOB_PHASE_INDEXING
	e.TouchJob(job.ID)
	return created, nil
}

func (e *Executor) RetryIndex(ctx context.Context, jobID int64) error {
	unlockJob := e.lockJob(jobID)
	defer unlockJob()

	job, err := e.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status != entity.JobStatus_INDEXING {
		return fmt.Errorf("job cannot index from status %s", job.Status)
	}
	runner, err := e.GetJobRunner(ctx, jobID)
	if err != nil {
		return err
	}
	return e.startIndex(jobID, runner)
}

func (e *Executor) startIndex(jobID int64, runner Runner) error {
	indexCtx, cancel := context.WithCancel(tools.ShutdownContext)
	if !e.beginAttempt(jobID, cancel) {
		cancel()
		return fmt.Errorf("index job failed, id=%d, %w", jobID, ErrJobBusy)
	}

	tools.Working()
	go func() {
		defer tools.Done()
		defer e.endAttempt(jobID)
		defer cancel()

		if err := runner.Index(indexCtx); err != nil {
			runner.Logger().WithContext(indexCtx).WithError(err).Errorf("job indexing failed, id=%d", jobID)
			return
		}
	}()
	return nil
}

// UpdateJobStatus advances the common durable checkpoint for a Job runner.
func (e *Executor) UpdateJobStatus(
	ctx context.Context, jobID int64, from, to entity.JobStatus,
) error {
	db, closeDB, err := e.openStateDB(jobID)
	if err != nil {
		return err
	}
	defer closeDB()

	result := db.WithContext(ctx).Model(&JobRecord{}).
		Where("id = ? AND status = ?", singletonJobID, from).
		Update("status", to)
	if result.Error != nil {
		return fmt.Errorf("update Job status failed, id=%d, %w", jobID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("update Job status failed, id=%d status=%s", jobID, from)
	}
	e.TouchJob(jobID)
	return nil
}

func (e *Executor) GetJobRunner(ctx context.Context, id int64) (Runner, error) {
	e.runnersLock.Lock()
	runner := e.runners[id]
	e.runnersLock.Unlock()
	if runner != nil {
		return runner, nil
	}

	// Build the type-specific Job object without holding the cache lock.
	job, err := e.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	factory, err := getRunnerFactory(job.Kind)
	if err != nil {
		return nil, err
	}
	runner, err = factory(ctx, e, job)
	if err != nil {
		return nil, fmt.Errorf("create job runner failed, id=%d kind=%s, %w", id, job.Kind, err)
	}

	// Publish exactly one cached object even if a caller bypasses the Job lock.
	e.runnersLock.Lock()
	if existing := e.runners[id]; existing != nil {
		e.runnersLock.Unlock()
		_ = runner.Close()
		return existing, nil
	}
	e.runners[id] = runner
	e.runnersLock.Unlock()
	return runner, nil
}

// StartJob starts one asynchronous typed attempt through the common lifecycle.
func (e *Executor) StartJob(
	ctx context.Context,
	jobID int64,
	kind entity.JobKind,
	run func(context.Context, Runner) error,
) error {
	unlockJob := e.lockJob(jobID)
	defer unlockJob()

	// Reject invalid or terminal work before allocating an attempt.
	if run == nil {
		return fmt.Errorf("start job failed, runner function is nil")
	}
	job, err := e.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Status != entity.JobStatus_PENDING {
		return fmt.Errorf("job cannot start from status %s", job.Status)
	}
	if job.Kind != kind {
		return fmt.Errorf("unexpected job kind, id=%d got=%s want=%s", jobID, job.Kind, kind)
	}
	runner, err := e.GetJobRunner(ctx, jobID)
	if err != nil {
		return err
	}

	// Register the attempt once so duplicate Start calls fail deterministically.
	runCtx, cancel := context.WithCancel(tools.ShutdownContext)
	if !e.beginAttempt(jobID, cancel) {
		cancel()
		return fmt.Errorf("start job failed, id=%d, %w", jobID, ErrJobBusy)
	}

	// Run asynchronously while keeping shutdown and cancellation observable.
	tools.Working()
	go func() {
		defer tools.Done()
		defer e.endAttempt(jobID)
		defer cancel()

		if err := run(runCtx, runner); err != nil {
			runner.Logger().WithContext(runCtx).WithError(err).Errorf("job attempt failed, id=%d", jobID)
		}
	}()

	return nil
}

func (e *Executor) Cancel(jobID int64) error {
	e.attemptsLock.Lock()
	attempt := e.attempts[jobID]
	e.attemptsLock.Unlock()

	if attempt == nil {
		return fmt.Errorf("cancel job failed, id=%d, %w", jobID, ErrJobNotRunning)
	}
	attempt.cancel()
	return nil
}

// UseJobRunner executes a typed read while excluding concurrent Job deletion.
func (e *Executor) UseJobRunner(
	ctx context.Context,
	jobID int64,
	kind entity.JobKind,
	use func(Runner) error,
) error {
	unlockJob := e.lockJob(jobID)
	defer unlockJob()

	// Resolve only the expected typed runner while deletion remains excluded.
	if use == nil {
		return fmt.Errorf("use job runner failed, function is nil")
	}
	job, err := e.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Kind != kind {
		return fmt.Errorf("unexpected job kind, id=%d got=%s want=%s", jobID, job.Kind, kind)
	}
	runner, err := e.GetJobRunner(ctx, jobID)
	if err != nil {
		return fmt.Errorf("get job runner failed, id=%d, %w", jobID, err)
	}
	if err := use(runner); err != nil {
		return fmt.Errorf("use job runner failed, id=%d, %w", jobID, err)
	}
	return nil
}

func (e *Executor) lockJob(jobID int64) func() {
	e.jobLocksLock.Lock()
	mutex := e.jobLocks[jobID]
	if mutex == nil {
		mutex = new(jobMutex)
		e.jobLocks[jobID] = mutex
	}
	mutex.refs++
	e.jobLocksLock.Unlock()

	mutex.lock.Lock()
	return func() {
		mutex.lock.Unlock()
		e.jobLocksLock.Lock()
		mutex.refs--
		if mutex.refs == 0 {
			delete(e.jobLocks, jobID)
		}
		e.jobLocksLock.Unlock()
	}
}

func (e *Executor) IsRunning(jobID int64) bool {
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()
	return e.attempts[jobID] != nil
}

// RunningJobIDs returns a stable snapshot of active Job attempts.
func (e *Executor) RunningJobIDs() []int64 {
	e.attemptsLock.Lock()
	ids := e.runningJobIDsLocked()
	e.attemptsLock.Unlock()

	return ids
}

// TryQuiesce rejects new attempts after confirming that current operations have drained.
func (e *Executor) TryQuiesce() ([]int64, bool) {
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()

	ids := e.runningJobIDsLocked()
	if len(ids) > 0 {
		return ids, false
	}
	e.quiescing = true
	return nil, true
}

func (e *Executor) runningJobIDsLocked() []int64 {
	ids := make([]int64, 0, len(e.attempts))
	for id := range e.attempts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (e *Executor) beginAttempt(jobID int64, cancel context.CancelFunc) bool {
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()

	if e.quiescing || e.attempts[jobID] != nil {
		return false
	}
	e.attempts[jobID] = &attempt{cancel: cancel}
	return true
}

func (e *Executor) endAttempt(jobID int64) {
	// Detach the complete attempt before releasing any of its resources.
	e.attemptsLock.Lock()
	current := e.attempts[jobID]
	delete(e.attempts, jobID)
	e.attemptsLock.Unlock()

	if current == nil {
		return
	}

	// Release logical resource keys for future attempts.
	e.resourcesLock.Lock()
	for _, resource := range current.resources {
		e.leasedResources.Remove(resource.key)
	}
	e.resourcesLock.Unlock()

	// Return only devices whose physical cleanup completed successfully.
	e.devicesLock.Lock()
	for _, resource := range current.resources {
		if resource.releaseDevice {
			e.availableDevices.Add(resource.device)
		}
	}
	e.devicesLock.Unlock()
}

func (e *Executor) withNextJobRevision(update func(int64) (bool, error)) error {
	e.jobRevisionLock.Lock()
	defer e.jobRevisionLock.Unlock()

	revision := e.jobRevision + 1
	changed, err := update(revision)
	if err != nil {
		return err
	}
	// Never expose a polling cursor that has no persisted catalog observation.
	if changed {
		e.jobRevision = revision
	}
	return nil
}

func (e *Executor) currentJobRevision() int64 {
	e.jobRevisionLock.Lock()
	defer e.jobRevisionLock.Unlock()
	return e.jobRevision
}

// TouchJob publishes a runtime phase change through the catalog revision.
func (e *Executor) TouchJob(jobID int64) {
	// Reconcile the query projection from the authoritative bundle on every publication.
	err := e.publishJobProjection(context.Background(), jobID)
	if err != nil {
		logrus.WithError(err).Errorf("touch Job catalog failed, id=%d", jobID)
	}
}

func (e *Executor) removeRunner(jobID int64) Runner {
	e.runnersLock.Lock()
	defer e.runnersLock.Unlock()

	runner := e.runners[jobID]
	delete(e.runners, jobID)
	return runner
}

func (e *Executor) Lib() *library.Library {
	return e.lib
}

func (e *Executor) Paths() Paths {
	return e.paths
}

func (e *Executor) Scripts() Scripts {
	return e.scripts
}

func (e *Executor) Previews() Previewer {
	return e.previews
}
