package demo

import (
	"bytes"
	"context"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	archivejob "github.com/samuelncui/yatm/internal/executor/archive"
	restorejob "github.com/samuelncui/yatm/internal/executor/restore"
	scanjob "github.com/samuelncui/yatm/internal/executor/scan"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	previewpkg "github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPrepareCreatesOperableReviewFixture(t *testing.T) {
	// Create the complete fixture in a disposable path accepted by the safety boundary.
	t.Setenv("PATH", t.TempDir())
	root := filepath.Join(t.TempDir(), "yatm-demo-test")
	err := Prepare(context.Background(), Options{
		Root: root, Listen: "127.0.0.1:18080", Reset: true,
	})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(root, "config.yaml"))
	require.FileExists(t, filepath.Join(root, "read-info.sh"))

	// Verify mounted and unmounted Volume states use real marker directories.
	hdd, err := mediapkg.OpenVolume(filepath.Join(root, "volumes", "review-hdd"))
	require.NoError(t, err)
	require.Equal(t, entity.VolumeType_VOLUME_TYPE_HDD, hdd.Marker.Profile.Type)
	smr, err := mediapkg.OpenVolume(filepath.Join(root, "volumes", "review-hm-smr"))
	require.NoError(t, err)
	require.Equal(t, entity.VolumeType_VOLUME_TYPE_HM_SMR, smr.Marker.Profile.Type)
	require.NoDirExists(t, filepath.Join(root, "volumes", "review-offline"))
	require.DirExists(t, filepath.Join(root, "offline-shelf", "review-offline"))

	// A mounted disk without a marker stays available as an Initialize candidate.
	candidates, err := mediapkg.ListVolumeCandidates([]string{filepath.Join(root, "volumes")})
	require.NoError(t, err)
	states := make(map[string]mediapkg.VolumeCandidateState, len(candidates))
	for _, candidate := range candidates {
		states[filepath.Base(candidate.Root)] = candidate.State
	}
	require.Equal(t, mediapkg.VolumeCandidateInitialized, states["review-hdd"])
	require.Equal(t, mediapkg.VolumeCandidateInitialized, states["review-hm-smr"])
	require.Equal(t, mediapkg.VolumeCandidateUninitialized, states["review-new-disk"])
	require.NotContains(t, states, "review-offline")

	// Verify annotations and the large result set used for search pagination.
	db, err := resource.OpenSQLite(filepath.Join(root, databaseName))
	require.NoError(t, err)
	requireTableCount(t, db, library.ModelMedia, 4)
	requireTableCount(t, db, library.ModelFile, 363)
	requireTableCount(t, db, library.ModelFileTag, 345)
	requireTableCount(t, db, executor.ModelJob, 13)

	// The fixture contains only background workflows; ordinary file edits are request-bound.
	var unsupportedJobs int64
	require.NoError(t, db.Model(executor.ModelJob).Where("catalog_kind NOT IN ?", []entity.JobKind{
		entity.JobKind_JOB_KIND_ARCHIVE, entity.JobKind_JOB_KIND_RESTORE, entity.JobKind_JOB_KIND_SCAN,
	}).Count(&unsupportedJobs).Error)
	require.Zero(t, unsupportedJobs)
	requireTableCount(t, db, &library.Location{}, 5)

	// Curated Files retain independent organization and annotations.
	lib := library.New(db)
	budget, err := lib.GetByPath(context.Background(), library.Root.ID, "Projects/aurora/budget.csv")
	require.NoError(t, err)
	require.Equal(t, "Draft budget; finance review is still pending.", budget.Note)
	tags, err := lib.MGetFileTags(context.Background(), budget.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"confidential", "finance", "project"}, tags[budget.ID])

	// Saved image revisions and the video expose the standard Preview roles.
	previewFile, err := lib.GetByPath(context.Background(), library.Root.ID, "Photos/archive-room.png")
	require.NoError(t, err)
	previews, err := previewpkg.New(previewpkg.Config{Root: filepath.Join(root, "previews")}, filepath.Join(root, "work"))
	require.NoError(t, err)
	manifest, err := previews.Manifest(previewFile.Signature)
	require.NoError(t, err)
	require.Equal(t, "thumbnail", manifest.Assets[0].Role)
	requireReviewImageVersions(t, lib, previews, root, previewFile.ID)
	videoFile, err := lib.GetByPath(context.Background(), library.Root.ID, "Photos/"+demoVideoName)
	require.NoError(t, err)
	videoManifest, err := previews.Manifest(videoFile.Signature)
	require.NoError(t, err)
	roles := make([]string, 0, len(videoManifest.Assets))
	for _, asset := range videoManifest.Assets {
		roles = append(roles, asset.Role)
	}
	require.Equal(t, []string{"poster", "timeline", "timeline-map"}, roles)
	require.Equal(t, demoVideoCredit, videoFile.Note)
	requireBundledVideoPreviews(t, previews, videoFile.Signature)
	videoOriginal, err := lib.GetByPath(context.Background(), library.Root.ID, "Unforged/Incoming/Camera A/"+demoVideoName)
	require.NoError(t, err)
	require.Equal(t, demoVideoCredit, videoOriginal.Note)
	require.Equal(t, videoFile.Signature, videoOriginal.Signature)
	videoSource, err := os.ReadFile(filepath.Join(
		root, "source", "Incoming Review", "Camera A", demoVideoName,
	))
	require.NoError(t, err)
	require.Equal(t, bundledVideo, videoSource)
	require.GreaterOrEqual(t, len(videoSource), 8)
	require.Equal(t, []byte("ftyp"), videoSource[4:8])
	// Keep both search pages available for testing refresh without collapsing the loaded window.
	var invoices int64
	require.NoError(t, db.Model(library.ModelFileTag).Where("tag = ?", "invoice").Count(&invoices).Error)
	require.Equal(t, int64(101), invoices)
	firstPage, err := lib.SearchFiles(context.Background(), "tag:invoice", "", 100)
	require.NoError(t, err)
	require.Len(t, firstPage.Results, 100)
	require.NotEmpty(t, firstPage.NextCursor)
	secondPage, err := lib.SearchFiles(context.Background(), "tag:invoice", firstPage.NextCursor, 100)
	require.NoError(t, err)
	require.Len(t, secondPage.Results, 1)
	require.Empty(t, secondPage.NextCursor)

	// Verify every pending workflow contains actionable manifest rows.
	archiveDB := openReviewJobDB(t, db, root, entity.JobKind_JOB_KIND_ARCHIVE)
	requireTableCount(t, archiveDB, &archivejob.Item{}, 4)
	restoreDB := openReviewJobDB(t, db, root, entity.JobKind_JOB_KIND_RESTORE, entity.JobStatus_JOB_STATUS_READY)
	var restoreFiles int64
	require.NoError(t, restoreDB.Model(&restorejob.File{}).Distinct("file_id").Count(&restoreFiles).Error)
	require.Equal(t, int64(3), restoreFiles)

	// Single-file recovery publishes an observed path without claiming a complete Location scan.
	var destination library.Location
	require.NoError(t, db.Where("name = ?", "Restored files").First(&destination).Error)
	require.True(t, destination.RestoreTarget)
	require.Zero(t, destination.LastSyncAtNS)
	var recovered library.FileLocation
	require.NoError(t, db.Where("location_id = ?", destination.ID).First(&recovered).Error)
	require.Equal(t, "Recovered sample/Research/query-examples.txt", recovered.Path)
	recoveryDB := openReviewJobDB(t, db, root, entity.JobKind_JOB_KIND_RESTORE, entity.JobStatus_JOB_STATUS_COMPLETED)
	var recovery restorejob.File
	require.NoError(t, recoveryDB.Where("completed = ?", true).First(&recovery).Error)
	require.True(t, recovery.Linked)
	require.Equal(t, recovered.FileID, recovery.ResultFileID)
	require.False(t, db.Migrator().HasTable("restore_results"))

	// Real Verify findings make both retained bad history and checked healthy copies reviewable.
	verifyDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool {
		return spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES
	}, entity.JobStatus_JOB_STATUS_COMPLETED)
	for _, finding := range []entity.ScanFinding{entity.ScanFinding_SCAN_FINDING_MISMATCH, entity.ScanFinding_SCAN_FINDING_MISSING} {
		var count int64
		require.NoError(t, verifyDB.Model(&scanjob.Entry{}).Where("finding = ?", finding).Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
	var healthy int64
	require.NoError(t, db.Model(&library.Position{}).Where("health = ?", entity.PositionHealth_POSITION_HEALTH_HEALTHY).Count(&healthy).Error)
	require.Positive(t, healthy)

	// The pending integrity check freezes only Tape inventory, never a freely chosen backend.
	tapeCheckDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool {
		return spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES
	}, entity.JobStatus_JOB_STATUS_READY)
	var tapeCheck scanjob.Config
	require.NoError(t, tapeCheckDB.First(&tapeCheck).Error)
	require.Equal(t, TapeBarcode, tapeCheck.MediaIdentity)
	tape, err := lib.GetMedia(context.Background(), tapeCheck.Spec.MediaId)
	require.NoError(t, err)
	require.Equal(t, entity.MediaKind_MEDIA_KIND_TAPE, tape.Kind)
	requireTableCount(t, tapeCheckDB, &scanjob.Entry{}, 2)

	// Inventory results remain independent of content-integrity observations.
	scanDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool {
		return spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_INVENTORY
	}, entity.JobStatus_JOB_STATUS_COMPLETED)
	scanRecord := new(executor.JobRecord)
	require.NoError(t, scanDB.First(scanRecord, 1).Error)
	require.NotNil(t, scanRecord.LatestAttemptStartedAtNS)
	require.NotNil(t, scanRecord.LatestAttemptFinishedAtNS)
	require.GreaterOrEqual(t, *scanRecord.LatestAttemptFinishedAtNS, *scanRecord.LatestAttemptStartedAtNS)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, scanRecord.Status)
	changes := map[entity.ScanChange]int64{}
	for _, change := range []entity.ScanChange{
		entity.ScanChange_SCAN_CHANGE_ADDED,
		entity.ScanChange_SCAN_CHANGE_CHANGED,
		entity.ScanChange_SCAN_CHANGE_REMOVED,
	} {
		var count int64
		require.NoError(t, scanDB.Model(&scanjob.Entry{}).Where("change = ?", change).Count(&count).Error)
		changes[change] = count
	}
	require.Equal(t, map[entity.ScanChange]int64{
		entity.ScanChange_SCAN_CHANGE_ADDED:   1,
		entity.ScanChange_SCAN_CHANGE_CHANGED: 1,
		entity.ScanChange_SCAN_CHANGE_REMOVED: 1,
	}, changes)
	previewDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool {
		return spec.PreviewPolicy != entity.PreviewPolicy_PREVIEW_POLICY_NONE
	}, entity.JobStatus_JOB_STATUS_COMPLETED)

	// A successful Preview is not recorded per entry: the content-addressed store holds the bundles
	// that the entries with a content identity resolve to.
	store, err := newPreviewManager(root, filepath.Join(root, "work"), false)
	require.NoError(t, err)
	var previewEntries []*scanjob.Entry
	require.NoError(t, previewDB.Where("length(sha256) = 32").Find(&previewEntries).Error)
	readyPreviews := 0
	for _, entry := range previewEntries {
		signature, err := library.NewFileSignature(entry.SHA256, entry.Size)
		require.NoError(t, err)
		exists, err := store.Exists(signature)
		require.NoError(t, err)
		if exists {
			readyPreviews++
		}
	}
	require.Equal(t, 2, readyPreviews)
	previewConfig := new(scanjob.Config)
	require.NoError(t, previewDB.First(previewConfig, 1).Error)
	require.NotNil(t, previewConfig.Spec)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_POLICY_REGENERATE_ALL, previewConfig.Spec.PreviewPolicy)
	previewRecord := new(executor.JobRecord)
	require.NoError(t, previewDB.First(previewRecord, 1).Error)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, previewRecord.Status)

	// Locations distinguish a completed Scan from actual filesystem availability.
	var locations []*library.Location
	require.NoError(t, db.Order("id").Find(&locations).Error)
	require.ElementsMatch(t, []string{"Documents", "Unavailable originals", "Shared files", "Incoming", "Restored files"},
		[]string{locations[0].Name, locations[1].Name, locations[2].Name, locations[3].Name, locations[4].Name})
	documents, unavailable := locations[0], locations[1]
	require.Positive(t, documents.LastSyncAtNS)
	require.Positive(t, unavailable.LastSyncAtNS)
	require.NoDirExists(t, unavailable.RootPath)
	require.DirExists(t, documents.RootPath)
	require.Positive(t, documents.LastJobID)
	requireReviewJobLinks(t, db, root, locations)

	// Scan exposes one actionable Job error, and a readable sibling is not partially published.
	failedDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool {
		return len(spec.Selections) > 0 && spec.Selections[0].GetLocation().GetLocationId() == documents.ID
	}, entity.JobStatus_JOB_STATUS_FAILED)
	var failedRecord executor.JobRecord
	require.NoError(t, failedDB.First(&failedRecord, 1).Error)
	require.Contains(t, failedRecord.Error, "reconnected.txt")
	require.Positive(t, documents.LastSyncAtNS)
	retainedRows, err := lib.LocationOriginalsPage(context.Background(), documents.ID, "", 100)
	require.NoError(t, err)
	for _, row := range retainedRows {
		require.NotEqual(t, "ready.txt", row.Path)
		require.NotEqual(t, "reconnected.txt", row.Path)
	}
	require.FileExists(t, filepath.Join(documents.RootPath, "ready.txt"))
	require.FileExists(t, filepath.Join(documents.RootPath, "reconnected.txt"))
	for _, name := range []string{"scopes", "observations", "originals"} {
		require.False(t, failedDB.Migrator().HasTable(name))
	}

	// Live browsing adds neither a new arrival nor an empty directory to the Library.
	indexed, err := lib.LocationOriginalsPage(context.Background(), documents.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, indexed, 6)
	require.FileExists(t, filepath.Join(documents.RootPath, "new-arrival.txt"))
	require.DirExists(t, filepath.Join(documents.RootPath, "Empty folder"))
	unadmitted, err := lib.GetByPath(context.Background(), library.Root.ID, "Unforged/Documents/new-arrival.txt")
	require.NoError(t, err)
	require.Nil(t, unadmitted)

	// Unarchived current text keeps its identity, annotations and three dated saved versions.
	current, err := lib.GetByPath(context.Background(), library.Root.ID, "Unforged/Documents/mutable.txt")
	require.NoError(t, err)
	require.NotEmpty(t, current.Note)
	original, err := lib.GetFileLocation(context.Background(), current.ID)
	require.NoError(t, err)
	require.NotNil(t, original)
	latest, err := lib.LatestFileVersion(context.Background(), current.ID)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.NotEqual(t, original.Signature, latest.Signature)
	require.NoError(t, lib.HydrateFileContent(context.Background(), current))
	require.Zero(t, current.ContentSummary.ArchivedCopyCount)
	requireDatedRestoreFixtures(t, lib, current.ID, previewFile.ID)
	extra, err := lib.GetByPath(context.Background(), library.Root.ID, "Unforged/Documents/mutable (1).txt")
	require.NoError(t, err)
	require.Nil(t, extra)

	// Unknown content remains unsigned, while known coverage supplies a version without another copy.
	unknown, err := lib.SearchFiles(context.Background(), "has:unknown", "", 100)
	require.NoError(t, err)
	require.Len(t, unknown.Results, 1)
	require.Equal(t, "notes.txt", unknown.Results[0].File.Name)
	page, err := lib.SearchFiles(context.Background(), "name:handbook.md AND has:original AND has:archive", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Equal(t, "handbook.md", page.Results[0].File.Name)
	versions, _, err := lib.ListFileVersions(context.Background(), page.Results[0].File.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1, "known archived content is a saved version without another physical copy")
	require.Equal(t, page.Results[0].File.ID, versions[0].FileID)
	require.NotNil(t, versions[0].FirstArchivedAtNS)
	covered, _, err := lib.ListContentCopies(context.Background(), versions[0].Signature, 0, 10)
	require.NoError(t, err)
	require.Len(t, covered, 1)

	// The product review tag works through the same quoted simple query used by the frontend.
	review, err := lib.SearchFiles(
		context.Background(), `(name:"archive-review" OR tag:"archive-review" OR note:"archive-review")`, "", 100,
	)
	require.NoError(t, err)
	require.Len(t, review.Results, 5)
	needsArchive, err := lib.SearchFiles(
		context.Background(), `(tag:"archive-review") AND (has:original AND NOT has:unknown AND NOT has:archive)`, "", 100,
	)
	require.NoError(t, err)
	require.Len(t, needsArchive.Results, 3,
		"unavailable originals remain indexed and unknown content is not classified as needing Archive")

	// Duplicate search compares published originals across Locations even when one root is unavailable.
	requireReviewDuplicates(t, lib, documents.ID, unavailable.ID)
	requireReviewIdenticalHistory(t, lib)
	requireReviewIdenticalPaging(t, lib)
	require.NoError(t, lib.HydrateFileContent(
		context.Background(), current, page.Results[0].File, unknown.Results[0].File,
	))
	require.True(t, current.ContentSummary.HasVersions)
	require.Zero(t, current.ContentSummary.ArchivedCopyCount)
	require.Positive(t, page.Results[0].File.ContentSummary.ArchivedCopyCount)
	require.False(t, unknown.Results[0].File.ContentSummary.SignatureKnown)
	requireReviewVersionHistory(t, lib, root, current.ID, budget.ID)

	// Exercise the normal Trash allocation without colliding with the visible dot-directory fixture.
	require.NoError(t, lib.Delete(context.Background(), []int64{budget.ID}))
	parents, err := lib.ListParents(context.Background(), budget.ID)
	require.NoError(t, err)
	require.NotEmpty(t, parents)
	require.Equal(t, int64(library.TrashFileID), parents[0].ID)
	dotDirectory, err := lib.GetByPath(context.Background(), library.Root.ID, ".review")
	require.NoError(t, err)
	require.NotNil(t, dotDirectory)

	// Verify reuse preserves reviewer-created runtime files and Library edits.
	reviewerFile := filepath.Join(root, "target", "reviewer-created.txt")
	require.NoError(t, os.WriteFile(reviewerFile, []byte("keep"), 0o644))
	require.NoError(t, Prepare(context.Background(), Options{Root: root, Listen: "127.0.0.1:18080"}))
	require.FileExists(t, reviewerFile)
	parents, err = lib.ListParents(context.Background(), budget.ID)
	require.NoError(t, err)
	require.Equal(t, int64(library.TrashFileID), parents[0].ID)
}

func requireReviewJobLinks(t *testing.T, db *gorm.DB, root string, locations []*library.Location) {
	t.Helper()
	// Location navigation and copy-health results must still resolve after setup Job deletion.
	var ids []int64
	require.NoError(t, db.Model(&library.Position{}).Distinct("health_job_id").Pluck("health_job_id", &ids).Error)
	for _, location := range locations {
		ids = append(ids, location.LastJobID, location.LastSyncJobID)
	}
	for _, id := range ids {
		if id == 0 {
			continue
		}
		var count int64
		require.NoError(t, db.Model(executor.ModelJob).Where("id = ?", id).Count(&count).Error)
		require.EqualValues(t, 1, count, "referenced Job %d was deleted", id)
		require.FileExists(t, filepath.Join(root, "work", "jobs", strconv.FormatInt(id, 10), "state.db"))
	}
}

func requireReviewIdenticalHistory(t *testing.T, lib *library.Library) {
	// A retained unavailable original still participates through recorded current and saved evidence.
	t.Helper()
	ctx := context.Background()
	snapshot, err := lib.OpenIdenticalSnapshot(ctx, library.IdenticalScope{Source: library.IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	groups, err := snapshot.Groups("", 100)
	require.NoError(t, err)
	var history *library.IdenticalGroup
	for index := range groups.Groups {
		group := &groups.Groups[index]
		if group.Count == 3 && group.Name == "a.txt" {
			history = group
		}
	}
	require.NotNil(t, history)

	// Saved evidence connects different current contents and keeps the unavailable member.
	members, err := snapshot.Members(history.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, members.Members, 3)
	originals := make(map[string]*library.FileLocation, len(members.Members))
	for _, member := range members.Members {
		require.NotEmpty(t, member.Evidence)
		require.NotNil(t, member.Original)
		originals[member.Name] = member.Original
	}
	require.Contains(t, originals, "a.txt")
	require.Contains(t, originals, "b.txt")
	require.Contains(t, originals, "c.txt")
	require.Greater(t, originals["a.txt"].Size, originals["b.txt"].Size)
	require.NotEqual(t, originals["a.txt"].Signature, originals["b.txt"].Signature)
	require.NotEqual(t, originals["b.txt"].Signature, originals["c.txt"].Signature)

	// Removing a saved version remains an independent action from deleting archive bytes.
	file, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Shared files/a.txt")
	require.NoError(t, err)
	versions, _, err := lib.ListFileVersions(ctx, file.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1, "the Demo exposes a removable saved-version record without deleting archive bytes")
}

func requireReviewIdenticalPaging(t *testing.T, lib *library.Library) {
	// One operable Location supplies a group spanning pages and a few independently removable groups.
	t.Helper()
	ctx := context.Background()
	shared, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Shared files/Large group/.member-000.txt")
	require.NoError(t, err)
	require.NotNil(t, shared)
	original, err := lib.GetFileLocation(ctx, shared.ID)
	require.NoError(t, err)
	require.NotNil(t, original)
	snapshot, err := lib.OpenIdenticalSnapshot(ctx, library.IdenticalScope{
		Source: library.IdenticalLocations,
		Roots:  []library.IdenticalRoot{{LocationID: original.LocationID}},
	})
	require.NoError(t, err)
	defer snapshot.Close()
	require.EqualValues(t, 6, snapshot.GroupCount)
	require.EqualValues(t, 220, snapshot.AllRows)
	require.EqualValues(t, 217, snapshot.VisibleRows)
	groups, err := snapshot.Groups("", 10)
	require.NoError(t, err)
	counts := make([]int64, 0, len(groups.Groups))
	for _, group := range groups.Groups {
		counts = append(counts, group.Count)
	}
	require.ElementsMatch(t, []int64{201, 3, 4, 2, 2, 2}, counts)

	// Jump across unloaded pages and switch hidden projections without losing complete group counts.
	for _, includeHidden := range []bool{false, true} {
		for _, offset := range []int64{0, 200, 100} {
			rows, total, err := snapshot.Rows(offset, 100, includeHidden)
			require.NoError(t, err)
			require.NotEmpty(t, rows)
			require.Equal(t, offset, rows[0].Position)
			require.Greater(t, total, offset)
			for _, row := range rows {
				if row.Group.Count != 201 {
					continue
				}
				if includeHidden {
					require.EqualValues(t, 201, row.DisplayCount)
				} else {
					require.EqualValues(t, 198, row.DisplayCount)
				}
			}
		}
	}
}

func requireReviewDuplicates(t *testing.T, lib *library.Library, documentsID, unavailableID int64) {
	// Count complete content groups across ordinary small search pages, not within one page.
	t.Helper()
	ctx := context.Background()
	groups := make(map[string]int)
	ids := make(map[int64]struct{})
	cursor := ""
	for {
		page, err := lib.SearchFiles(ctx, "tag:duplicate-review AND has:duplicates", cursor, 2)
		require.NoError(t, err)
		for _, result := range page.Results {
			require.NotContains(t, ids, result.File.ID)
			ids[result.File.ID] = struct{}{}
			require.NotEmpty(t, result.File.Signature)
			groups[string(result.File.Signature)]++
		}
		require.LessOrEqual(t, len(ids), 9)
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	sizes := make([]int, 0, len(groups))
	for _, count := range groups {
		sizes = append(sizes, count)
	}
	require.ElementsMatch(t, []int{2, 3, 4}, sizes)

	// All members are discoverable by tag; a Location filter does not erase matches elsewhere.
	page, err := lib.SearchFiles(ctx, "tag:duplicate-review AND has:duplicates", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Results, 9)
	page, err = lib.SearchFiles(ctx, "location:"+strconv.FormatInt(documentsID, 10)+" AND has:duplicates", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Equal(t, "original-only.txt", page.Results[0].File.Name)
	unbackedID := page.Results[0].File.ID

	// Global duplicates and an explicit Inspector selection remain actionable in saved-only Library views.
	settings, err := lib.Settings().Library.Current(ctx)
	require.NoError(t, err)
	hidden, err := lib.Settings().Library.Save(ctx, &entity.LibrarySettings{
		IncludeUnbackedFiles: false,
		ConfirmRemove:        settings.ConfirmRemove,
	})
	require.NoError(t, err)
	selection, err := lib.InspectSelection(ctx, &entity.SelectionInspection{Selections: []*entity.FileSelection{{
		Scope:  entity.FileScope_FILE_SCOPE_ALL,
		Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: unbackedID}},
	}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, selection.FileCount)
	require.Zero(t, selection.MissingOriginalCount)

	// The identical engine exposes complete groups and every member of each rendered page.
	snapshot, err := lib.OpenIdenticalSnapshot(ctx, library.IdenticalScope{Source: library.IdenticalLibrary})
	require.NoError(t, err)
	defer snapshot.Close()
	grouped, err := snapshot.Groups("", 100)
	require.NoError(t, err)
	var counts []int64
	for _, group := range grouped.Groups {
		counts = append(counts, group.Count)
		var count int64
		cursor := ""
		for {
			members, err := snapshot.Members(group.ID, cursor, 100)
			require.NoError(t, err)
			count += int64(len(members.Members))
			cursor = members.NextCursor
			if cursor == "" {
				break
			}
		}
		require.Equal(t, group.Count, count)
	}
	require.Subset(t, counts, []int64{2, 3, 4})

	// A Locations scope collects recorded originals under its roots, including an unavailable one.
	scoped, err := lib.OpenIdenticalSnapshot(ctx, library.IdenticalScope{
		Source: library.IdenticalLocations,
		Roots:  []library.IdenticalRoot{{LocationID: documentsID}, {LocationID: unavailableID}},
	})
	require.NoError(t, err)
	defer scoped.Close()
	locationGroups, err := scoped.Groups("", 100)
	require.NoError(t, err)
	pair := false
	for _, group := range locationGroups.Groups {
		members, err := scoped.Members(group.ID, "", 100)
		require.NoError(t, err)
		hasLive, hasUnavailable := false, false
		for _, member := range members.Members {
			if member.Original == nil {
				continue
			}
			switch member.Original.Path {
			case "original-only.txt":
				hasLive = true
			case "copy-of-original-only.txt":
				hasUnavailable = true
			}
		}
		if hasLive && hasUnavailable {
			require.Equal(t, int64(2), group.Count)
			pair = true
		}
	}
	require.True(t, pair, "the cross-Location pair remains one complete group")
	_, err = lib.Settings().Library.Save(ctx, &entity.LibrarySettings{
		IncludeUnbackedFiles: settings.IncludeUnbackedFiles,
		ConfirmRemove:        hidden.ConfirmRemove,
	})
	require.NoError(t, err)
}

func requireReviewImageVersions(
	t *testing.T, lib *library.Library, previews *previewpkg.Manager, root string, fileID int64,
) {
	// The existing image retains one File and three separately recoverable versions with different thumbnails.
	t.Helper()
	versions, more, err := lib.ListFileVersions(context.Background(), fileID, 0, 10)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, versions, 3)
	colors := []color.RGBA{
		{R: 28, G: 72, B: 126, A: 255}, {R: 145, G: 65, B: 25, A: 255}, {R: 26, G: 112, B: 77, A: 255},
	}
	for index, version := range versions {
		manifest, err := previews.Manifest(version.Signature)
		require.NoError(t, err)
		require.Equal(t, version.Signature, manifest.FileSignature)
		asset, err := previews.Open(version.Signature, "thumbnail")
		require.NoError(t, err)
		data, err := io.ReadAll(asset)
		require.NoError(t, err)
		require.NoError(t, asset.Close())
		image, err := png.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		require.Equal(t, colors[index], color.RGBAModel.Convert(image.At(0, 0)))
		copies, _, err := lib.ListContentCopies(context.Background(), version.Signature, 0, 10)
		require.NoError(t, err)
		require.Len(t, copies, 1)
		content, err := os.ReadFile(filepath.Join(root, "volumes", "review-hdd", filepath.FromSlash(copies[0].Path)))
		require.NoError(t, err)
		require.Equal(t, content, data, "thumbnail must show this version's content, not the latest image")
	}
}

func requireReviewVersionHistory(t *testing.T, lib *library.Library, root string, currentID, budgetID int64) {
	// Ordinary saved files have publication dates, while legacy inventory explicitly lacks them.
	t.Helper()
	ctx := context.Background()
	budgetVersions, _, err := lib.ListFileVersions(ctx, budgetID, 0, 10)
	require.NoError(t, err)
	require.Len(t, budgetVersions, 1)
	require.NotNil(t, budgetVersions[0].FirstArchivedAtNS)
	require.NotNil(t, budgetVersions[0].LastArchivedAtNS)
	legacy, err := lib.GetByPath(ctx, library.Root.ID, "Tape Archive/legacy/board-minutes-2024.txt")
	require.NoError(t, err)
	require.Contains(t, legacy.Note, "original archive date was not recorded")
	legacyVersions, _, err := lib.ListFileVersions(ctx, legacy.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, legacyVersions, 1)
	require.Nil(t, legacyVersions[0].FirstArchivedAtNS)
	require.Nil(t, legacyVersions[0].LastArchivedAtNS)

	// Every seeded revision is a different recoverable content set, not a duplicate copy of one version.
	versions, more, err := lib.ListFileVersions(ctx, currentID, 0, 10)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, versions, 3)
	contents := []string{
		"First observed revision.\n",
		"Second revision: include the team's review comments.\n",
		"Third revision: approved notes with the final checklist and follow-up actions.\n",
	}
	signatures := make(map[string]bool, len(versions))
	for index, version := range versions {
		require.NotNil(t, version.FirstArchivedAtNS)
		require.Positive(t, *version.FirstArchivedAtNS)
		require.Equal(t, version.FirstArchivedAtNS, version.LastArchivedAtNS)
		require.EqualValues(t, len(contents[index]), version.Size)
		require.False(t, signatures[string(version.Signature)])
		signatures[string(version.Signature)] = true
		copies, more, err := lib.ListContentCopies(ctx, version.Signature, 0, 10)
		require.NoError(t, err)
		require.False(t, more)
		require.Len(t, copies, 1)
		content, err := os.ReadFile(filepath.Join(root, "volumes", "review-hdd", filepath.FromSlash(copies[0].Path)))
		require.NoError(t, err)
		require.Equal(t, contents[index], string(content))
	}
}

func TestValidateRootRejectsSymlinkEscape(t *testing.T) {
	// Resolve outside every temporary root even when the checkout is itself temporary.
	directory := t.TempDir()
	alias := filepath.Join(directory, "outside")
	require.NoError(t, os.Symlink(filepath.VolumeName(directory)+string(filepath.Separator), alias))

	// Validation follows the link without creating or removing its target.
	_, err := validateRoot(filepath.Join(alias, "yatm-demo-escape"))
	require.ErrorContains(t, err, "must be inside a temporary directory")
}

func TestValidateListenRequiresLoopback(t *testing.T) {
	// Accept local review addresses and reject externally reachable listeners.
	for _, value := range []string{"127.0.0.1:18080", "localhost:18080", "[::1]:18080"} {
		actual, err := validateListen(value)
		require.NoError(t, err)
		require.Equal(t, value, actual)
	}
	for _, value := range []string{"", "0.0.0.0:18080", "127.0.0.1:0", "127.0.0.1:not-a-port"} {
		_, err := validateListen(value)
		require.Error(t, err)
	}
}

func TestValidateVideoPathRequiresMP4File(t *testing.T) {
	// Accept a regular MP4 and reject missing, non-MP4, and directory inputs.
	video := filepath.Join(t.TempDir(), "sample.mp4")
	require.NoError(t, os.WriteFile(video, []byte("fixture"), 0o644))
	actual, err := validateVideoPath(video)
	require.NoError(t, err)
	canonical, err := filepath.EvalSymlinks(video)
	require.NoError(t, err)
	require.Equal(t, canonical, actual)

	for _, value := range []string{filepath.Join(t.TempDir(), "missing.mp4"), t.TempDir()} {
		_, err := validateVideoPath(value)
		require.Error(t, err)
	}
	text := filepath.Join(t.TempDir(), "sample.txt")
	require.NoError(t, os.WriteFile(text, []byte("fixture"), 0o644))
	_, err = validateVideoPath(text)
	require.ErrorContains(t, err, ".mp4")
}

func openJobDB(t *testing.T, root string, id int64) *gorm.DB {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(root, "work", "jobs", strconv.FormatInt(id, 10), "state.db"))
	require.NoError(t, err)
	return db
}

func openReviewJobDB(t *testing.T, catalog *gorm.DB, root string, kind entity.JobKind, states ...entity.JobStatus) *gorm.DB {
	t.Helper()
	// Inspect active Bundles in descending creation order and close each rejected candidate.
	var jobs []struct{ ID int64 }
	require.NoError(t, catalog.Table("jobs").Where("deleted_at_ns = 0").Order("id DESC").Find(&jobs).Error)
	for _, job := range jobs {
		db := openJobDB(t, root, job.ID)
		record := new(executor.JobRecord)
		require.NoError(t, db.First(record, 1).Error)
		if record.Kind == kind && (len(states) == 0 || record.Status == states[0]) {
			return db
		}
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	}
	t.Fatalf("Demo has no %s Job", kind)
	return nil
}

func openReviewScanDB(t *testing.T, catalog *gorm.DB, root string, matches func(*entity.ScanJobSpec) bool, state entity.JobStatus) *gorm.DB {
	t.Helper()
	// Match only active Scan Bundles, releasing database handles for unrelated fixtures.
	var jobs []struct{ ID int64 }
	require.NoError(t, catalog.Table("jobs").
		Where("deleted_at_ns = 0 AND catalog_kind = ?", entity.JobKind_JOB_KIND_SCAN).Order("id DESC").Find(&jobs).Error)
	for _, job := range jobs {
		db := openJobDB(t, root, job.ID)
		var config scanjob.Config
		require.NoError(t, db.First(&config, 1).Error)
		var record executor.JobRecord
		require.NoError(t, db.First(&record, 1).Error)
		if record.Status == state && matches(config.Spec) {
			return db
		}
		sqlDB, err := db.DB()
		require.NoError(t, err)
		require.NoError(t, sqlDB.Close())
	}
	t.Fatal("Demo has no Scan with the requested policy and state")
	return nil
}

func requireTableCount(t *testing.T, db *gorm.DB, model any, want int64) {
	t.Helper()
	var count int64
	require.NoError(t, db.Model(model).Count(&count).Error)
	require.Equal(t, want, count)
}
