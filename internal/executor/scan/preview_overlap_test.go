package scan

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

// slowPreviewer blocks one source until released and counts the others.
type slowPreviewer struct {
	scanPreviewer
	slow     string
	release  chan struct{}
	finished atomic.Int64
	active   atomic.Int64
	peak     atomic.Int64
}

func (p *slowPreviewer) Exists([]byte) (bool, error) { return false, nil }

func (p *slowPreviewer) Generate(ctx context.Context, name string, hash []byte, size, mtime int64, update bool, settings *entity.PreviewJobSettings) ([]byte, error) {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for peak := p.peak.Load(); active > peak && !p.peak.CompareAndSwap(peak, active); peak = p.peak.Load() {
	}
	if name == p.slow {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer p.finished.Add(1)
	return []byte{1}, nil
}

func TestScanSlowPreviewHoldsOnlyItsOwnSlot(t *testing.T) {
	// One slow file must not idle the other slots at a page boundary: every other file of later
	// pages still completes while it runs, within the traversal's concurrency bound.
	previews := &slowPreviewer{slow: "/source/file-0000.jpg", release: make(chan struct{})}
	exe, _ := setupAnalyzeWithPreview(t, previews)
	r := newProgressRunner(t)
	r.exe, r.phase = exe, entity.JobPhase_JOB_PHASE_GENERATING_PREVIEWS
	count := batchSize*2 + 10
	for index := 0; index < count; index++ {
		require.NoError(t, r.db.Create(&Entry{Path: fmt.Sprintf("file-%04d.jpg", index),
			SourcePath: fmt.Sprintf("/source/file-%04d.jpg", index), Size: 1, SHA256: sha256Sum(fmt.Sprint(index))}).Error)
	}

	done := make(chan error, 1)
	go func() {
		done <- r.generatePreviews(context.Background(), &Config{Spec: &entity.ScanJobSpec{
			PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY}}, &Scope{}, nil)
	}()
	require.Eventually(t, func() bool { return previews.finished.Load() == int64(count-1) }, 10*time.Second, time.Millisecond,
		"files of later pages finish while the first page's slow file still runs")
	close(previews.release)
	require.NoError(t, <-done)
	require.EqualValues(t, count, previews.finished.Load())
	require.LessOrEqual(t, previews.peak.Load(), int64(16), "the traversal keeps its concurrency bound")
	require.EqualValues(t, count, r.previewOutcomes.ready)
}

func TestScanPreviewFollowsSavedContentFacts(t *testing.T) {
	// Preview reaches a row only when every lower row the content stage reads has saved its facts.
	r := newProgressRunner(t)
	for index := 1; index <= 5; index++ {
		require.NoError(t, r.db.Create(&Entry{ID: int64(index), Path: fmt.Sprint(index)}).Error)
	}
	settled := newSettledRows()
	settled.submit([]int64{2, 4})
	var lock sync.Mutex
	var seen []int64
	visited := func() []int64 { lock.Lock(); defer lock.Unlock(); return append([]int64(nil), seen...) }
	done := make(chan error, 1)
	go func() {
		done <- r.eachPreviewEntry(context.Background(), &Scope{}, 4, settled, func(_ context.Context, row *Entry) error {
			lock.Lock()
			seen = append(seen, row.ID)
			lock.Unlock()
			return nil
		})
	}()
	require.Eventually(t, func() bool { return len(visited()) == 1 }, time.Second, time.Millisecond)
	settled.save([]int64{4})
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, []int64{1}, visited(), "row 3 waits behind unsaved row 2")
	settled.save([]int64{2})
	require.Eventually(t, func() bool { return len(visited()) == 4 }, time.Second, time.Millisecond)
	settled.finish()
	require.NoError(t, <-done)
	require.ElementsMatch(t, []int64{1, 2, 3, 4, 5}, visited())
}

func TestScanContentStageAdvancesThePreviewBound(t *testing.T) {
	// The real read submits its pages in ID order and advances the bound as the writer saves them.
	r := newProgressRunner(t)
	entries := contentEntries(t, r, 1, "a", "bb", "ccc")
	settled := newSettledRows()
	config := &Config{Spec: &entity.ScanJobSpec{Selections: scanLocationSelections(1),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY}}
	require.NoError(t, r.readContent(testAttemptContext(context.Background()), config, &Scope{LocationID: 1}, nil, nil, settled))
	bound, final, err := settled.wait(context.Background(), 0)
	require.NoError(t, err)
	require.False(t, final)
	require.Equal(t, entries[len(entries)-1].ID, bound)
}

func TestScanPreviewFailureDoesNotStopTheContentRead(t *testing.T) {
	// Overlapping Preview fails the scope only after the content stage finished and saved its facts.
	previews := &scanPreviewer{fail: true}
	exe, source := setupAnalyzeWithPreview(t, previews)
	for index := 0; index < 20; index++ {
		writeAnalyzeFile(t, source, fmt.Sprintf("file-%02d.jpg", index), fmt.Sprint(index))
	}
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY,
		Selections: scanLocationSelections(source.ID), SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
		PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY})
	job, err := exe.GetJob(context.Background(), r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, "Preview generator failures are per-item findings")
	var unread, failed int64
	require.NoError(t, r.db.Model(&Entry{}).Where("needs_hash = ?", true).Count(&unread).Error)
	require.NoError(t, r.db.Model(&Entry{}).Where("preview_error <> ''").Count(&failed).Error)
	require.Zero(t, unread)
	require.EqualValues(t, 20, failed)
}
