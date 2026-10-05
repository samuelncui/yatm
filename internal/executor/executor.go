package executor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/ignore"
	"github.com/samuelncui/yatm/internal/library"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/samuelncui/yatm/internal/tools"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

var (
	ErrJobBusy       = errors.New("job is running")
	ErrJobNotRunning = errors.New("job is not running")
)

// attemptCancelledByOperator is the reason a cancelled attempt records. The operator acts on Jobs,
// so the internal context error is not what a reader of the Job should see.
const attemptCancelledByOperator = "Cancelled by the operator"

// errCancelledByOperator is the cancellation cause Cancel attaches, so an attempt that returns the
// context error is still reported as the operator's action rather than as an unnamed cancellation.
var errCancelledByOperator = errors.New(attemptCancelledByOperator)

type jobExecutionSettingsKey struct{}

// WithJobExecutionSettings freezes validated pipeline limits into an attempt context.
func WithJobExecutionSettings(ctx context.Context, settings *entity.JobExecutionSettings) context.Context {
	return context.WithValue(
		ctx, jobExecutionSettingsKey{}, proto.Clone(settings).(*entity.JobExecutionSettings),
	)
}

// JobExecutionSettings returns the pipeline limits frozen at this attempt's admission boundary.
func JobExecutionSettings(ctx context.Context) (*entity.JobExecutionSettings, error) {
	settings, ok := ctx.Value(jobExecutionSettingsKey{}).(*entity.JobExecutionSettings)
	if !ok {
		return nil, fmt.Errorf("Job execution Settings are not configured")
	}
	return proto.Clone(settings).(*entity.JobExecutionSettings), nil
}

func (e *Executor) withJobExecutionSettings(ctx context.Context) (context.Context, error) {
	if e.settings == nil {
		return nil, fmt.Errorf("Settings module is not configured")
	}
	settings, err := e.settings.Job.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("read Job execution Settings failed, %w", err)
	}
	return WithJobExecutionSettings(ctx, settings.GetExecution()), nil
}

type attempt struct {
	cancel    context.CancelCauseFunc
	resources []attemptResource
	startedAt time.Time
}

type attemptResource struct {
	key string
	// retain keeps a resource held after the attempt ends, for state that needs investigation.
	retain bool
}

type jobMutex struct {
	lock sync.Mutex
	refs int
}

// Previewer is the Preview behavior consumed by Job runners and public APIs.
type Previewer interface {
	CheckGeneration(context.Context) error
	Capabilities(context.Context) (*entity.GetPreviewCapabilitiesResponse, error)
	Supports(sourcePath string, settings *entity.PreviewJobSettings) bool
	// Exists answers whether one content already has published assets, which is what decides
	// reuse. It reads no manifest: a bundle is renamed into place, so it is complete or absent.
	Exists(signature []byte) (bool, error)
	Generate(ctx context.Context, sourcePath string, sha256 []byte, size, mtimeNS int64, force bool, settings *entity.PreviewJobSettings) ([]byte, error)
	Manifest(signature []byte) (*entity.PreviewManifest, error)
	Open(signature []byte, role string) (io.ReadCloser, error)
}

type Executor struct {
	db       *gorm.DB
	reader   *gorm.DB
	lib      *library.Library
	settings *settingspkg.Module

	devices   []string
	resources *resourceQueue

	runnersLock sync.Mutex
	runners     map[int64]Runner

	jobLocksLock sync.Mutex
	jobLocks     map[int64]*jobMutex

	attemptsLock sync.Mutex
	attempts     map[int64]*attempt
	quiescing    bool

	jobRevisionLock sync.Mutex
	jobRevision     int64

	paths              Paths
	access             []compiledAccessRange
	scripts            Scripts
	previews           Previewer
	temporaryNamesLock sync.RWMutex
	temporaryNames     map[string]struct{}
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

// compiledAccessRange is one administrator boundary with its rules compiled once.
type compiledAccessRange struct {
	root    string
	matcher *ignore.Matcher
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
	// Share the Library-owned Settings module with every attempt admission path.
	var appSettings *settingspkg.Module
	if lib != nil {
		appSettings = lib.Settings()
	}
	value := &Executor{
		db:        db,
		lib:       lib,
		settings:  appSettings,
		devices:   devices,
		resources: newResourceQueue(),
		runners:   make(map[int64]Runner),
		jobLocks:  make(map[int64]*jobMutex),
		attempts:  make(map[int64]*attempt),
		paths:     paths,
		scripts:   scripts,
		previews:  previews,
	}

	// Administrator rules are immutable after construction; compile them once.
	for _, access := range value.AccessRanges() {
		value.access = append(value.access, compiledAccessRange{root: access.Root, matcher: ignore.Compile(access.Ignore)})
	}
	return value
}

// NewWithCatalog routes Catalog reads independently of the sole SQLite writer.
func NewWithCatalog(write, read *gorm.DB, lib *library.Library,
	devices []string, paths Paths, scripts Scripts, previews Previewer,
) *Executor {
	value := New(write, lib, devices, paths, scripts, previews)
	value.reader = read
	return value
}

func (e *Executor) readDB() *gorm.DB {
	if e.reader != nil {
		return e.reader
	}
	return e.db
}

func (e *Executor) AutoMigrate() error {
	// Format validation precedes schema or query-projection changes.
	if err := dataformat.InitializeCatalog(e.db); err != nil {
		return err
	}
	if err := dataformat.CheckBundles(e.db, e.paths.Work); err != nil {
		return err
	}
	if err := e.db.AutoMigrate(ModelJob, &JobProperty{}); err != nil {
		return err
	}

	// Continue the published revision sequence; migration already assigns initial revisions.
	var revision int64
	if err := e.db.Unscoped().Model(&jobCatalogRow{}).Select("COALESCE(MAX(revision), 0)").Scan(&revision).Error; err != nil {
		return fmt.Errorf("read current Job revision failed, %w", err)
	}
	e.jobRevision = revision
	return nil
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
	if kind != entity.JobKind_JOB_KIND_SCAN || target.LocationID < 0 || target.MediaID < 0 || target.LocationID > 0 && target.MediaID > 0 {
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
	row := &jobCatalogRow{
		ExecutorID:  localExecutorID,
		TargetName:  target.TargetName,
		LocationID:  target.LocationID,
		MediaID:     target.MediaID,
		CatalogKind: kind,
	}
	if err := e.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("create job catalog failed, %w", err)
	}
	job := row.snapshot()
	unlockJob := e.lockJob(job.ID)
	defer unlockJob()
	defer func() {
		if rerr == nil {
			return
		}
		e.cleanupFailedJob(ctx, job.ID)
	}()

	// Index the primary target before exposing the completed bundle to catalog searches.
	var properties []JobProperty
	if target.LocationID > 0 {
		properties = append(properties, JobProperty{Key: JobPropertyLocation, Value: target.LocationID})
	}
	if target.MediaID > 0 {
		properties = append(properties, JobProperty{Key: JobPropertyMedia, Value: target.MediaID})
	}
	if err := e.AddJobProperties(ctx, job.ID, properties...); err != nil {
		return nil, err
	}

	// Create the common Job DB before the kind-specific manifest.
	if err := e.createBundle(ctx, job, &JobRecord{
		ID:         singletonJobID,
		Kind:       kind,
		Status:     entity.JobStatus_JOB_STATUS_PREPARING,
		Checkpoint: entity.JobStatus_JOB_STATUS_PREPARING,
		Priority:   priority,
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

func (e *Executor) startIndex(jobID int64, runner Runner) error {
	// Freeze operator tunables before reserving the attempt or starting its durable clock.
	indexCtx, cancel := context.WithCancelCause(tools.ShutdownContext)
	var err error
	indexCtx, err = e.withJobExecutionSettings(indexCtx)
	if err != nil {
		cancel(context.Canceled)
		return fmt.Errorf("index job failed, id=%d, %w", jobID, err)
	}

	// Reserve the attempt before recording its durable timing boundary.
	if err := e.beginAttempt(jobID, cancel); err != nil {
		cancel(context.Canceled)
		return fmt.Errorf("index job failed, id=%d, %w", jobID, err)
	}
	if err := e.startAttemptTiming(indexCtx, jobID); err != nil {
		cancel(context.Canceled)
		e.endAttempt(jobID)
		runner.Logger().WithError(err).Errorf("start Job attempt timing failed, id=%d", jobID)
		return err
	}

	// Include runner cleanup in elapsed time before releasing attempt ownership.
	tools.Working()
	go func() {
		defer tools.Done()
		defer e.endAttempt(jobID)
		defer e.finishAttemptTiming(jobID, runner)
		defer cancel(context.Canceled)
		err := runner.Index(indexCtx)
		e.recordAttemptResult(indexCtx, jobID, runner, err, entity.JobStatus_JOB_STATUS_FAILED)
		if err != nil {
			runner.Logger().WithContext(indexCtx).WithError(err).Errorf("job indexing failed, id=%d", jobID)
		}
	}()
	return nil
}

// UpdateJobStatus records that a runner reached the durable checkpoint `to`, which is also the
// Job's state. `from` guards against a duplicate or concurrent transition. The checkpoint retains
// the manifest position for result inspection; it does not authorize restarting a failed Job.
func (e *Executor) UpdateJobStatus(
	ctx context.Context, jobID int64, from, to entity.JobStatus,
) error {
	db, closeDB, err := e.openStateDB(jobID)
	if err != nil {
		return err
	}
	defer closeDB()

	// A runner advances the manifest checkpoint and durable state; its active attempt supplies the live phase.
	result := db.WithContext(ctx).Model(&JobRecord{}).
		Where("id = ? AND checkpoint = ?", singletonJobID, from).
		Updates(map[string]any{"checkpoint": to, "status": to})
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
	if err := setRunnerActive(runner, false); err != nil {
		return nil, errors.Join(err, runner.Close())
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
	// Serialize admission against typed reads and Job deletion.
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
	if job.Status != entity.JobStatus_JOB_STATUS_READY {
		return fmt.Errorf("job cannot start from status %s", job.Status)
	}
	if job.Kind != kind {
		return fmt.Errorf("unexpected job kind, id=%d got=%s want=%s", jobID, job.Kind, kind)
	}
	runner, err := e.GetJobRunner(ctx, jobID)
	if err != nil {
		return err
	}
	// Freeze operator tunables and register the attempt once so duplicate starts fail deterministically.
	runCtx, cancel := context.WithCancelCause(tools.ShutdownContext)
	runCtx, err = e.withJobExecutionSettings(runCtx)
	if err != nil {
		cancel(context.Canceled)
		return fmt.Errorf("start job failed, %w", err)
	}
	if err := e.beginAttempt(jobID, cancel); err != nil {
		cancel(context.Canceled)
		return fmt.Errorf("start job failed, id=%d, %w", jobID, err)
	}
	if err := e.startAttemptTiming(runCtx, jobID); err != nil {
		cancel(context.Canceled)
		e.endAttempt(jobID)
		runner.Logger().WithError(err).Errorf("start Job attempt timing failed, id=%d", jobID)
		return err
	}

	// Only Archive and Restore return to Media selection when an admitted Media operation fails.
	failureState := entity.JobStatus_JOB_STATUS_FAILED
	if kind == entity.JobKind_JOB_KIND_ARCHIVE || kind == entity.JobKind_JOB_KIND_RESTORE {
		failureState = job.Status
	}

	// Run asynchronously while keeping shutdown and cancellation observable.
	tools.Working()
	go func() {
		defer tools.Done()
		defer e.endAttempt(jobID)
		defer e.finishAttemptTiming(jobID, runner)
		defer cancel(context.Canceled)
		err := run(runCtx, runner)
		e.recordAttemptResult(runCtx, jobID, runner, err, failureState)
		if err != nil {
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
	// The cause is what the Job records as its reason, so the operator's action is named rather
	// than left as the internal context error the runner returns.
	attempt.cancel(errCancelledByOperator)
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

func (e *Executor) beginAttempt(jobID int64, cancel context.CancelCauseFunc) error {
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()

	if e.quiescing || e.attempts[jobID] != nil {
		return ErrJobBusy
	}
	e.runnersLock.Lock()
	runner := e.runners[jobID]
	e.runnersLock.Unlock()
	if err := setRunnerActive(runner, true); err != nil {
		return fmt.Errorf("activate Job resources failed, id=%d, %w", jobID, err)
	}
	e.attempts[jobID] = &attempt{cancel: cancel}
	return nil
}

func (e *Executor) endAttempt(jobID int64) {
	// Detach the complete attempt before releasing any of its resources.
	e.attemptsLock.Lock()
	defer e.attemptsLock.Unlock()
	current := e.attempts[jobID]
	if current == nil {
		return
	}
	e.runnersLock.Lock()
	runner := e.runners[jobID]
	e.runnersLock.Unlock()
	if err := setRunnerActive(runner, false); err != nil {
		logrus.WithError(err).Errorf("release idle Job resources failed, id=%d", jobID)
	}
	delete(e.attempts, jobID)

	// Release every resource the attempt still holds; retained ones stay unavailable.
	for _, resource := range current.resources {
		if !resource.retain {
			e.resources.release(resource.key)
		}
	}

	// Readers and new starts observe settlement only after its final catalog revision exists.
	if !current.startedAt.IsZero() {
		e.TouchJob(jobID)
	}
}

// Runners without file resources need no retention hook, including in-memory test runners.
func setRunnerActive(runner Runner, active bool) error {
	files, ok := runner.(interface{ SetActive(bool) error })
	if !ok {
		return nil
	}
	return files.SetActive(active)
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
