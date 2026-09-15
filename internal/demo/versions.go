package demo

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	scanjob "github.com/samuelncui/yatm/executor/scan"
	"github.com/samuelncui/yatm/library"
)

func seedImageVersions(ctx context.Context, lib *library.Library, exe *executor.Executor, volume *seededVolume) error {
	// Extend the existing blue image's history without allocating a new File or changing its annotations.
	fileID := volume.files["featured/photos/archive-room.png"]
	colors := []color.RGBA{{R: 145, G: 65, B: 25, A: 255}, {R: 26, G: 112, B: 77, A: 255}}
	root := filepath.Join(exe.Paths().Source, "Image Versions")
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	root, err := exe.OnlineRoot(root)
	if err != nil {
		return err
	}
	location := &library.Location{Name: "Photos", RootPath: root, ExecutorID: "local"}
	if err := lib.CreateOnlineSource(ctx, location); err != nil {
		return err
	}
	for index, base := range colors {
		content, err := demoPreviewImageWithColor(base)
		if err != nil {
			return err
		}
		originals, err := writeFixtureFiles(root, []fixtureFile{{path: "archive-room.png", content: content}})
		if err != nil {
			return err
		}
		original := originals[0]
		signature, err := library.NewFileSignature(original.Hash, original.Size)
		if err != nil {
			return err
		}
		_, err = lib.AdmitObservation(ctx, location.ID, location.BindingToken, &library.OnlinePosition{
			FileID: fileID, Path: original.Path, Size: original.Size, Mode: uint32(original.Mode), MtimeNS: original.ModTime.UnixNano(), Hash: original.Hash, Signature: signature,
		})
		if err != nil {
			return err
		}

		// Generate each revision from its real source before the next edit replaces that source.
		job, err := scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Priority: 15, Spec: &entity.ScanJobSpec{
			Selections:      []*entity.FileSelection{{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: fileID}}}},
			SignaturePolicy: entity.ScanSignaturePolicy_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS, PreviewPolicy: entity.PreviewPolicy_PREVIEW_MISSING_ONLY,
		}})
		if err != nil {
			return fmt.Errorf("create Demo image version Preview failed, %w", err)
		}
		if err := waitForStatus(ctx, exe, job.Job.Id, entity.JobStatus_COMPLETED); err != nil {
			return err
		}

		// Preserve each image and publish its version through the verified archive boundary.
		copies, err := writeFixtureFiles(volume.root, []fixtureFile{{
			path: fmt.Sprintf("image-history/archive-room-v%d.png", index+2), content: content,
		}})
		if err != nil {
			return err
		}
		copy := copies[0]
		signature, err = library.NewFileSignature(copy.Hash, copy.Size)
		if err != nil {
			return err
		}
		copy.Expected = &entity.ExpectedFile{FileId: fileID, Signature: signature, Sha256: copy.Hash,
			Size: copy.Size, Mode: uint32(copy.Mode), MtimeNs: copy.ModTime.UnixNano()}
		if _, err := lib.CommitMedia(ctx, volume.media, mediaFileSource(copies)); err != nil {
			return fmt.Errorf("publish Demo image version failed, %w", err)
		}
	}
	return nil
}
