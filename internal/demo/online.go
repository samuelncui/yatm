package demo

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	scanjob "github.com/samuelncui/yatm/executor/scan"
	"github.com/samuelncui/yatm/library"
)

func seedOnlineSources(
	ctx context.Context,
	lib *library.Library,
	exe *executor.Executor,
	root string,
	archived []fixtureFile,
	volume *seededVolume,
) error {
	// Exercise local confirmation with a V5 round trip before any verified original bindings exist.
	if _, err := seedOnlineSource(ctx, exe, "Reference documents", "Imported", nil, nil); err != nil {
		return err
	}
	var backup bytes.Buffer
	if err := lib.Export(ctx, &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_FILE, entity.LibraryEntityType_LOCATION, entity.LibraryEntityType_FILE_LOCATION,
	}); err != nil {
		return err
	}
	if err := lib.Import(ctx, &backup); err != nil {
		return err
	}

	// Analyze discovers handbook's saved version from existing coverage without another physical backup.
	files := []fixtureFile{
		{path: "online-only.txt", content: []byte("Online original without an archival copy.\n")},
		{path: "mutable.txt", content: []byte("First observed revision.\n")},
		{path: ".notes/readme.txt", content: []byte("Ordinary dot directories are indexed.\n")},
		{path: "downloads/active.part", content: []byte("Excluded incomplete download.\n")},
	}
	for _, file := range archived {
		if file.path == "featured/documents/team-handbook.md" {
			files = append(files, fixtureFile{path: "handbook.md", content: file.content})
		}
	}
	daily, err := seedOnlineSource(ctx, exe, "Documents", "Documents", files, []string{"/downloads/"})
	if err != nil {
		return err
	}
	if err := seedAnalyze(ctx, exe, daily.ID, entity.JobStatus_COMPLETED); err != nil {
		return err
	}
	old, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Documents/mutable.txt")
	if err != nil {
		return err
	}
	old.Note = "Project meeting notes."
	if err := lib.SaveFile(ctx, old); err != nil {
		return err
	}

	// Retain three distinct, physically present versions of the same organized File.
	history := []fixtureFile{
		{path: "online-history/mutable.txt", content: files[1].content},
		{path: "online-history/mutable-2.txt", content: []byte("Second revision: include the team's review comments.\n")},
		{
			path:    "online-history/mutable-3.txt",
			content: []byte("Third revision: approved notes with the final checklist and follow-up actions.\n"),
		},
	}
	for index, revision := range history {
		if index > 0 {
			if err := os.WriteFile(filepath.Join(daily.RootPath, "mutable.txt"), revision.content, 0644); err != nil {
				return err
			}
			if err := seedAnalyze(ctx, exe, daily.ID, entity.JobStatus_COMPLETED); err != nil {
				return err
			}
		}
		current, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Documents/mutable.txt")
		if err != nil {
			return err
		}
		saved, err := writeFixtureFiles(volume.root, []fixtureFile{revision})
		if err != nil {
			return err
		}
		saved[0].Expected = &entity.ExpectedFile{FileId: current.ID, Signature: current.Signature, Sha256: current.Hash,
			Size: current.Size, Mode: uint32(current.Mode), MtimeNs: current.ModTime.UnixNano()}
		if _, err := lib.CommitMedia(ctx, volume.media, mediaFileSource(saved)); err != nil {
			return err
		}
	}

	// Leave a fourth content state unarchived so reviewers can save and restore independently.
	if err := os.WriteFile(
		filepath.Join(daily.RootPath, "mutable.txt"),
		[]byte("Updated original content; the same File now has unarchived changes.\n"), 0644,
	); err != nil {
		return err
	}
	if err := seedAnalyze(ctx, exe, daily.ID, entity.JobStatus_COMPLETED); err != nil {
		return err
	}

	// An unavailable source retains its Library records without offering cached physical browsing.
	offline, err := seedOnlineSource(ctx, exe, "Unavailable originals", "Unavailable", []fixtureFile{
		{path: "retained.txt", content: []byte("Indexed before this directory became unavailable.\n")},
		{path: "copy-of-online-only.txt", content: files[0].content},
	}, nil)
	if err != nil {
		return err
	}
	if err := seedAnalyze(ctx, exe, offline.ID, entity.JobStatus_COMPLETED); err != nil {
		return err
	}
	if err := os.Rename(offline.RootPath, filepath.Join(root, "offline-shelf", "online-originals")); err != nil {
		return err
	}

	// A failed attempt becomes immediately retryable after its real directory is reconnected.
	retry, err := seedOnlineSource(ctx, exe, "Analysis retry", "Retry", []fixtureFile{
		{path: "ready.txt", content: []byte("Retry analysis to collect this reconnected directory.\n")},
	}, nil)
	if err != nil {
		return err
	}
	parked := filepath.Join(root, "offline-shelf", "retry-originals")
	if err := os.Rename(retry.RootPath, parked); err != nil {
		return err
	}
	if err := seedAnalyze(ctx, exe, retry.ID, entity.JobStatus_INDEXING); err != nil {
		return err
	}
	if err := os.Rename(parked, retry.RootPath); err != nil {
		return err
	}

	// Metadata-only observation is visible but makes no claim about content coverage.
	unknown, err := seedOnlineSource(ctx, exe, "Content not checked", "Metadata", []fixtureFile{
		{path: "notes.txt", content: []byte("Indexed without a content hash.\n")},
	}, nil)
	if err != nil {
		return err
	}
	if err := seedAnalyzeSpec(
		ctx, exe, &entity.ScanJobSpec{LocationId: unknown.ID, SignaturePolicy: entity.ScanSignaturePolicy_KNOWN_ONLY, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS}, entity.JobStatus_COMPLETED,
	); err != nil {
		return err
	}

	// A shared review tag makes distinct coverage and access states discoverable through ordinary search.
	for _, path := range []string{
		"Unforged/Documents/mutable.txt",
		"Unforged/Documents/online-only.txt",
		"Unforged/Documents/handbook.md",
		"Unforged/Unavailable originals/retained.txt",
		"Unforged/Content not checked/notes.txt",
	} {
		file, err := lib.GetByPath(ctx, library.Root.ID, path)
		if err != nil {
			return fmt.Errorf("find Demo review file %q failed, %w", path, err)
		}
		if file == nil {
			return fmt.Errorf("Demo review file %q is missing", path)
		}
		if err := lib.EditFileMetadata(
			ctx, []int64{file.ID}, library.FileMetadataEdit{AddTags: []string{"backup-review"}},
		); err != nil {
			return fmt.Errorf("tag Demo review file %q failed, %w", path, err)
		}
	}
	// A newly arrived file and an empty directory can be browsed before any Library admission.
	if err := os.WriteFile(filepath.Join(daily.RootPath, "new-arrival.txt"), []byte("Browse, annotate or back up this file without Analyze.\n"), 0644); err != nil {
		return err
	}
	return os.Mkdir(filepath.Join(daily.RootPath, "Empty folder"), defaultPerm)
}

func seedOnlineSource(ctx context.Context, exe *executor.Executor, name, directory string, files []fixtureFile, exclusions []string) (*library.Location, error) {
	// Materialize only disposable fixture files and use normal canonical-root admission.
	root := filepath.Join(exe.Paths().Source, "Online Review", directory)
	if err := os.MkdirAll(root, defaultPerm); err != nil {
		return nil, err
	}
	if _, err := writeFixtureFiles(root, files); err != nil {
		return nil, err
	}
	canonical, err := exe.OnlineRoot(root)
	if err != nil {
		return nil, err
	}
	source := &library.Location{Name: name, RootPath: canonical, ExecutorID: "local", Exclusions: &entity.OnlineExclusions{Format: "gitignore", Text: strings.Join(exclusions, "\n")}}
	if err := exe.Lib().CreateOnlineSource(ctx, source); err != nil {
		return nil, err
	}
	return source, nil
}

func seedAnalyze(ctx context.Context, exe *executor.Executor, sourceID int64, want entity.JobStatus) error {
	return seedAnalyzeSpec(ctx, exe, &entity.ScanJobSpec{LocationId: sourceID, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, CompareLibrary: true}, want)
}

func seedAnalyzeSpec(ctx context.Context, exe *executor.Executor, spec *entity.ScanJobSpec, want entity.JobStatus) error {
	// Use the public Scan workflow for basic collection and content analysis alike.
	job, err := scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Priority: 12, Spec: spec})
	if err != nil {
		return fmt.Errorf("create Demo Analyze failed, %w", err)
	}
	return waitForStatus(ctx, exe, job.Job.Id, want)
}
