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
	"github.com/samuelncui/yatm/executor"
	archivejob "github.com/samuelncui/yatm/executor/archive"
	restorejob "github.com/samuelncui/yatm/executor/restore"
	scanjob "github.com/samuelncui/yatm/executor/scan"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	previewpkg "github.com/samuelncui/yatm/preview"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPrepareCreatesOperableReviewFixture(t *testing.T) {
	// Create the complete fixture in a disposable path accepted by the safety boundary.
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

	// Verify annotations and the large result set used for search pagination.
	db, err := resource.OpenSQLite(filepath.Join(root, databaseName))
	require.NoError(t, err)
	requireTableCount(t, db, library.ModelMedia, 4)
	requireTableCount(t, db, library.ModelFile, 181)
	requireTableCount(t, db, library.ModelFileTag, 416)
	requireTableCount(t, db, executor.ModelJob, 18)
	// The fixture contains only background workflows; ordinary file edits are request-bound.
	var unsupportedJobs int64
	require.NoError(t, db.Model(executor.ModelJob).Where("catalog_kind NOT IN ?", []entity.JobKind{
		entity.JobKind_ARCHIVE, entity.JobKind_RESTORE, entity.JobKind_SCAN,
	}).Count(&unsupportedJobs).Error)
	require.Zero(t, unsupportedJobs)
	requireTableCount(t, db, &library.Location{}, 9)
	lib := library.New(db)
	budget, err := lib.GetByPath(context.Background(), library.Root.ID, "Projects/aurora/budget.csv")
	require.NoError(t, err)
	require.Equal(t, "Draft budget; finance review is still pending.", budget.Note)
	tags, err := lib.MGetFileTags(context.Background(), budget.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"confidential", "finance", "project"}, tags[budget.ID])
	previewFile, err := lib.GetByPath(context.Background(), library.Root.ID, "Photos/archive-room.png")
	require.NoError(t, err)
	previews, err := previewpkg.New(previewpkg.Config{Root: filepath.Join(root, "previews")}, filepath.Join(root, "work"))
	require.NoError(t, err)
	manifest, err := previews.Manifest(previewFile.Signature)
	require.NoError(t, err)
	require.Equal(t, "thumbnail", manifest.Assets[0].Role)
	requireReviewImageVersions(t, lib, previews, root, previewFile.ID)
	videoFile, err := lib.GetByPath(context.Background(), library.Root.ID, "Photos/warehouse-walkthrough.mp4")
	require.NoError(t, err)
	videoManifest, err := previews.Manifest(videoFile.Signature)
	require.NoError(t, err)
	roles := make([]string, 0, len(videoManifest.Assets))
	for _, asset := range videoManifest.Assets {
		roles = append(roles, asset.Role)
	}
	require.Equal(t, []string{"poster", "timeline", "timeline-map"}, roles)
	videoSource, err := os.ReadFile(filepath.Join(
		root, "source", "Incoming Review", "Camera A", "warehouse-walkthrough.mp4",
	))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(videoSource), 8)
	require.Equal(t, []byte("ftyp"), videoSource[4:8])
	var invoices int64
	require.NoError(t, db.Model(library.ModelFileTag).Where("tag = ?", "invoice").Count(&invoices).Error)
	require.Equal(t, int64(125), invoices)

	// Keep both search pages available for testing refresh without collapsing the loaded window.
	firstPage, err := lib.SearchFiles(context.Background(), "tag:invoice", "", 100)
	require.NoError(t, err)
	require.Len(t, firstPage.Results, 100)
	require.NotEmpty(t, firstPage.NextCursor)
	secondPage, err := lib.SearchFiles(context.Background(), "tag:invoice", firstPage.NextCursor, 100)
	require.NoError(t, err)
	require.Len(t, secondPage.Results, 25)
	require.Empty(t, secondPage.NextCursor)

	// Verify every pending workflow contains actionable manifest rows.
	archiveDB := openReviewJobDB(t, db, root, entity.JobKind_ARCHIVE)
	requireTableCount(t, archiveDB, &archivejob.Item{}, 7)
	restoreDB := openReviewJobDB(t, db, root, entity.JobKind_RESTORE, entity.JobStatus_PENDING)
	var restoreFiles int64
	require.NoError(t, restoreDB.Model(&restorejob.Copy{}).Distinct("file_id").Count(&restoreFiles).Error)
	require.Equal(t, int64(3), restoreFiles)

	// Single-file recovery publishes a current binding without claiming a complete Location scan.
	var destination library.Location
	require.NoError(t, db.Where("name = ?", "Restored files").First(&destination).Error)
	require.True(t, destination.RestoreTarget)
	require.NotEmpty(t, destination.BindingToken)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, destination.Binding)
	require.Zero(t, destination.LastSyncAt)
	var recovered library.FileLocation
	require.NoError(t, db.Where("location_id = ?", destination.ID).First(&recovered).Error)
	require.True(t, recovered.CurrentBinding(&destination))
	require.Equal(t, "Recovered sample/Research/query-examples.txt", recovered.Path)
	requireTableCount(t, db, &library.RestoreResult{}, 1)

	// Real Verify findings make both retained bad history and checked healthy copies reviewable.
	verifyDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool { return spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES }, entity.JobStatus_COMPLETED)
	for _, finding := range []entity.ScanFinding{entity.ScanFinding_MISMATCH, entity.ScanFinding_MISSING} {
		var count int64
		require.NoError(t, verifyDB.Model(&scanjob.Entry{}).Where("finding = ?", finding).Count(&count).Error)
		require.EqualValues(t, 1, count)
	}
	var healthy int64
	require.NoError(t, db.Model(&library.Position{}).Where("health = ?", entity.PositionHealth_HEALTHY).Count(&healthy).Error)
	require.Positive(t, healthy)

	// The pending integrity check freezes only Tape inventory, never a freely chosen backend.
	tapeCheckDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool { return spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES }, entity.JobStatus_PENDING)
	var tapeCheck scanjob.Config
	require.NoError(t, tapeCheckDB.First(&tapeCheck).Error)
	require.Equal(t, TapeBarcode, tapeCheck.MediaIdentity)
	tape, err := lib.GetMedia(context.Background(), tapeCheck.Spec.MediaId)
	require.NoError(t, err)
	require.Equal(t, entity.MediaKind_MEDIA_KIND_TAPE, tape.Kind)
	requireTableCount(t, tapeCheckDB, &scanjob.Entry{}, 2)

	// Inventory results remain independent of content-integrity observations.
	scanDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool {
		return spec.ResultPolicy == entity.ScanResultPolicy_PUBLISH_INVENTORY
	}, entity.JobStatus_COMPLETED)
	scanRecord := new(executor.JobRecord)
	require.NoError(t, scanDB.First(scanRecord, 1).Error)
	require.Equal(t, entity.JobStatus_COMPLETED, scanRecord.Status)
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
	previewDB := openReviewScanDB(t, db, root, func(spec *entity.ScanJobSpec) bool { return spec.PreviewPolicy != entity.PreviewPolicy_PREVIEW_NONE }, entity.JobStatus_COMPLETED)
	var readyPreviews int64
	require.NoError(t, previewDB.Model(&scanjob.Entry{}).Where("preview = ?", entity.ScanPreviewOutcome_PREVIEW_READY).Count(&readyPreviews).Error)
	require.EqualValues(t, 2, readyPreviews)
	previewConfig := new(scanjob.Config)
	require.NoError(t, previewDB.First(previewConfig, 1).Error)
	require.NotNil(t, previewConfig.Spec)
	require.Equal(t, entity.PreviewPolicy_PREVIEW_REGENERATE_ALL, previewConfig.Spec.PreviewPolicy)
	previewRecord := new(executor.JobRecord)
	require.NoError(t, previewDB.First(previewRecord, 1).Error)
	require.Equal(t, entity.JobStatus_COMPLETED, previewRecord.Status)

	// Locations separate local confirmation, completed analysis and actual filesystem availability.
	var online []*library.Location
	require.NoError(t, db.Order("id").Find(&online).Error)
	require.Equal(t, entity.OnlineBinding_UNCONFIRMED, online[0].Binding)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, online[1].Binding)
	require.Positive(t, online[1].LastSyncAt)
	require.Equal(t, "Documents", online[1].Name)
	require.Positive(t, online[2].LastSyncAt)
	require.NoDirExists(t, online[2].RootPath)
	require.DirExists(t, online[3].RootPath)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, online[3].Binding)
	require.Positive(t, online[3].LastJobID)
	indexed, err := lib.OnlineFilesPage(context.Background(), online[1].ID, "", 100)
	require.NoError(t, err)
	require.Len(t, indexed, 4)
	require.FileExists(t, filepath.Join(online[1].RootPath, "new-arrival.txt"))
	require.DirExists(t, filepath.Join(online[1].RootPath, "Empty folder"))
	unadmitted, err := lib.GetByPath(context.Background(), library.Root.ID, "Unforged/Documents/new-arrival.txt")
	require.NoError(t, err)
	require.Nil(t, unadmitted)
	current, err := lib.GetByPath(context.Background(), library.Root.ID, "Unforged/Documents/mutable.txt")
	require.NoError(t, err)
	require.NotEmpty(t, current.Note)
	state, err := lib.FileState(context.Background(), current.ID)
	require.NoError(t, err)
	require.NotNil(t, state.Original)
	require.NotNil(t, state.LatestVersion)
	require.NotEqual(t, state.Original.Signature, state.LatestVersion.Signature)
	require.Equal(t, entity.ContentCoverage_NO_ARCHIVED_COPY, state.Coverage)
	requireDatedRestoreFixtures(t, lib, current.ID, previewFile.ID)
	extra, err := lib.GetByPath(context.Background(), library.Root.ID, "Unforged/Documents/mutable (1).txt")
	require.NoError(t, err)
	require.Nil(t, extra)
	unknown, err := lib.SearchFiles(context.Background(), "has:unknown", "", 100)
	require.NoError(t, err)
	require.Len(t, unknown.Results, 1)
	require.Equal(t, "notes.txt", unknown.Results[0].File.Name)
	page, err := lib.SearchFiles(context.Background(), "name:handbook.md AND has:online AND has:archive", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)
	require.Equal(t, "handbook.md", page.Results[0].File.Name)
	versions, _, err := lib.ListFileVersions(context.Background(), page.Results[0].File.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1, "known archived content is a saved version without another physical backup")
	require.Equal(t, page.Results[0].File.ID, versions[0].FileID)
	require.NotNil(t, versions[0].FirstArchivedAt)
	covered, _, err := lib.ListContentCopies(context.Background(), versions[0].Signature, 0, 10)
	require.NoError(t, err)
	require.Len(t, covered, 1)

	// The product review tag works through the same quoted simple query used by the frontend.
	review, err := lib.SearchFiles(
		context.Background(), `(name:"backup-review" OR tag:"backup-review" OR note:"backup-review")`, "", 100,
	)
	require.NoError(t, err)
	require.Len(t, review.Results, 5)
	needsBackup, err := lib.SearchFiles(
		context.Background(), `(tag:"backup-review") AND (has:online AND NOT has:unknown AND NOT has:archive)`, "", 100,
	)
	require.NoError(t, err)
	require.Len(t, needsBackup.Results, 3,
		"unavailable originals remain indexed and unknown content is not classified as missing backup")

	// Duplicate search compares published originals across Locations even when one root is unavailable.
	requireReviewDuplicates(t, lib, online[1].ID)
	require.NoError(t, lib.HydrateFileContent(
		context.Background(), current, page.Results[0].File, unknown.Results[0].File,
	))
	require.True(t, current.ContentSummary.HasVersions)
	require.Zero(t, current.ContentSummary.ArchivedCopies)
	require.Positive(t, page.Results[0].File.ContentSummary.ArchivedCopies)
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

	// Verify reuse preserves reviewer-created runtime files.
	reviewerFile := filepath.Join(root, "target", "reviewer-created.txt")
	require.NoError(t, os.WriteFile(reviewerFile, []byte("keep"), 0o644))
	require.NoError(t, Prepare(context.Background(), Options{Root: root, Listen: "127.0.0.1:18080"}))
	require.FileExists(t, reviewerFile)
}

func requireReviewDuplicates(t *testing.T, lib *library.Library, documentsID int64) {
	// Count complete content groups across ordinary small search pages, not within one page.
	t.Helper()
	ctx := context.Background()
	groups := make(map[string]int)
	ids := make(map[int64]struct{})
	cursor := ""
	for {
		page, err := lib.SearchFiles(ctx, "has:duplicates", cursor, 2)
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
	require.Equal(t, "online-only.txt", page.Results[0].File.Name)
	unbackedID := page.Results[0].File.ID

	// Global duplicates and an explicit Inspector selection remain actionable in saved-only Library views.
	settings, err := lib.GetLibrarySettings(ctx)
	require.NoError(t, err)
	hidden, err := lib.UpdateLibrarySettings(ctx, false, settings.Revision)
	require.NoError(t, err)
	selection, err := lib.InspectSelection(ctx, &entity.InspectSelectionRequest{Selections: []*entity.FileSelection{{
		Scope:  entity.FileScope_FILE_SCOPE_ALL,
		Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: unbackedID}},
	}}})
	require.NoError(t, err)
	require.EqualValues(t, 1, selection.Files)
	require.Zero(t, selection.MissingOriginals)

	// The grouped catalog exposes full counts and all members even with a single-Location filter.
	grouped, err := lib.ListDuplicateGroups(ctx, "", "", 20)
	require.NoError(t, err)
	var counts []int64
	for _, group := range grouped.Groups {
		counts = append(counts, group.OriginalCount)
		members, err := lib.ListDuplicateMembers(ctx, group.Signature, "", "", 20)
		require.NoError(t, err)
		require.Len(t, members.Members, int(group.OriginalCount))
		require.Equal(t, group, members.Group)
	}
	require.ElementsMatch(t, []int64{2, 3, 4}, counts)
	query := "location:" + strconv.FormatInt(documentsID, 10)
	grouped, err = lib.ListDuplicateGroups(ctx, query, "", 20)
	require.NoError(t, err)
	require.Len(t, grouped.Groups, 1)
	require.Equal(t, int64(2), grouped.Groups[0].OriginalCount)
	require.Equal(t, int64(1), grouped.Groups[0].MatchingCount)
	_, err = lib.UpdateLibrarySettings(ctx, settings.IncludeUnbackedFiles, hidden.Revision)
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
	require.NotNil(t, budgetVersions[0].FirstArchivedAt)
	require.NotNil(t, budgetVersions[0].LastArchivedAt)
	legacy, err := lib.GetByPath(ctx, library.Root.ID, "Tape Archive/legacy/board-minutes-2024.txt")
	require.NoError(t, err)
	require.Contains(t, legacy.Note, "original backup date was not recorded")
	legacyVersions, _, err := lib.ListFileVersions(ctx, legacy.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, legacyVersions, 1)
	require.Nil(t, legacyVersions[0].FirstArchivedAt)
	require.Nil(t, legacyVersions[0].LastArchivedAt)

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
		require.NotNil(t, version.FirstArchivedAt)
		require.Positive(t, *version.FirstArchivedAt)
		require.Equal(t, version.FirstArchivedAt, version.LastArchivedAt)
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
	// Point a temporary path at the repository and ensure reset cannot follow it.
	repository, err := os.Getwd()
	require.NoError(t, err)
	alias := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.Symlink(repository, alias))
	_, err = validateRoot(filepath.Join(alias, "yatm-demo-escape"))
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
	var jobs []*executor.Job
	require.NoError(t, catalog.Order("id DESC").Find(&jobs).Error)
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
	var jobs []*executor.Job
	require.NoError(t, catalog.Where("catalog_kind = ? AND catalog_status = ?", entity.JobKind_SCAN, state).Order("id DESC").Find(&jobs).Error)
	for _, job := range jobs {
		db := openJobDB(t, root, job.ID)
		var config scanjob.Config
		require.NoError(t, db.First(&config, 1).Error)
		if matches(config.Spec) {
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
