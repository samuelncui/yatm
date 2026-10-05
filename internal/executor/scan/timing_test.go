package scan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestScanStageSnapshotUsesOnlyActiveScope(t *testing.T) {
	r := newProgressRunner(t)
	r.phase = entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT
	require.NoError(t, r.db.Create(&[]*Entry{
		{LocationID: 1, ScopePath: "active", Path: "pending", Size: 100, NeedsHash: true},
		{LocationID: 1, ScopePath: "active", Path: "ready", Size: 900},
		{LocationID: 1, ScopePath: "other", Path: "other", Size: 900, NeedsHash: true},
	}).Error)
	startContentAttempt(t, r, &Scope{ID: 1, LocationID: 1, Path: "active"})
	r.inflightBytes.Store(20)

	// Global business totals remain available, but progress and its denominator share the active scope.
	progress, err := r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1900, progress.TotalBytes)
	require.EqualValues(t, 1000, *progress.Stage.Total)
	require.EqualValues(t, 920, progress.Stage.Completed)
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, progress.Stage.EstimateState)
}

type delayedTimingSession struct{ checkSession }

func (s *delayedTimingSession) SourcePath(name string) (string, error) {
	// Simulate source preparation before ACP can emit the first byte-progress event.
	time.Sleep(30 * time.Millisecond)
	return s.checkSession.SourcePath(name)
}

func TestScanReadTimingStartsBeforeFirstACPResult(t *testing.T) {
	// A tiny read with slow preparation catches session clocks incorrectly started after ACP began reading.
	r := newProgressRunner(t)
	r.logger = logrus.New()
	r.logger.SetOutput(io.Discard)
	root := t.TempDir()
	filename := filepath.Join(root, "tiny")
	require.NoError(t, os.WriteFile(filename, []byte("x"), 0o644))
	info, err := os.Stat(filename)
	require.NoError(t, err)
	require.NoError(t, r.db.Create(&Entry{Path: "tiny", SourcePath: filename, Size: 1, Mode: uint32(info.Mode()),
		MtimeNS: info.ModTime().UnixNano(), NeedsHash: true}).Error)
	config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}})

	// The first manifest batch is source preparation: stalling it still lands inside the session.
	var once sync.Once
	require.NoError(t, r.db.Callback().Query().Before("gorm:query").Register("test:slow-first-batch", func(tx *gorm.DB) {
		if tx.Statement.Table != "entries" {
			return
		}
		once.Do(func() { time.Sleep(30 * time.Millisecond) })
	}))

	// The committed average includes initial source preparation even with no intermediate progress event.
	require.NoError(t, r.readContent(testAttemptContext(context.Background()), config, &Scope{}, nil,
		&delayedTimingSession{checkSession{root: root}}))
	average := r.progress.ToEntity().HistoricalAverageSpeedBytesPerSecond
	require.LessOrEqual(t, average, int64(33))
	require.Nil(t, r.contentScope)
}

func TestScanReadPreparationFailureReachesJobLog(t *testing.T) {
	// ACP preparation failures still require a path-specific warning from the content phase.
	r := newProgressRunner(t)
	var logs bytes.Buffer
	r.logger = logrus.New()
	r.logger.SetOutput(&logs)
	require.NoError(t, r.db.Create(&Entry{Path: "lost.jpg", SourcePath: filepath.Join(t.TempDir(), "lost.jpg"), NeedsHash: true}).Error)
	config := frozenLimits(&Config{Spec: &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY}})

	// The original read failure remains authoritative while the Job gains actionable evidence.
	err := r.readContent(testAttemptContext(context.Background()), config, &Scope{}, nil, nil)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Contains(t, logs.String(), "Scan content file failed")
	require.Contains(t, logs.String(), "level=warning")
	require.Contains(t, logs.String(), "path=lost.jpg")
}

func TestScanPhaseTransitionClearsReadSpeedsAndPreviewSamples(t *testing.T) {
	// Preserve historical ACP measurements internally while changing to a non-read stage.
	r := newProgressRunner(t)
	r.progress.UpdateSessionCurrent(1<<20, 1)
	r.progress.CommitSession()
	r.previewTiming = previewTiming{total: 9, completed: 3, samples: 3, started: time.Now().Add(-time.Minute)}
	r.contentScope = &Scope{LocationID: 1}
	var logs bytes.Buffer
	r.logger = logrus.New()
	r.logger.SetOutput(&logs)
	r.job = &executor.Job{ID: 42}
	r.setPhase(entity.JobPhase_JOB_PHASE_PROCESSING_CONTENT)
	r.setPhase(entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS)

	// Preview cannot inherit byte throughput or estimates from a preceding scope or phase.
	progress, err := r.progressSnapshot(context.Background())
	require.NoError(t, err)
	require.Zero(t, progress.SpeedBytesPerSecond)
	require.Zero(t, progress.AverageSpeedBytesPerSecond)
	require.Zero(t, progress.HistoricalAverageSpeedBytesPerSecond)
	require.Nil(t, progress.Stage.RemainingSeconds)
	require.Nil(t, r.contentScope)
	require.Equal(t, previewTiming{}, r.previewTiming)
	require.Contains(t, logs.String(), "Scan phase started")
	require.Contains(t, logs.String(), "Scan phase finished")
	require.Contains(t, logs.String(), "elapsed_ms=")
	require.Contains(t, logs.String(), "job_id=42")

	// Moving from preparation to explicit Tape execution clears timing before a new phase starts.
	r.previewTiming = previewTiming{samples: 5}
	r.resetAttemptProgress()
	require.Equal(t, previewTiming{}, r.previewTiming)
	require.True(t, r.phaseStarted.IsZero())
}

type cachedTimingPreviewer struct{ scanPreviewer }

func (*cachedTimingPreviewer) Exists([]byte) (bool, error) { return true, nil }

func TestScanPreviewSamplingExcludesReusedAndSkippedEntries(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			// Observe the sampler inside the real Generate boundary without synthetic decoder delays.
			previews := &scanPreviewer{}
			var implementation executor.Previewer = previews
			if cached {
				wrapped := &cachedTimingPreviewer{}
				previews, implementation = &wrapped.scanPreviewer, wrapped
			}
			exe, location := setupAnalyzeWithPreview(t, implementation)
			r := newProgressRunner(t)
			r.exe = exe
			r.phase = entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS
			var observations []previewTiming
			previews.before = func(context.Context) {
				r.lock.Lock()
				observations = append(observations, r.previewTiming)
				r.lock.Unlock()
			}

			// Include unsupported, unsigned and duplicate paths in the stage's settled item mix.
			for index, name := range []string{"one.jpg", "two.jpg", "three.jpg", "four.jpg", "unsupported.txt", "unsigned.jpg", "duplicate.jpg"} {
				content := fmt.Sprint(index)
				if name == "duplicate.jpg" {
					content = "0"
				}
				writeAnalyzeFile(t, location, name, content)
				path := filepath.Join(location.RootPath, name)
				info, err := os.Stat(path)
				require.NoError(t, err)
				hash := sha256.Sum256([]byte(content))
				row := &Entry{Path: name, SourcePath: path, Size: info.Size(), Mode: uint32(info.Mode()),
					MtimeNS: info.ModTime().UnixNano(), SHA256: hash[:]}
				if name == "unsigned.jpg" {
					row.SHA256 = nil
				}
				require.NoError(t, r.db.Create(row).Error)
			}

			require.NoError(t, r.generatePreviews(context.Background(), frozenLimits(&Config{Spec: &entity.ScanJobSpec{
				PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY}}), &Scope{}))

			// Published content never reaches the generator, so a cached scope costs no decoder call
			// and contributes no work sample; a missing scope classifies its four unique contents.
			if cached {
				require.Empty(t, observations, "a published bundle is served without a generator call")
				require.EqualValues(t, 5, r.previewOutcomes.ready, "four contents and their duplicate")
				require.EqualValues(t, 2, r.previewOutcomes.skipped, "unsupported and unsigned entries")
			} else {
				require.Len(t, observations, 4, "one generation per unique content, including the duplicate")
				require.EqualValues(t, 5, r.previewOutcomes.ready, "four contents and their duplicate")
				require.EqualValues(t, 2, r.previewOutcomes.skipped, "unsupported and unsigned entries")
				for _, observed := range observations {
					require.EqualValues(t, 7, observed.total)
					require.True(t, observed.workKnown, "the scope classified its unique generation workload")
					require.False(t, observed.started.IsZero(), "scope wall time includes classified work")
					require.Zero(t, observed.samples, "a fake Generate call without the decoder-start event is not a real work sample")
					require.EqualValues(t, 4, observed.workTotal)
				}
			}
			require.Equal(t, previewTiming{}, r.previewTiming)
		})
	}
}
