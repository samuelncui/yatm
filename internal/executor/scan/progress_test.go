package scan

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// openTestContentStream opens a stream with the validated defaults an attempt would freeze.
func openTestContentStream(
	t *testing.T, ctx context.Context, r *runner, config *Config, scope *Scope, session mediapkg.ReadSession,
	values ...*entity.JobExecutionSettings,
) (*contentStream, error) {
	t.Helper()
	settings := settingspkg.DefaultJobExecution()
	if len(values) > 0 {
		settings = values[0]
	}
	return newContentStream(ctx, r, config, scope, session, settings)
}

func testAttemptContext(ctx context.Context) context.Context {
	return executor.WithJobExecutionSettings(ctx, settingspkg.DefaultJobExecution())
}

// frozenLimits is retained as a fixture helper while execution limits move out of Scan bundles.
func frozenLimits(config *Config) *Config {
	return config
}

func newProgressRunner(t *testing.T) *runner {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "scan.db"))
	require.NoError(t, err)
	require.NoError(t, prepareSchema(db))
	return &runner{db: db, progress: executor.NewProgress()}
}

// startContentAttempt publishes the attempt state a running content stage reads: the durable
// denominator and completed base plus the sampling window the work path opens.
func startContentAttempt(t *testing.T, r *runner, scope *Scope) *contentStream {
	t.Helper()
	totals, err := r.readContentTotals(context.Background(), scope)
	require.NoError(t, err)
	stream, err := newContentStream(
		context.Background(), r, frozenLimits(&Config{Spec: &entity.ScanJobSpec{}}), scope, nil,
		settingspkg.DefaultJobExecution(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = stream.writer.Close() })
	stream.totals = totals
	r.publishContentStage(stream)
	r.sampleContentStage()
	return stream
}

func TestScanUnfrozenProgressReportsObservedRows(t *testing.T) {
	for _, tc := range []struct {
		phase      entity.JobPhase
		completed  int64
		bytes      int64
		totalFiles int64
		totalBytes int64
	}{
		{entity.JobPhase_JOB_PHASE_INDEXING, 2, 1056, 0, 0},
		{entity.JobPhase_JOB_PHASE_UNSPECIFIED, 1, 1024, 2, 1056},
	} {
		t.Run(tc.phase.String(), func(t *testing.T) {
			// Discovery counts grow during indexing; after failure the manifest remains provisional.
			r := newProgressRunner(t)
			r.phase = tc.phase
			require.NoError(t, r.db.Create(&[]*Entry{
				{LocationID: 1, Path: "one", Size: 1024},
				{LocationID: 2, Path: "two", Size: 32, NeedsHash: true},
			}).Error)

			progress, err := r.progressSnapshot(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.completed, progress.CopiedFileCount)
			require.Equal(t, tc.bytes, progress.CopiedBytes)
			require.False(t, progress.TotalKnown)
			require.Equal(t, tc.totalFiles, progress.TotalFileCount)
			require.Equal(t, tc.totalBytes, progress.TotalBytes)
			require.Nil(t, progress.Stage.Total, "discovery never carries a denominator")
			require.Equal(t, entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE, progress.Stage.EstimateState)
			require.Equal(t, tc.completed, progress.Stage.Completed)
		})
	}
}

func TestScanManifestProgressSurvivesRunnerReconstruction(t *testing.T) {
	r := newProgressRunner(t)
	require.NoError(t, r.db.Create(&[]*Entry{
		{Path: "known", Change: entity.ScanChange_SCAN_CHANGE_ADDED, Size: 10},
		{Path: "pending", Change: entity.ScanChange_SCAN_CHANGE_ADDED, Size: 20, NeedsHash: true},
		{Path: "removed", Change: entity.ScanChange_SCAN_CHANGE_REMOVED, Size: 40, NeedsHash: true},
	}).Error)
	r.phase = entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT
	startContentAttempt(t, r, &Scope{})
	r.inflightBytes.Store(5)

	// The attempt's snapshot supplies the durable denominator and base; the moving byte is in flight.
	progress, err := r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.True(t, progress.TotalKnown)
	require.EqualValues(t, 2, progress.TotalFileCount)
	require.EqualValues(t, 30, progress.TotalBytes)
	require.EqualValues(t, 1, progress.CopiedFileCount)
	require.EqualValues(t, 15, progress.CopiedBytes)

	// A reopened runner has no transient bytes and reads the same completed base from the manifest.
	reopened := &runner{db: r.db, phase: entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT, progress: executor.NewProgress()}
	startContentAttempt(t, reopened, &Scope{})
	progress, err = reopened.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, progress.CopiedFileCount)
	require.EqualValues(t, 10, progress.CopiedBytes)
}

// countingLogger counts the statements and the aggregate queries one runner database issues.
type countingLogger struct {
	logger.Interface
	lock      sync.Mutex
	total     int
	aggregate int
}

func (l *countingLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	l.lock.Lock()
	l.total++
	if strings.Contains(sql, "COUNT(") || strings.Contains(sql, "SUM(") {
		l.aggregate++
	}
	l.lock.Unlock()
	l.Interface.Trace(ctx, begin, fc, err)
}

func (l *countingLogger) counts() (int, int) {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.total, l.aggregate
}

func TestScanProgressRequestIssuesNoAggregateQuery(t *testing.T) {
	// Sampling runs on the ACP work path: two work events advance the stage estimate, and one
	// progress request then reads the snapshot without issuing any statement against the manifest.
	r := newProgressRunner(t)
	require.NoError(t, r.db.Create(&[]*Entry{
		{Path: "settled", Change: entity.ScanChange_SCAN_CHANGE_ADDED, Size: 400},
		{Path: "pending", Change: entity.ScanChange_SCAN_CHANGE_ADDED, Size: 600, NeedsHash: true},
	}).Error)
	r.phase = entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT
	counting := &countingLogger{Interface: r.db.Logger}
	r.db.Logger = counting

	// Opening the attempt reads the durable denominator and completed base once, and never again.
	stream := startContentAttempt(t, r, &Scope{})
	baseline, baselineAggregates := counting.counts()
	require.Equal(t, 4, baseline, "the attempt reads the durable totals once")
	require.Equal(t, baseline, baselineAggregates, "the attempt's statements are the durable totals")

	// ACP reports one sample every second; the events alone must carry the estimate forward.
	handler := stream.eventHandler()
	handler(&acp.EventUpdateProgress{Bytes: 100, Files: 1})
	time.Sleep(5*time.Second + 100*time.Millisecond)
	handler(&acp.EventUpdateProgress{Bytes: 500, Files: 1})

	progress, err := r.progressSnapshot(context.Background())
	require.NoError(t, err)
	total, aggregate := counting.counts()
	t.Logf("attempt: statements=%d aggregates=%d; request: +%d statements +%d aggregates",
		baseline, baselineAggregates, total-baseline, aggregate-baselineAggregates)
	require.Equal(t, baselineAggregates, aggregate, "a progress request must not aggregate the manifest")
	require.Equal(t, baseline, total, "a progress request must not touch the database at all")
	require.True(t, progress.TotalKnown)
	require.EqualValues(t, 1000, progress.TotalBytes)
	require.EqualValues(t, 900, progress.CopiedBytes)
	require.EqualValues(t, 900, progress.Stage.Completed)
	require.EqualValues(t, 1000, *progress.Stage.Total)
	require.NotEmpty(t, progress.Stage.WindowId, "the work path opened the window before the first request")
	require.NotNil(t, progress.Stage.RatePerSecond, "the work events produced the rate sample")
	require.Positive(t, *progress.Stage.RatePerSecond)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, progress.Stage.EstimateState)
}

func TestScanContentProgressReportsTheRunningAttempt(t *testing.T) {
	// Every request that observes a content phase reads the running attempt's durable snapshot:
	// cumulative totals, the byte unit and its scope denominator. The attempt is published before
	// the phase becomes observable, so a request never sees an empty stage.
	exe, source := setupAnalyze(t)
	for index := 0; index < 20; index++ {
		writeAnalyzeFile(t, source, fmt.Sprintf("file-%d", index), strings.Repeat("x", 4096))
	}
	ctx := context.Background()
	created, err := Create(ctx, exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY, Selections: scanLocationSelections(source.ID),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ}})
	require.NoError(t, err)
	value, err := exe.GetJobRunner(ctx, created.Job.Id)
	require.NoError(t, err)

	// Slow every manifest query so the short content phase stays observable from this goroutine.
	require.NoError(t, value.(*runner).db.Callback().Query().Before("gorm:query").Register("test:slow-entries", func(tx *gorm.DB) {
		if tx.Statement.Table == "entries" {
			time.Sleep(20 * time.Millisecond)
		}
	}))

	observed, sampled := 0, 0
	for exe.IsRunning(created.Job.Id) {
		result, err := (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: created.Job.Id})
		require.NoError(t, err)
		stage := result.Progress.Stage
		if stage == nil || (stage.Phase != entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT && stage.Phase != entity.JobPhase_JOB_PHASE_VERIFYING_MEDIA) {
			time.Sleep(time.Millisecond)
			continue
		}
		observed++
		require.True(t, result.Progress.TotalKnown, "the attempt published its durable totals")
		require.EqualValues(t, 20, result.Progress.TotalFileCount)
		require.EqualValues(t, 81920, stage.GetTotal())
		require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_BYTES, stage.Unit)
		if stage.WindowId != "" {
			sampled++
		}
		time.Sleep(time.Millisecond)
	}
	require.Positive(t, observed, "the content phase must be observable")
	require.Positive(t, sampled, "the work path opens the stage window")
}

func TestScanComparisonProgressIsDurableAndPreviewUsesTheAttempt(t *testing.T) {
	r := newProgressRunner(t)
	require.NoError(t, r.db.Create(&[]*Entry{
		{Path: "done", Change: entity.ScanChange_SCAN_CHANGE_ADDED, Size: 10, Compared: true},
		{Path: "pending", Change: entity.ScanChange_SCAN_CHANGE_UNCHANGED, Size: 20},
	}).Error)

	r.phase = entity.JobPhase_JOB_PHASE_COMPARING_CONTENT
	progress, err := r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.True(t, progress.TotalKnown)
	require.EqualValues(t, 1, progress.CopiedFileCount)
	require.EqualValues(t, 10, progress.CopiedBytes)

	// Preview work is not durable, so the attempt's own completions are what the stage reports.
	r.phase = entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS
	progress, err = r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.Zero(t, progress.CopiedFileCount)
	r.previewOutcomes = previewOutcomes{ready: 2, skipped: 1}
	progress, err = r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 3, progress.CopiedFileCount)
	require.NotNil(t, progress.Stage.Total, "the manifest still supplies the denominator")
}

func TestScanIndeterminateFinalStagesRetainDiscoveredWork(t *testing.T) {
	r := newProgressRunner(t)
	require.NoError(t, r.db.Create(&[]*Entry{
		{Path: "one", Change: entity.ScanChange_SCAN_CHANGE_ADDED, Size: 10},
		{Path: "two", Change: entity.ScanChange_SCAN_CHANGE_UNCHANGED, Size: 20},
	}).Error)
	for _, phase := range []entity.JobPhase{
		entity.JobPhase_JOB_PHASE_VALIDATING_SOURCE,
		entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA,
		entity.JobPhase_JOB_PHASE_PUBLISHING_SOURCE,
	} {
		r.phase = phase
		progress, err := r.progressSnapshot(context.Background())
		require.NoError(t, err)
		require.EqualValues(t, 2, progress.CopiedFileCount)
		require.EqualValues(t, 30, progress.CopiedBytes)
		require.Nil(t, progress.Stage.Total, "phase %s never carries a denominator", phase)
		require.Nil(t, progress.Stage.RemainingSeconds)
	}
}

func TestVerifyCopiesWaitingForMediaReportsNoPercentage(t *testing.T) {
	// A verification Job freezes its baseline before it waits for a drive. No work in that phase can
	// advance the frozen count, so the stage must not offer a denominator a card would render as 0%.
	r := newProgressRunner(t)
	r.phase = entity.JobPhase_JOB_PHASE_QUEUED
	require.NoError(t, r.db.Create(&Config{ID: 1, Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}}).Error)
	require.NoError(t, r.db.Create(&[]*Entry{
		{Path: "one", Change: entity.ScanChange_SCAN_CHANGE_UNCHANGED, Size: 10},
		{Path: "two", Change: entity.ScanChange_SCAN_CHANGE_UNCHANGED, Size: 20},
	}).Error)

	progress, err := r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.Nil(t, progress.Stage.Total, "a waiting phase never carries a denominator")
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE, progress.Stage.EstimateState)
	require.EqualValues(t, 2, progress.CopiedFileCount, "the frozen found count stays visible")
	require.EqualValues(t, 2, progress.Stage.Completed)

	// The claim is about the stage rule, not about the business total, so the rule refuses here too.
	require.False(t, executor.RuleFor(entity.JobPhase_JOB_PHASE_QUEUED, &executor.Counts{Files: 2, Bytes: 30}).Ratio)
}

func TestZeroByteContentScopeReportsItemsInsteadOfAFalsePercentage(t *testing.T) {
	// A scope of empty files has a real item workload and a zero-byte denominator, which must not
	// become a 0% byte bar.
	r := newProgressRunner(t)
	require.NoError(t, r.db.Create(&[]*Entry{
		{Path: "empty-one", Change: entity.ScanChange_SCAN_CHANGE_ADDED, NeedsHash: true},
		{Path: "empty-two", Change: entity.ScanChange_SCAN_CHANGE_ADDED, NeedsHash: true},
		{Path: "settled", Change: entity.ScanChange_SCAN_CHANGE_ADDED},
	}).Error)
	r.phase = entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT
	stream := startContentAttempt(t, r, &Scope{})
	stream.lock.Lock()
	stream.processed = 1
	stream.lock.Unlock()
	r.sampleContentStage()

	progress, err := r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_ITEMS, progress.Stage.Unit)
	require.EqualValues(t, 3, *progress.Stage.Total)
	require.EqualValues(t, 2, progress.Stage.Completed, "completed work is the settled entry plus the read one")
	require.EqualValues(t, 2, progress.CopiedFileCount, "the item counter follows the same work")
	require.Zero(t, progress.TotalBytes)
}

func TestScanEntryTraversalStopsWithoutMaterializingRemainingPages(t *testing.T) {
	r := newProgressRunner(t)
	rows := make([]*Entry, 0, batchSize)
	for index := 0; index < batchSize*3; index++ {
		rows = append(rows, &Entry{Path: fmt.Sprintf("file-%04d", index), Change: entity.ScanChange_SCAN_CHANGE_ADDED})
		if len(rows) == batchSize {
			require.NoError(t, r.db.Create(&rows).Error)
			rows = rows[:0]
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	processed := 0
	err := r.eachEntry(ctx, &Scope{}, func(*Entry) error {
		processed++
		if processed == batchSize+5 {
			cancel()
		}
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, batchSize+5, processed)
}
