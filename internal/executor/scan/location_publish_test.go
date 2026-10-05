package scan

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestAnalyzePartialPublicationPreservesLastCompleteScan(t *testing.T) {
	for _, priorComplete := range []bool{false, true} {
		name := "no complete Scan"
		if priorComplete {
			name = "retained complete Scan"
		}
		t.Run(name, func(t *testing.T) {
			// Keep a selected original separate from content outside the one-file Scan.
			ctx := context.Background()
			exe, source := setupAnalyze(t)
			writeAnalyzeFile(t, source, "selected.txt", "old")
			writeAnalyzeFile(t, source, "untouched.txt", "retained")
			if priorComplete {
				runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(source.ID),
					SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
					ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
			}
			complete, err := exe.Lib().GetLocation(ctx, source.ID)
			require.NoError(t, err)
			before, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
			require.NoError(t, err)

			// Publish changed metadata through the same one-file known-only request as the Demo.
			writeAnalyzeFile(t, source, "selected.txt", "new selected metadata")
			r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(source.ID, "selected.txt"),
				SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
				ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
			job, err := exe.GetJob(ctx, r.job.ID)
			require.NoError(t, err)
			require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
			partial, err := exe.Lib().GetLocation(ctx, source.ID)
			require.NoError(t, err)
			require.Equal(t, r.job.ID, partial.LastJobID)
			after, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
			require.NoError(t, err)
			require.NotEmpty(t, after)
			require.Equal(t, "selected.txt", after[0].Path)
			require.EqualValues(t, len("new selected metadata"), after[0].Size)
			require.Empty(t, after[0].Hash)
			require.Empty(t, after[0].Signature)
			if priorComplete {
				require.Len(t, after, 2)
				require.Equal(t, before[0].FileID, after[0].FileID)
				require.Equal(t, before[1], after[1], "unselected original facts remain untouched")
			} else {
				require.Len(t, after, 1, "partial publication never admits an unselected original")
			}
			require.Equal(t, complete.LastSyncJobID, partial.LastSyncJobID)
			require.Equal(t, complete.LastSyncAtNS, partial.LastSyncAtNS)
		})
	}
}

func TestAnalyzeSelectedDirectoryPublishesAbsenceWithoutAdvancingCompleteScan(t *testing.T) {
	// A complete baseline includes a selected directory and an independent outside original.
	ctx := context.Background()
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "outside.txt", "untouched")
	writeAnalyzeFile(t, source, "selected/kept.txt", "old")
	writeAnalyzeFile(t, source, "selected/removed.txt", "removed")
	runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(source.ID),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	complete, err := exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	before, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, before, 3)

	// The completed selected range publishes both changed facts and its missing original.
	writeAnalyzeFile(t, source, "selected/kept.txt", "new selected metadata")
	require.NoError(t, os.Remove(filepath.Join(source.RootPath, "selected", "removed.txt")))
	r, progress := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(source.ID, "selected"),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	job, err := exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	require.EqualValues(t, 1, progress.RemovedCount)
	after, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, after, 2)
	require.Equal(t, before[0], after[0])
	require.Equal(t, before[1].FileID, after[1].FileID)
	require.EqualValues(t, len("new selected metadata"), after[1].Size)
	absent, err := exe.Lib().GetFileLocation(ctx, before[2].FileID)
	require.NoError(t, err)
	require.Nil(t, absent)
	_, err = exe.Lib().GetFile(ctx, before[2].FileID)
	require.NoError(t, err)
	partial, err := exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, complete.LastSyncJobID, partial.LastSyncJobID)
	require.Equal(t, complete.LastSyncAtNS, partial.LastSyncAtNS)
}

func TestAnalyzeCompleteScanMarkersAreLocationScoped(t *testing.T) {
	// Keep the two registered roots separate so one Job can fully observe only the first.
	ctx := context.Background()
	exe, first := setupAnalyze(t)
	second := &library.Location{Name: "Second", ExecutorID: "local",
		RootPath: filepath.Join(first.RootPath, "second-root")}
	require.NoError(t, os.Mkdir(second.RootPath, 0755))
	require.NoError(t, exe.Lib().CreateLocation(ctx, second))
	writeAnalyzeFile(t, first, "one.txt", "first")
	writeAnalyzeFile(t, second, "two.txt", "second")
	runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(second.ID),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	before, err := exe.Lib().GetLocation(ctx, second.ID)
	require.NoError(t, err)

	// Whole-Location coverage in one source must not advance a different source's partial markers.
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{
		Selections:      append(scanLocationSelections(first.ID), scanLocationSelections(second.ID, "two.txt")...),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	job, err := exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	whole, err := exe.Lib().GetLocation(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, r.job.ID, whole.LastSyncJobID)
	require.Positive(t, whole.LastSyncAtNS)
	partial, err := exe.Lib().GetLocation(ctx, second.ID)
	require.NoError(t, err)
	require.Equal(t, before.LastSyncJobID, partial.LastSyncJobID)
	require.Equal(t, before.LastSyncAtNS, partial.LastSyncAtNS)
}

func TestAnalyzeLibrarySelectionDoesNotClaimWholeLocationCoverage(t *testing.T) {
	// A Library directory contains every recorded original but cannot enumerate new Location files.
	ctx := context.Background()
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "indexed.txt", "old")
	runAnalysis(t, exe, &entity.ScanJobSpec{Selections: scanLocationSelections(source.ID),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	complete, err := exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	before, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, before, 1)
	file, err := exe.Lib().GetFile(ctx, before[0].FileID)
	require.NoError(t, err)

	// Re-observe the complete logical selection without promoting it to whole-Location coverage.
	writeAnalyzeFile(t, source, "indexed.txt", "new selected metadata")
	writeAnalyzeFile(t, source, "not-indexed.txt", "unselected")
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: []*entity.FileSelection{{
		Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: file.ParentID}},
		Scope:  entity.FileScope_FILE_SCOPE_ALL}},
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	job, err := exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)
	after, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, after, 1)
	require.Equal(t, before[0].FileID, after[0].FileID)
	require.EqualValues(t, len("new selected metadata"), after[0].Size)
	partial, err := exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, complete.LastSyncJobID, partial.LastSyncJobID)
	require.Equal(t, complete.LastSyncAtNS, partial.LastSyncAtNS)
}

func TestAnalyzeLocationRootNormalizesVisibilityScope(t *testing.T) {
	// Location selections cover live originals regardless of Library visibility preferences.
	ctx := context.Background()
	exe, source := setupAnalyze(t)
	writeAnalyzeFile(t, source, "unsaved.txt", "ordinary original")
	writeAnalyzeFile(t, source, "excluded/ignored.txt", "ignored original")
	selections := scanLocationSelections(source.ID)
	selections[0].Scope = entity.FileScope_FILE_SCOPE_SAVED
	r, _ := runAnalysis(t, exe, &entity.ScanJobSpec{Selections: selections,
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS})
	job, err := exe.GetJob(ctx, r.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)

	// The frozen request is ALL; unsaved files are observed and Ignore remains part of whole coverage.
	var config Config
	require.NoError(t, r.db.WithContext(ctx).First(&config, 1).Error)
	require.Equal(t, entity.FileScope_FILE_SCOPE_ALL, config.Spec.Selections[0].Scope)
	rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "unsaved.txt", rows[0].Path)
	location, err := exe.Lib().GetLocation(ctx, source.ID)
	require.NoError(t, err)
	require.Equal(t, r.job.ID, location.LastSyncJobID)
	require.Positive(t, location.LastSyncAtNS)
}
