package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	scanjob "github.com/samuelncui/yatm/internal/executor/scan"
	"github.com/samuelncui/yatm/internal/library"
)

func seedLocations(
	ctx context.Context,
	lib *library.Library,
	exe *executor.Executor,
	root string,
	archived []fixtureFile,
	volume *seededVolume,
) (*library.Location, *library.Location, error) {
	// Analyze discovers handbook's saved version from existing coverage without another physical copy.
	files := []fixtureFile{
		{path: "original-only.txt", content: []byte("Original without an archival copy.\n")},
		{path: "mutable.txt", content: []byte("First observed revision.\n")},
		{path: ".notes/readme.txt", content: []byte("Ordinary dot directories are indexed.\n")},
		{path: "downloads/active.part", content: []byte("Excluded incomplete download.\n")},
	}
	for _, file := range archived {
		if file.path == "featured/documents/team-handbook.md" {
			files = append(files, fixtureFile{path: "handbook.md", content: file.content})
		}
	}
	daily, err := seedLocation(ctx, exe, "Documents", "Documents", files, []string{"/downloads/"})
	if err != nil {
		return nil, nil, err
	}
	if err := seedAnalyze(ctx, exe, daily.ID, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return nil, nil, err
	}

	// Annotations belong to the ongoing File while its observed content changes.
	old, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Documents/mutable.txt")
	if err != nil {
		return nil, nil, err
	}
	old.Note = "Project meeting notes."
	if err := lib.SaveFile(ctx, old); err != nil {
		return nil, nil, err
	}

	// Retain three distinct, physically present versions of the same organized File.
	history := []fixtureFile{
		{path: "original-history/mutable.txt", content: files[1].content},
		{path: "original-history/mutable-2.txt", content: []byte("Second revision: include the team's review comments.\n")},
		{
			path:    "original-history/mutable-3.txt",
			content: []byte("Third revision: approved notes with the final checklist and follow-up actions.\n"),
		},
	}
	for index, revision := range history {
		if index > 0 {
			if err := os.WriteFile(filepath.Join(daily.RootPath, "mutable.txt"), revision.content, 0644); err != nil {
				return nil, nil, err
			}
			if err := seedAnalyze(ctx, exe, daily.ID, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
				return nil, nil, err
			}
		}
		current, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Documents/mutable.txt")
		if err != nil {
			return nil, nil, err
		}

		// Save the observed revision's bytes before another revision replaces its original.
		saved, err := writeFixtureFiles(volume.root, []fixtureFile{revision})
		if err != nil {
			return nil, nil, err
		}
		mtimeNS, err := dataformat.Nanoseconds(current.ModTime)
		if err != nil {
			return nil, nil, fmt.Errorf("convert Demo history mtime failed, %w", err)
		}
		saved[0].Expected = &entity.ExpectedFile{FileId: current.ID, Signature: current.Signature, Sha256: current.Hash,
			SizeBytes: current.Size, Mode: uint32(current.Mode), MtimeNs: mtimeNS}
		if _, err := lib.CommitMedia(ctx, volume.media, mediaFileSource(saved)); err != nil {
			return nil, nil, err
		}
	}

	// Leave a fourth content state unarchived so reviewers can save and restore independently.
	if err := os.WriteFile(
		filepath.Join(daily.RootPath, "mutable.txt"),
		[]byte("Updated original content; the same File now has unarchived changes.\n"), 0644,
	); err != nil {
		return nil, nil, err
	}
	if err := seedAnalyze(ctx, exe, daily.ID, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return nil, nil, err
	}
	setup, err := lib.GetLocation(ctx, daily.ID)
	if err != nil {
		return nil, nil, err
	}

	// An unavailable source retains its Library records without offering cached physical browsing.
	offline, err := seedLocation(ctx, exe, "Unavailable originals", "Unavailable", []fixtureFile{
		{path: "retained.txt", content: []byte("Indexed before this directory became unavailable.\n")},
		{path: "copy-of-original-only.txt", content: files[0].content},
	}, nil)
	if err != nil {
		return nil, nil, err
	}
	if err := seedAnalyze(ctx, exe, offline.ID, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return nil, nil, err
	}

	// A missing selected file fails the whole Location without publishing its readable sibling, and
	// reconnecting it does not revive that Job: a new Job is what scans the selection again.
	if _, err := writeFixtureFiles(daily.RootPath, []fixtureFile{
		{path: "ready.txt", content: []byte("Scan collects this file after all selected files are available.\n")},
		{path: "reconnected.txt", content: []byte("Create a new Scan after this selected file has been reconnected.\n")},
	}); err != nil {
		return nil, nil, err
	}
	parked := filepath.Join(root, "offline-shelf", "failed-originals")
	if err := os.Rename(filepath.Join(daily.RootPath, "reconnected.txt"), parked); err != nil {
		return nil, nil, err
	}
	if err := seedAnalyzeSpec(ctx, exe, &entity.ScanJobSpec{
		Selections:   demoLocationSelections(daily.ID, "ready.txt", "reconnected.txt"),
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS,
	}, entity.JobStatus_JOB_STATUS_FAILED); err != nil {
		return nil, nil, err
	}
	if err := os.Rename(parked, filepath.Join(daily.RootPath, "reconnected.txt")); err != nil {
		return nil, nil, err
	}

	// Metadata-only observation is visible but makes no claim about content coverage.
	if _, err := writeFixtureFiles(daily.RootPath, []fixtureFile{
		{path: "notes.txt", content: []byte("Indexed without a content hash.\n")},
	}); err != nil {
		return nil, nil, err
	}
	if err := seedAnalyzeSpec(ctx, exe, &entity.ScanJobSpec{
		Selections:      demoLocationSelections(daily.ID, "notes.txt"),
		SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY,
		ResultPolicy:    entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS,
	}, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
		return nil, nil, err
	}

	// Remove superseded construction results only when neither Location link still needs them.
	current, err := lib.GetLocation(ctx, daily.ID)
	if err != nil {
		return nil, nil, err
	}
	if setup.LastJobID != current.LastJobID && setup.LastJobID != current.LastSyncJobID {
		if _, err := exe.DeleteJobs(ctx, false, setup.LastJobID); err != nil {
			return nil, nil, err
		}
	}

	// A shared review tag makes distinct coverage and access states discoverable through ordinary search.
	for _, path := range []string{
		"Unforged/Documents/mutable.txt",
		"Unforged/Documents/original-only.txt",
		"Unforged/Documents/handbook.md",
		"Unforged/Unavailable originals/retained.txt",
		"Unforged/Documents/notes.txt",
	} {
		file, err := lib.GetByPath(ctx, library.Root.ID, path)
		if err != nil {
			return nil, nil, fmt.Errorf("find Demo review file %q failed, %w", path, err)
		}
		if file == nil {
			return nil, nil, fmt.Errorf("Demo review file %q is missing", path)
		}
		if err := lib.EditFileMetadata(
			ctx, []int64{file.ID}, library.FileMetadataEdit{AddTags: []string{"archive-review"}},
		); err != nil {
			return nil, nil, fmt.Errorf("tag Demo review file %q failed, %w", path, err)
		}
	}

	// A newly arrived file and an empty directory can be browsed before any Library admission.
	if err := os.WriteFile(filepath.Join(daily.RootPath, "new-arrival.txt"),
		[]byte("Browse, annotate or Archive this file without Analyze.\n"), 0644); err != nil {
		return nil, nil, err
	}
	if err := os.Mkdir(filepath.Join(daily.RootPath, "Empty folder"), defaultPerm); err != nil {
		return nil, nil, err
	}
	return daily, offline, nil
}

func seedLocation(ctx context.Context, exe *executor.Executor, name, directory string, files []fixtureFile, exclusions []string) (*library.Location, error) {
	// Materialize only disposable fixture files and use normal canonical-root admission.
	root := filepath.Join(exe.Paths().Source, "Live Review", directory)
	if err := os.MkdirAll(root, defaultPerm); err != nil {
		return nil, err
	}
	if _, err := writeFixtureFiles(root, files); err != nil {
		return nil, err
	}
	canonical, err := exe.LocationRoot(root)
	if err != nil {
		return nil, err
	}
	source := &library.Location{Name: name, RootPath: canonical, ExecutorID: "local", Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: strings.Join(exclusions, "\n")}}}
	if err := exe.Lib().CreateLocation(ctx, source); err != nil {
		return nil, err
	}
	return source, nil
}

func seedAnalyze(ctx context.Context, exe *executor.Executor, sourceID int64, want entity.JobStatus) error {
	// Retain the latest complete setup Scan rather than every intermediate content revision.
	previous, err := exe.Lib().GetLocation(ctx, sourceID)
	if err != nil {
		return err
	}
	if err := seedAnalyzeSpec(ctx, exe, &entity.ScanJobSpec{
		Selections:   demoLocationSelections(sourceID),
		ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS, CompareLibrary: true,
	}, want); err != nil {
		return err
	}
	if previous.LastJobID == 0 {
		return nil
	}

	// Failed examples stay inspectable; only settled construction work is redundant.
	job, err := exe.GetJob(ctx, previous.LastJobID)
	if err != nil {
		return err
	}
	if job.Kind != entity.JobKind_JOB_KIND_SCAN || job.Status != entity.JobStatus_JOB_STATUS_COMPLETED {
		return nil
	}
	_, err = exe.DeleteJobs(ctx, false, job.ID)
	return err
}

func demoLocationSelections(id int64, paths ...string) []*entity.FileSelection {
	if len(paths) == 0 {
		paths = []string{""}
	}
	selections := make([]*entity.FileSelection, 0, len(paths))
	for _, path := range paths {
		selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: id, Path: path}}, Scope: entity.FileScope_FILE_SCOPE_ALL})
	}
	return selections
}

func seedAnalyzeSpec(ctx context.Context, exe *executor.Executor, spec *entity.ScanJobSpec, want entity.JobStatus) error {
	// Use the public Scan workflow for basic collection and content analysis alike.
	job, err := scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Priority: 12, Spec: spec})
	if err != nil {
		return fmt.Errorf("create Demo Analyze failed, %w", err)
	}
	return waitForStatus(ctx, exe, job.Job.Id, want)
}
