package demo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
)

const tebibyte = int64(1 << 40)

type fixtureFile struct {
	path    string
	content []byte
	note    string
	tags    []string
}

type seededVolume struct {
	root  string
	media *library.Media
	files map[string]int64
}

func seed(
	ctx context.Context,
	lib *library.Library,
	exe *executor.Executor,
	paths executor.Paths,
	root string,
	videoPath string,
	identicalFiles int,
) error {
	// Build image content and load the licensed video shared by the Library and Preview sources.
	previewImage, err := demoPreviewImage()
	if err != nil {
		return err
	}
	previewVideo, err := demoPreviewVideo(videoPath)
	if err != nil {
		return err
	}
	files := reviewFiles(previewImage, previewVideo)

	// Register mounted, unmounted, random-write, sequential-write, and sequential-read Media states.
	hdd, err := seedVolume(ctx, lib, paths.Volumes[0], "review-hdd", "Review HDD", &entity.VolumeMediaProfile{
		SerialNumber: "REVIEW-HDD-001", Type: entity.VolumeType_VOLUME_TYPE_HDD,
	}, files)
	if err != nil {
		return err
	}
	if _, err := seedVolume(ctx, lib, paths.Volumes[0], "review-hm-smr", "Review HM-SMR", &entity.VolumeMediaProfile{
		SerialNumber: "REVIEW-SMR-001", Type: entity.VolumeType_VOLUME_TYPE_HM_SMR,
	}, nil); err != nil {
		return err
	}
	if err := seedOfflineVolume(ctx, lib, paths.Volumes[0], filepath.Join(root, "offline-shelf")); err != nil {
		return err
	}

	// Leave one mounted disk without a marker so Add Volume can demonstrate initialization.
	if err := os.MkdirAll(filepath.Join(paths.Volumes[0], "review-new-disk"), defaultPerm); err != nil {
		return fmt.Errorf("create uninitialized Demo Volume failed, %w", err)
	}
	if err := seedTape(ctx, lib); err != nil {
		return err
	}

	// Materialize logical Files, annotations, and review-friendly top-level directories.
	if _, err := lib.Trim(ctx, true, true, false); err != nil {
		return fmt.Errorf("trim Demo Library failed, %w", err)
	}
	if err := organizeLibrary(ctx, lib, hdd, files); err != nil {
		return err
	}

	// Leave real filesystem drift and pending Jobs for reviewers to operate.
	if err := mutateVolumeForScan(hdd.root); err != nil {
		return err
	}
	if err := seedArchiveSources(paths.Source, previewImage, previewVideo); err != nil {
		return err
	}
	daily, offline, err := seedLocations(ctx, lib, exe, root, files, hdd)
	if err != nil {
		return err
	}
	if err := seedDuplicateOriginals(ctx, lib, exe, hdd, offline, identicalFiles); err != nil {
		return err
	}
	if err := seedImageVersions(ctx, lib, exe, hdd, daily); err != nil {
		return err
	}

	// Park one Location after its current and saved evidence is complete.
	if err := os.Rename(offline.RootPath, filepath.Join(root, "offline-shelf", "originals")); err != nil {
		return err
	}
	if err := seedJobs(ctx, exe, hdd); err != nil {
		return err
	}

	// Carry the video credit to Incoming's distinct File as well as its organized saved copy.
	saved, err := lib.GetByPath(ctx, library.Root.ID, "Photos/"+demoVideoName)
	if err != nil {
		return err
	}
	original, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/Incoming/Camera A/"+demoVideoName)
	if err != nil {
		return err
	}
	if saved == nil || original == nil {
		return fmt.Errorf("Demo video annotation source or destination is missing")
	}
	original.Note = saved.Note
	return lib.SaveFile(ctx, original)
}

func reviewFiles(previewImage, previewVideo []byte) []fixtureFile {
	// Credit the bundled excerpt through normal File annotations, including its derived images.
	videoNote := "User-supplied video with native poster and timeline Preview assets."
	if bytes.Equal(previewVideo, bundledVideo) {
		videoNote = demoVideoCredit
	}

	// Keep a small curated set for metadata and mixed-field search review.
	files := []fixtureFile{
		{
			path: "featured/projects/aurora/roadmap.md", content: []byte("# Aurora roadmap\n\nLaunch review in October.\n"),
			note: "Primary planning document for the Aurora launch.", tags: []string{"project", "priority", "review"},
		},
		{
			path: "featured/projects/aurora/budget.csv", content: []byte("category,amount\ntravel,12000\nhardware,48000\n"),
			note: "Draft budget; finance review is still pending.", tags: []string{"project", "finance", "confidential"},
		},
		{
			path: "featured/documents/vendor-contract.txt", content: []byte("Vendor agreement review copy\nRenewal: 2027-01-15\n"),
			note: "Legal review copy; not the signed original.", tags: []string{"contract", "legal", "review"},
		},
		{
			path: "featured/documents/team-handbook.md", content: []byte("# Team handbook\n\nPractical notes for the archive team.\n"),
			note: "Internal onboarding reference.", tags: []string{"documentation", "team"},
		},
		{
			path: "featured/photos/location-notes.txt", content: []byte("Harbour warehouse\nAisle 4, shelf 12\n"),
			note: "Notes accompanying the site photo set.", tags: []string{"photo", "location"},
		},
		{
			path: "featured/photos/archive-room.png", content: append([]byte(nil), previewImage...),
			note: "Archive room layout.", tags: []string{"photo", "preview", "demo"},
		},
		{
			path: "featured/photos/" + demoVideoName, content: append([]byte(nil), previewVideo...),
			note: videoNote, tags: []string{"video", "preview", "demo"},
		},
		{
			path: "featured/research/retention-policy.txt", content: []byte("Retain project material for seven years.\n"),
			note: "Intentionally removed from the mock disk after indexing.", tags: []string{"policy", "research"},
		},
		{
			path: "featured/research/query-examples.txt", content: []byte("tag:project AND size:>100\nname:*.md OR note:review\n"),
			note: "Useful search examples for the frontend review.", tags: []string{"search", "demo"},
		},
	}

	// Exceed the public page size so pagination and incremental loading remain reviewable.
	for index := 1; index <= 101; index++ {
		files = append(files, fixtureFile{
			path:    fmt.Sprintf("records/2026/invoice-%03d.txt", index),
			content: []byte(fmt.Sprintf("Invoice %03d\nStatus: archived\nAmount: %d\n", index, 1000+index*37)),
			note:    "Imported finance record for pagination review.", tags: []string{"invoice", "finance", "2026"},
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files
}

func demoPreviewImage() ([]byte, error) {
	return demoPreviewImageWithColor(color.RGBA{R: 28, G: 72, B: 126, A: 255})
}

func demoPreviewImageWithColor(base color.RGBA) ([]byte, error) {
	// Render a deterministic review image without depending on external assets.
	canvas := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < canvas.Bounds().Dy(); y++ {
		for x := 0; x < canvas.Bounds().Dx(); x++ {
			canvas.SetRGBA(x, y, color.RGBA{
				R: base.R + uint8(x*52/canvas.Bounds().Dx()),
				G: base.G + uint8(y*58/canvas.Bounds().Dy()),
				B: base.B + uint8(x*48/canvas.Bounds().Dx()),
				A: 255,
			})
		}
	}
	draw.Draw(canvas, image.Rect(36, 38, 284, 58), &image.Uniform{C: color.RGBA{R: 236, G: 244, B: 255, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(54, 78, 266, 140), &image.Uniform{C: color.RGBA{R: 18, G: 42, B: 74, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(canvas, image.Rect(72, 94, 248, 106), &image.Uniform{C: color.RGBA{R: 77, G: 208, B: 190, A: 255}}, image.Point{}, draw.Src)

	// Encode the image exactly once for both physical copies.
	data := new(bytes.Buffer)
	if err := png.Encode(data, canvas); err != nil {
		return nil, fmt.Errorf("encode Demo Preview image failed, %w", err)
	}
	return data.Bytes(), nil
}

func demoPreviewVideo(path string) ([]byte, error) {
	// Read an explicit override for generation with the native worker.
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read Demo Preview video failed, path=%q, %w", path, err)
		}
		return data, nil
	}

	// Give fixture creation its own copy of the licensed embedded excerpt.
	return append([]byte(nil), bundledVideo...), nil
}

func seedVolume(
	ctx context.Context,
	lib *library.Library,
	volumesRoot, directory, name string,
	profile *entity.VolumeMediaProfile,
	files []fixtureFile,
) (*seededVolume, error) {
	// Initialize the same marker and Library identity used by a real mounted Volume.
	root := filepath.Join(volumesRoot, directory)
	if err := os.MkdirAll(root, defaultPerm); err != nil {
		return nil, fmt.Errorf("create Demo Volume root failed, path=%q, %w", root, err)
	}
	volume, err := mediapkg.InitializeVolume(root, profile)
	if err != nil {
		return nil, fmt.Errorf("initialize Demo Volume failed, name=%q, %w", name, err)
	}
	media := &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: name,
		Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS, CapacityBytes: 2 * tebibyte,
	}

	// Commit actual fixture files when the Volume is not intentionally empty.
	physical, err := writeFixtureFiles(root, files)
	if err != nil {
		return nil, err
	}

	// Publish known archive dates for ordinary examples; only legacy Tape inventory lacks them.
	for _, item := range physical {
		parent, err := lib.MkdirAll(ctx, library.Root.ID, path.Join("Unforged", name, path.Dir(item.Path)), defaultPerm)
		if err != nil {
			return nil, fmt.Errorf("create Demo archive parent failed, path=%q, %w", item.Path, err)
		}
		file := &library.File{ParentID: parent.ID, Name: path.Base(item.Path), Kind: entity.FileKind_FILE_KIND_REGULAR}
		if err := lib.SaveFile(ctx, file); err != nil {
			return nil, fmt.Errorf("create Demo archive File failed, path=%q, %w", item.Path, err)
		}
		signature, err := library.NewFileSignature(item.Hash, item.Size)
		if err != nil {
			return nil, err
		}
		mtimeNS, err := dataformat.Nanoseconds(item.ModTime)
		if err != nil {
			return nil, fmt.Errorf("convert Demo archive mtime failed, %w", err)
		}
		item.Expected = &entity.ExpectedFile{FileId: file.ID, Signature: signature, Sha256: item.Hash,
			SizeBytes: item.Size, Mode: uint32(item.Mode), MtimeNs: mtimeNS}
	}

	// Commit each version together with its physically present copy.
	if len(physical) == 0 {
		media, err = lib.CreateMedia(ctx, media)
	} else {
		media, err = lib.CommitMedia(ctx, media, mediaFileSource(physical))
	}
	if err != nil {
		return nil, fmt.Errorf("register Demo Volume failed, name=%q, %w", name, err)
	}
	return &seededVolume{root: root, media: media, files: make(map[string]int64, len(files))}, nil
}

func writeFixtureFiles(root string, files []fixtureFile) ([]*library.MediaFile, error) {
	// Write deterministic content and timestamps to the physical mock Volume.
	result := make([]*library.MediaFile, 0, len(files))
	baseTime := time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC)
	for index, item := range files {
		filename := filepath.Join(root, filepath.FromSlash(item.path))
		if err := os.MkdirAll(filepath.Dir(filename), defaultPerm); err != nil {
			return nil, fmt.Errorf("create Demo file directory failed, path=%q, %w", filename, err)
		}
		if err := os.WriteFile(filename, item.content, 0o644); err != nil {
			return nil, fmt.Errorf("write Demo file failed, path=%q, %w", filename, err)
		}
		mtime := baseTime.Add(time.Duration(index) * time.Minute)
		if err := os.Chtimes(filename, mtime, mtime); err != nil {
			return nil, fmt.Errorf("set Demo file time failed, path=%q, %w", filename, err)
		}
		info, err := os.Stat(filename)
		if err != nil {
			return nil, fmt.Errorf("stat Demo file failed, path=%q, %w", filename, err)
		}
		digest := sha256.Sum256(item.content)
		result = append(result, &library.MediaFile{
			Path: item.path, Size: info.Size(), Mode: info.Mode(), ModTime: info.ModTime(),
			WriteTime: mtime.Add(time.Minute), Hash: append([]byte(nil), digest[:]...),
		})
	}
	return result, nil
}

func mediaFileSource(files []*library.MediaFile) library.MediaFileSource {
	return func(ctx context.Context, yield func(*library.MediaFile) error) error {
		for _, file := range files {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("read Demo Media files canceled, %w", err)
			}
			if err := yield(file); err != nil {
				return fmt.Errorf("yield Demo Media file failed, path=%q, %w", file.Path, err)
			}
		}
		return nil
	}
}

func seedOfflineVolume(ctx context.Context, lib *library.Library, volumesRoot, shelfRoot string) error {
	// Register the Volume while mounted, then move it outside discovery roots.
	mountedRoot := filepath.Join(volumesRoot, "review-offline")
	if err := os.MkdirAll(mountedRoot, defaultPerm); err != nil {
		return fmt.Errorf("create offline Demo Volume failed, %w", err)
	}
	volume, err := mediapkg.InitializeVolume(mountedRoot, &entity.VolumeMediaProfile{
		SerialNumber: "REVIEW-OFFLINE-001", Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	if err != nil {
		return fmt.Errorf("initialize offline Demo Volume failed, %w", err)
	}
	if _, err := lib.CreateMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "Offsite Shelf 01",
		Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS, CapacityBytes: 4 * tebibyte,
	}); err != nil {
		return fmt.Errorf("register offline Demo Volume failed, %w", err)
	}
	if err := os.Rename(mountedRoot, filepath.Join(shelfRoot, "review-offline")); err != nil {
		return fmt.Errorf("unmount offline Demo Volume failed, %w", err)
	}
	return nil
}

func seedTape(ctx context.Context, lib *library.Library) error {
	// Register LTFS v0 metadata so Tape remains inspectable without emulating LTFS I/O.
	when := time.Date(2025, time.December, 12, 14, 30, 0, 0, time.UTC)
	createdAtNS, err := dataformat.Nanoseconds(when)
	if err != nil {
		return fmt.Errorf("convert Demo Tape creation time failed, %w", err)
	}
	files := []fixtureFile{
		{path: "legacy/board-minutes-2024.txt", content: []byte("Board minutes archive copy\n")},
		{path: "legacy/project-ember-final.txt", content: []byte("Project Ember final delivery\n")},
	}
	physical := make([]*library.MediaFile, 0, len(files))
	for _, item := range files {
		digest := sha256.Sum256(item.content)
		physical = append(physical, &library.MediaFile{
			Path: item.path, Size: int64(len(item.content)), Mode: fs.FileMode(0o644),
			ModTime: when, WriteTime: when, Hash: append([]byte(nil), digest[:]...),
		})
	}
	tape, err := lib.CommitMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: TapeBarcode, Name: "Quarterly LTO-9 (mock)",
		Profile: (&entity.TapeMediaProfile{
			SerialNumber: "MOCK-LTO9-001", Encryption: "review-only", Format: library.TapeFormatLTFSV0,
		}).Pack(),
		CreatedAtNS: createdAtNS, CapacityBytes: 18 * tebibyte,
	}, mediaFileSource(physical))
	if err != nil {
		return fmt.Errorf("register Demo Tape failed, %w", err)
	}

	// Import only the two legacy copies without inventing their original archive dates.
	positions, err := lib.ListMediaFilePositions(ctx, tape.ID, "", len(files))
	if err != nil {
		return fmt.Errorf("read Demo Tape inventory failed, %w", err)
	}
	ids := make([]int64, 0, len(positions))
	for _, position := range positions {
		ids = append(ids, position.ID)
	}
	imported, err := lib.ImportArchivePositions(ctx, ids)
	if err != nil {
		return fmt.Errorf("import Demo Tape inventory failed, %w", err)
	}
	note := "Legacy archive inventory: the original archive date was not recorded. " +
		"The mock Tape supports metadata inspection only."
	if err := lib.EditFileMetadata(ctx, imported, library.FileMetadataEdit{Note: &note}); err != nil {
		return fmt.Errorf("annotate Demo legacy archive failed, %w", err)
	}
	return nil
}

func organizeLibrary(ctx context.Context, lib *library.Library, volume *seededVolume, files []fixtureFile) error {
	// Attach annotations to every physical fixture before moving logical directories.
	for _, item := range files {
		logicalPath := filepath.ToSlash(filepath.Join("Unforged", volume.media.Name, item.path))
		file, err := lib.GetByPath(ctx, library.Root.ID, logicalPath)
		if err != nil {
			return fmt.Errorf("read Demo File failed, path=%q, %w", logicalPath, err)
		}
		if file == nil {
			return fmt.Errorf("Demo File is missing, path=%q", logicalPath)
		}
		volume.files[item.path] = file.ID
		if err := lib.EditFileMetadata(ctx, []int64{file.ID}, library.FileMetadataEdit{
			AddTags: item.tags,
			Note:    &item.note,
		}); err != nil {
			return fmt.Errorf("annotate Demo File failed, path=%q, %w", logicalPath, err)
		}
	}

	// Expose curated directory names directly at the Library root.
	moves := []struct {
		from string
		name string
	}{
		{from: "featured/projects", name: "Projects"},
		{from: "featured/documents", name: "Documents"},
		{from: "featured/photos", name: "Photos"},
		{from: "featured/research", name: "Research"},
		{from: "records", name: "Records"},
	}
	for _, move := range moves {
		logicalPath := filepath.ToSlash(filepath.Join("Unforged", volume.media.Name, move.from))
		directory, err := lib.GetByPath(ctx, library.Root.ID, logicalPath)
		if err != nil {
			return fmt.Errorf("read Demo directory failed, path=%q, %w", logicalPath, err)
		}
		if directory == nil {
			return fmt.Errorf("Demo directory is missing, path=%q", logicalPath)
		}
		directory.ParentID = library.Root.ID
		directory.Name = move.name
		if err := lib.MoveFile(ctx, directory); err != nil {
			return fmt.Errorf("move Demo directory failed, path=%q, %w", logicalPath, err)
		}
	}

	// Make Tape and dot-directory behavior visible at the same navigation level.
	tapeDir, err := lib.GetByPath(ctx, library.Root.ID, "Unforged/"+TapeBarcode)
	if err != nil {
		return fmt.Errorf("read Demo Tape directory failed, %w", err)
	}
	if tapeDir == nil {
		return fmt.Errorf("Demo Tape directory is missing")
	}
	tapeDir.ParentID = library.Root.ID
	tapeDir.Name = "Tape Archive"
	if err := lib.MoveFile(ctx, tapeDir); err != nil {
		return fmt.Errorf("move Demo Tape directory failed, %w", err)
	}
	tapeNote := "Files represented by the inspectable mock LTO-9 tape."
	if err := lib.EditFileMetadata(ctx, []int64{tapeDir.ID}, library.FileMetadataEdit{
		AddTags: []string{"archive", "tape"},
		Note:    &tapeNote,
	}); err != nil {
		return fmt.Errorf("annotate Demo Tape directory failed, %w", err)
	}
	dotDirectory, err := lib.MkdirAll(ctx, library.Root.ID, ".review", defaultPerm)
	if err != nil {
		return fmt.Errorf("create Demo dot-directory failed, %w", err)
	}
	dotDirectoryNote := "Visible dot-directory fixture for browser behavior review."
	if err := lib.EditFileMetadata(ctx, []int64{dotDirectory.ID}, library.FileMetadataEdit{Note: &dotDirectoryNote}); err != nil {
		return fmt.Errorf("note Demo dot-directory failed, %w", err)
	}
	return nil
}

func mutateVolumeForScan(root string) error {
	// Add, change, and remove one physical file each for an actionable Scan Diff.
	added := filepath.Join(root, "manual-imports", "field-notes.txt")
	if err := os.MkdirAll(filepath.Dir(added), defaultPerm); err != nil {
		return fmt.Errorf("create Demo Scan addition failed, %w", err)
	}
	if err := os.WriteFile(added, []byte("Added outside YATM after the last Archive.\n"), 0o644); err != nil {
		return fmt.Errorf("write Demo Scan addition failed, %w", err)
	}
	changed := filepath.Join(root, "featured", "projects", "aurora", "roadmap.md")
	if err := os.WriteFile(changed, []byte("# Aurora roadmap\n\nLaunch review moved to November.\n"), 0o644); err != nil {
		return fmt.Errorf("write Demo Scan change failed, %w", err)
	}
	removed := filepath.Join(root, "featured", "research", "retention-policy.txt")
	if err := os.Remove(removed); err != nil {
		return fmt.Errorf("remove Demo Scan fixture failed, %w", err)
	}
	return nil
}

func seedArchiveSources(root string, previewImage, previewVideo []byte) error {
	// Populate a small nested source manifest for the pending Archive Job.
	files := map[string]string{
		"Incoming Review/Camera A/location.json":      "{\"location\":\"studio-a\"}\n",
		"Incoming Review/Deliverables/review-copy.md": "# Review copy\nReady for Archive.\n",
	}
	for name, content := range files {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), defaultPerm); err != nil {
			return fmt.Errorf("create Demo Archive source failed, path=%q, %w", filename, err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write Demo Archive source failed, path=%q, %w", filename, err)
		}
	}

	// Add supported image and video files for Scan's Preview generation.
	previewPath := filepath.Join(root, "Incoming Review", "Camera A", "contact-sheet.png")
	if err := os.WriteFile(previewPath, previewImage, 0o644); err != nil {
		return fmt.Errorf("write Demo Preview source failed, path=%q, %w", previewPath, err)
	}
	videoPath := filepath.Join(root, "Incoming Review", "Camera A", demoVideoName)
	if err := os.WriteFile(videoPath, previewVideo, 0o644); err != nil {
		return fmt.Errorf("write Demo Preview video failed, path=%q, %w", videoPath, err)
	}
	return nil
}
