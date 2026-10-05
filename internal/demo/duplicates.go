package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	scanjob "github.com/samuelncui/yatm/internal/executor/scan"
	"github.com/samuelncui/yatm/internal/library"
)

const (
	defaultIdenticalFiles  = 207
	defaultLargeGroupFiles = 201
)

func seedDuplicateOriginals(
	ctx context.Context, lib *library.Library, exe *executor.Executor, volume *seededVolume, offline *library.Location,
	identicalFiles int,
) error {
	// Use one scanned Location for the three- and four-member content groups.
	groups := []struct {
		paths   []string
		content string
	}{
		{[]string{"notes/team.txt", "notes/team-copy.txt", "notes/team-final.txt"}, "Weekly team meeting notes.\n"},
		{
			[]string{"checklist.txt", "checklist-copy.txt", "checklist-backup.txt", "checklist-final.txt"},
			"Brand asset checklist.\n",
		},
	}
	var files []fixtureFile
	for _, group := range groups {
		for _, path := range group.paths {
			files = append(files, fixtureFile{path: path, content: []byte(group.content)})
		}
	}
	location, err := seedLocation(ctx, exe, "Shared files", "Shared",
		append(files, identicalPagingFiles(defaultLargeGroupFiles, 0, defaultIdenticalFiles)...), nil)
	if err != nil {
		return err
	}
	if err := seedAnalyze(ctx, exe, location.ID, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return err
	}

	// Tag all nine members, including the two-member group spanning an unavailable Location.
	paths := []string{
		"Unforged/Documents/original-only.txt",
		"Unforged/Unavailable originals/copy-of-original-only.txt",
	}
	for _, file := range files {
		paths = append(paths, "Unforged/Shared files/"+file.path)
	}
	ids := make([]int64, 0, len(paths))
	for _, path := range paths {
		file, err := lib.GetByPath(ctx, library.Root.ID, path)
		if err != nil {
			return fmt.Errorf("find Demo duplicate %q failed, %w", path, err)
		}
		if file == nil {
			return fmt.Errorf("Demo duplicate %q is missing", path)
		}
		ids = append(ids, file.ID)
	}
	if err := lib.EditFileMetadata(ctx, ids, library.FileMetadataEdit{AddTags: []string{"duplicate-review"}}); err != nil {
		return fmt.Errorf("tag Demo duplicates failed, %w", err)
	}

	// Reuse the same Locations for connected saved histories with different current content.
	if err := seedIdenticalHistory(ctx, lib, exe, volume, location, offline); err != nil {
		return err
	}

	// Expand only after history construction so the large fixture is scanned once.
	if identicalFiles == defaultIdenticalFiles {
		return nil
	}
	return seedScaledIdentical(ctx, exe, location.ID, identicalFiles)
}

func seedIdenticalHistory(
	ctx context.Context, lib *library.Library, exe *executor.Executor, volume *seededVolume, shared, offline *library.Location,
) error {
	// Saved evidence links A{x,y}, B{y,z}, and C{z} into one transitive Library group.
	seed := []struct {
		location         *library.Location
		path             string
		initial, current []byte
	}{
		{shared, "a.txt", []byte("history-y\n"), []byte("history-x: a longer current revision for size sorting.\n")},
		{shared, "b.txt", []byte("history-z\n"), []byte("history-y\n")},
		{offline, "c.txt", []byte("history-z\n"), []byte("history-z\n")},
	}
	for _, item := range seed {
		if _, err := writeFixtureFiles(item.location.RootPath, []fixtureFile{
			{path: item.path, content: item.initial},
		}); err != nil {
			return err
		}
	}
	for _, location := range []*library.Location{shared, offline} {
		if err := seedAnalyze(ctx, exe, location.ID, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
			return err
		}
	}

	// Save the three histories while retaining their ordinary Location identities.
	files := make([]*library.File, len(seed))
	for index, item := range seed {
		file, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/"+item.location.Name+"/"+item.path)
		if err != nil || file == nil {
			return fmt.Errorf("find Demo identical history %q failed, %w", item.path, err)
		}
		files[index] = file
		archived, err := writeFixtureFiles(volume.root, []fixtureFile{
			{path: filepath.Join("identical-history", item.path), content: item.initial},
		})
		if err != nil {
			return err
		}
		mtimeNS, err := dataformat.Nanoseconds(file.ModTime)
		if err != nil {
			return fmt.Errorf("convert Demo identical history mtime failed, %w", err)
		}
		archived[0].Expected = &entity.ExpectedFile{
			FileId: file.ID, Signature: file.Signature, Sha256: file.Hash,
			SizeBytes: file.Size, Mode: uint32(file.Mode), MtimeNs: mtimeNS,
		}
		if _, err := lib.CommitMedia(ctx, volume.media, mediaFileSource(archived)); err != nil {
			return fmt.Errorf("save Demo identical history %q failed, %w", item.path, err)
		}
	}

	// Different current sizes keep sorting reviewable; saved evidence still connects all three.
	for _, item := range seed[:2] {
		if err := os.WriteFile(filepath.Join(shared.RootPath, item.path), item.current, 0o644); err != nil {
			return err
		}
	}
	if err := seedAnalyze(ctx, exe, shared.ID, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return err
	}

	// One tag finds the whole transitive example through ordinary Library search.
	if err := lib.EditFileMetadata(ctx, []int64{files[0].ID, files[1].ID, files[2].ID}, library.FileMetadataEdit{
		AddTags: []string{"identical-review"},
	}); err != nil {
		return fmt.Errorf("tag Demo identical history failed, %w", err)
	}
	return nil
}

func identicalPagingFiles(large, start, end int) []fixtureFile {
	// Materialize only the requested batch; equal content keeps the large group connected.
	files := make([]fixtureFile, 0, end-start)
	content := []byte("Shared content for a group spanning several pages.\n")
	for index := start; index < min(end, large); index++ {
		name := fmt.Sprintf("member-%03d.txt", index)
		if index%80 == 0 {
			name = "." + name
		}
		files = append(files, fixtureFile{
			path: filepath.Join("Large group", name), content: content,
		})
	}

	// Independent groups keep collapse and arbitrary scrolling visible beyond the first page.
	for index := max(start, large); index < end; index++ {
		member := index - large
		files = append(files, fixtureFile{
			path:    fmt.Sprintf("Many groups/group-%02d-%c.txt", member/2, 'a'+member%2),
			content: []byte(fmt.Sprintf("Paged group %02d\n", member/2)),
		})
	}
	return files
}

func writeScaledIdenticalFiles(ctx context.Context, root string, total int) error {
	// Keep at least the original 201 members; split larger fixtures about evenly between one group and pairs.
	large := max(defaultLargeGroupFiles, total/2)
	large += (total - large) % 2

	// Extend both directories without rewriting the small fixture's files or timestamps.
	for _, added := range [][2]int{
		{defaultLargeGroupFiles, large},
		{large + defaultIdenticalFiles - defaultLargeGroupFiles, total},
	} {
		for start := added[0]; start < added[1]; {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("write Demo Identical files canceled, %w", err)
			}
			end := start + min(256, added[1]-start)
			if _, err := writeFixtureFiles(root, identicalPagingFiles(large, start, end)); err != nil {
				return err
			}
			start = end
		}
	}
	return nil
}

func seedScaledIdentical(ctx context.Context, exe *executor.Executor, locationID int64, total int) error {
	// Reuse the complete Shared files Location after the small examples have settled.
	location, err := exe.Lib().GetLocation(ctx, locationID)
	if err != nil {
		return err
	}
	if err := writeScaledIdenticalFiles(ctx, location.RootPath, total); err != nil {
		return err
	}

	// Normal Scan owns bounded enumeration, hashing and publication at every requested size.
	created, err := scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Priority: 12, Spec: &entity.ScanJobSpec{
		Selections:   demoLocationSelections(locationID),
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, CompareLibrary: true,
	}})
	if err != nil {
		return fmt.Errorf("create scaled Demo Scan failed, %w", err)
	}

	// Large Scans use caller cancellation rather than the small fixture's fixed setup deadline.
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for exe.IsRunning(created.Job.Id) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for scaled Demo Scan canceled, %w", ctx.Err())
		case <-ticker.C:
		}
	}
	if err := waitForStatus(ctx, exe, created.Job.Id, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return err
	}

	// The complete Scan replaces Shared files' setup Job and retains the same Location and Job counts.
	if _, err := exe.DeleteJobs(ctx, false, location.LastJobID); err != nil {
		return fmt.Errorf("remove superseded Demo Scan failed, %w", err)
	}
	return nil
}
