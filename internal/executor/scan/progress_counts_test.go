package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

// referenceStageInput is the per-request manifest aggregation the maintained counters replace.
func referenceStageInput(t *testing.T, r *runner, phase entity.JobPhase) executor.StageInput {
	t.Helper()
	query := func(predicate string, args ...any) executor.Counts {
		var result executor.Counts
		require.NoError(t, r.db.Model(&Entry{}).Where(predicate, args...).
			Select("COUNT(*) AS files, COALESCE(SUM(size), 0) AS bytes").Scan(&result).Error)
		return result
	}
	removed := entity.ScanChange_SCAN_CHANGE_REMOVED
	found := query("change != ?", removed)
	ratio := func(processed executor.Counts) executor.StageInput {
		in := executor.StageInput{Phase: phase, Completed: processed, TotalKnown: true}
		if found.Files != 0 {
			in.Total = &found
		}
		return in
	}
	switch phase {
	case entity.JobPhase_JOB_PHASE_UNSPECIFIED:
		if found.Files == 0 {
			return executor.StageInput{Phase: phase}
		}
		return executor.StageInput{Phase: phase, Completed: query("change != ? AND needs_hash = ?", removed, false), Manifest: &found}
	case entity.JobPhase_JOB_PHASE_COMPARING_CONTENT:
		return ratio(query("change != ? AND compared = ?", removed, true))
	case entity.JobPhase_JOB_PHASE_COMPLETED:
		return ratio(found)
	default:
		return executor.StageInput{Phase: phase, Completed: found}
	}
}

func requireCountsMatch(t *testing.T, r *runner, phases ...entity.JobPhase) {
	t.Helper()
	for _, phase := range phases {
		r.lock.Lock()
		in, err := r.stageInput(context.Background(), phase)
		r.lock.Unlock()
		require.NoError(t, err)
		require.Equal(t, referenceStageInput(t, r, phase), in, "phase %s", phase)
	}
	requireResultsMatch(t, r)
}

// referenceResults is the per-request result aggregation the maintained counters replace.
func referenceResults(t *testing.T, r *runner) *entity.GetScanJobProgressResponse {
	t.Helper()
	var groups []struct {
		Change       entity.ScanChange
		Finding      entity.ScanFinding
		Count, Bytes int64
	}
	require.NoError(t, r.db.Model(&Entry{}).Select("change, finding, COUNT(*) AS count, COALESCE(SUM(size),0) AS bytes").Group("change,finding").Find(&groups).Error)
	reply := &entity.GetScanJobProgressResponse{}
	for _, g := range groups {
		switch g.Change {
		case entity.ScanChange_SCAN_CHANGE_ADDED:
			reply.AddedCount += g.Count
		case entity.ScanChange_SCAN_CHANGE_CHANGED:
			reply.ChangedCount += g.Count
		case entity.ScanChange_SCAN_CHANGE_REMOVED:
			reply.RemovedCount += g.Count
		case entity.ScanChange_SCAN_CHANGE_UNCHANGED:
			reply.UnchangedCount += g.Count
		}
		if g.Change != entity.ScanChange_SCAN_CHANGE_REMOVED {
			reply.ProcessedBytes += g.Bytes
		}
		switch g.Finding {
		case entity.ScanFinding_SCAN_FINDING_MATCH:
			reply.MatchedCount += g.Count
		case entity.ScanFinding_SCAN_FINDING_MISMATCH:
			reply.DamagedCount += g.Count
		case entity.ScanFinding_SCAN_FINDING_MISSING:
			reply.MissingCount += g.Count
		case entity.ScanFinding_SCAN_FINDING_UNREADABLE:
			reply.UnreadableCount += g.Count
		case entity.ScanFinding_SCAN_FINDING_UNVERIFIABLE:
			reply.UnverifiableCount += g.Count
		}
	}
	require.NoError(t, r.db.Model(&Entry{}).Where("preview_error <> ''").Count(&reply.PreviewsFailedCount).Error)
	return reply
}

// requireResultsMatch compares the reported result counters with the aggregation they replace.
func requireResultsMatch(t *testing.T, r *runner) {
	t.Helper()
	r.lock.Lock()
	results, err := r.lockedResults(context.Background())
	r.lock.Unlock()
	require.NoError(t, err)
	got := &entity.GetScanJobProgressResponse{
		AddedCount: results.changes[entity.ScanChange_SCAN_CHANGE_ADDED], ChangedCount: results.changes[entity.ScanChange_SCAN_CHANGE_CHANGED],
		RemovedCount: results.changes[entity.ScanChange_SCAN_CHANGE_REMOVED], UnchangedCount: results.changes[entity.ScanChange_SCAN_CHANGE_UNCHANGED],
		ProcessedBytes: results.bytes, MatchedCount: results.findings[entity.ScanFinding_SCAN_FINDING_MATCH],
		DamagedCount: results.findings[entity.ScanFinding_SCAN_FINDING_MISMATCH], MissingCount: results.findings[entity.ScanFinding_SCAN_FINDING_MISSING],
		UnreadableCount: results.findings[entity.ScanFinding_SCAN_FINDING_UNREADABLE], UnverifiableCount: results.findings[entity.ScanFinding_SCAN_FINDING_UNVERIFIABLE],
		PreviewsFailedCount: results.previewFailures,
	}
	require.Equal(t, referenceResults(t, r), got)
}

func TestScanMaintainedResultsMatchManifestAggregation(t *testing.T) {
	ctx := context.Background()
	t.Run("verification findings", func(t *testing.T) {
		exe, volume, media := setupVerifyExecutor(t)
		saveVerifyFile(t, exe, volume, media, "good", []byte("good"))
		damaged := saveVerifyFile(t, exe, volume, media, "damaged", []byte("before"))
		missing := saveVerifyFile(t, exe, volume, media, "missing", []byte("gone"))
		saveUnverifiableFile(t, exe, volume, media, "unknown", []byte("unverified"))
		require.NoError(t, os.WriteFile(filepath.Join(volume.Root, damaged.Path), []byte("damage"), 0644))
		require.NoError(t, os.Remove(filepath.Join(volume.Root, missing.Path)))
		id, run := createVerifyJob(t, exe, media.ID, volume)
		run()
		waitVerifyIdle(t, exe, id, entity.JobStatus_JOB_STATUS_COMPLETED)
		value, err := exe.GetJobRunner(ctx, id)
		require.NoError(t, err)
		r := value.(*runner)
		require.True(t, r.resultsKnown, "the attempt maintains its counters")
		requireResultsMatch(t, r)
		require.EqualValues(t, 1, r.results.findings[entity.ScanFinding_SCAN_FINDING_MISMATCH])
		require.EqualValues(t, 1, r.results.findings[entity.ScanFinding_SCAN_FINDING_MISSING])
	})
	t.Run("Preview failures and changes", func(t *testing.T) {
		exe, location := setupAnalyzeWithPreview(t, &scanPreviewer{fail: true})
		writeAnalyzeFile(t, location, "photo.jpg", "photo")
		writeAnalyzeFile(t, location, "other.jpg", "other")
		writeAnalyzeFile(t, location, "notes.txt", "notes")
		r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(location.ID),
			SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS,
			PreviewPolicy: entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY})
		require.True(t, r.resultsKnown, "the attempt maintains its counters")
		requireResultsMatch(t, r)
		require.EqualValues(t, 2, r.results.previewFailures)

		// A second analysis observes the published originals: changes and removals move between counters.
		writeAnalyzeFile(t, location, "notes.txt", "rewritten notes")
		require.NoError(t, os.Remove(filepath.Join(location.RootPath, "other.jpg")))
		r, _ = runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(location.ID),
			SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
		require.True(t, r.resultsKnown)
		requireResultsMatch(t, r)
		require.EqualValues(t, 1, r.results.changes[entity.ScanChange_SCAN_CHANGE_REMOVED])
	})
}

// discoveryPhases report the discovered counters the indexing work path maintains; the processed
// counter an idle Job reports is counted when that phase starts.
var discoveryPhases = []entity.JobPhase{
	entity.JobPhase_JOB_PHASE_INDEXING, entity.JobPhase_JOB_PHASE_COMPARING_CONTENT,
	entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA,
	entity.JobPhase_JOB_PHASE_PUBLISHING_SOURCE, entity.JobPhase_JOB_PHASE_COMPLETED,
}

func TestScanMaintainedCountsMatchManifestAggregation(t *testing.T) {
	// Overlapping physical and duplicate logical selections across two Locations exercise the
	// uniqueness conflicts the discovery counters must not count twice.
	f := newInputFixture(t, batchSize+9, 2)
	spec := f.spec()
	spec.CompareLibrary = true
	spec.Selections = append(scanLocationSelections(f.locations[0].ID, "selected/file-00000000"), spec.Selections[0], spec.Selections[0])
	r, _ := runAnalysis(t, f.exe, spec)
	ctx := testAttemptContext(context.Background())
	var config Config
	require.NoError(t, r.db.First(&config, 1).Error)

	// Indexing advances its counters from its own writes after the phase counted them once.
	r.setPhase(entity.JobPhase_JOB_PHASE_INDEXING)
	require.NoError(t, r.prepareInputs(ctx, &config, nil))
	requireCountsMatch(t, r, discoveryPhases...)

	// Comparison counts once when it starts and advances per compared row.
	require.NoError(t, r.db.Model(&Entry{}).Where("1 = 1").Update("compared", false).Error)
	r.setPhase(entity.JobPhase_JOB_PHASE_COMPARING_CONTENT)
	requireCountsMatch(t, r, entity.JobPhase_JOB_PHASE_COMPARING_CONTENT)
	require.NoError(t, r.compareContent(ctx, &Scope{LocationID: f.locations[1].ID}))
	requireCountsMatch(t, r, entity.JobPhase_JOB_PHASE_COMPARING_CONTENT)
	r.setPhase(entity.JobPhase_JOB_PHASE_UNSPECIFIED)
	requireCountsMatch(t, r, entity.JobPhase_JOB_PHASE_UNSPECIFIED)

	// A reopened idle runner counts its unchanging manifest once.
	reopened := &runner{db: r.db, progress: executor.NewProgress()}
	requireCountsMatch(t, reopened, append(discoveryPhases, entity.JobPhase_JOB_PHASE_UNSPECIFIED)...)
}

func TestScanMaintainedCountsFollowMediaInventory(t *testing.T) {
	// Inventory enumeration replaces frozen REMOVED baselines and adds new paths.
	ctx := context.Background()
	exe, volume, media := setupScanExecutor(t)
	modified := time.Unix(10, 0)
	commitScanPositions(t, exe, media,
		&library.MediaFile{Path: "gone.txt", Size: 4, Mode: 0o644, ModTime: modified, Hash: sha256Sum("gone")},
		&library.MediaFile{Path: "kept.txt", Size: 4, Mode: 0o644, ModTime: modified, Hash: sha256Sum("kept")},
	)
	writeScanFile(t, volume.Root, "kept.txt", []byte("kept-and-longer"), 0o644, modified)
	writeScanFile(t, volume.Root, "added.txt", []byte("added"), 0o644, modified)
	job := createScanJob(t, exe, media.ID, false)
	waitScanStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_COMPLETED)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	require.NoError(t, r.db.First(&config, 1).Error)

	r.setPhase(entity.JobPhase_JOB_PHASE_INDEXING)
	require.NoError(t, r.freezeMedia(ctx, &config))
	requireCountsMatch(t, r, entity.JobPhase_JOB_PHASE_INDEXING)
	session := &inventoryFixture{checkSession: checkSession{root: volume.Root, media: media},
		paths: []string{"kept.txt", "added.txt", "kept.txt"}, storage: &entity.StoragePosition{Order: []byte{1}}}
	require.NoError(t, r.enumerateMedia(ctx, &config, &Scope{}, session))
	_, err = os.Stat(filepath.Join(volume.Root, "gone.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
	requireCountsMatch(t, r, discoveryPhases...)
	require.EqualValues(t, 2, r.counts.found.Files)
	require.EqualValues(t, 20, r.counts.found.Bytes)
}
