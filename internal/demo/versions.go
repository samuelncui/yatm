package demo

import (
	"context"
	"fmt"
	"image/color"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/executor"
	scanjob "github.com/samuelncui/yatm/internal/executor/scan"
	"github.com/samuelncui/yatm/internal/library"
)

func seedImageVersions(
	ctx context.Context, lib *library.Library, exe *executor.Executor, volume *seededVolume, location *library.Location,
) error {
	// Extend the existing blue image's history without allocating a new File or changing its annotations.
	fileID := volume.files["featured/photos/archive-room.png"]
	colors := []color.RGBA{{R: 145, G: 65, B: 25, A: 255}, {R: 26, G: 112, B: 77, A: 255}}
	root := location.RootPath
	var previousJobID int64
	for index, base := range colors {
		// Publish this revision's original facts before generating its Preview.
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
		mtimeNS, err := dataformat.Nanoseconds(original.ModTime)
		if err != nil {
			return fmt.Errorf("convert Demo original mtime failed, %w", err)
		}
		_, err = lib.AdmitObservation(ctx, location.ID, &library.ObservedEntry{
			FileID: fileID, Path: original.Path, Size: original.Size, Mode: uint32(original.Mode),
			MtimeNS: mtimeNS, Hash: original.Hash, Signature: signature,
		})
		if err != nil {
			return err
		}

		// Generate each revision from its real source before the next edit replaces that source.
		job, err := scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Priority: 15, Spec: &entity.ScanJobSpec{
			Selections: []*entity.FileSelection{{Target: &entity.FileSelection_Library{
				Library: &entity.LibrarySelection{FileId: fileID},
			}}},
			SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ,
			PreviewPolicy:   entity.PreviewPolicy_PREVIEW_POLICY_MISSING_ONLY,
		}})
		if err != nil {
			return fmt.Errorf("create Demo image version Preview failed, %w", err)
		}
		if err := waitForStatus(ctx, exe, job.Job.Id, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
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
		mtimeNS, err = dataformat.Nanoseconds(copy.ModTime)
		if err != nil {
			return fmt.Errorf("convert Demo copy mtime failed, %w", err)
		}
		copy.Expected = &entity.ExpectedFile{FileId: fileID, Signature: signature, Sha256: copy.Hash,
			SizeBytes: copy.Size, Mode: uint32(copy.Mode), MtimeNs: mtimeNS}
		if _, err := lib.CommitMedia(ctx, volume.media, mediaFileSource(copies)); err != nil {
			return fmt.Errorf("publish Demo image version failed, %w", err)
		}

		// Versions and assets remain after the earlier generation Job becomes redundant.
		if previousJobID != 0 {
			if _, err := exe.DeleteJobs(ctx, false, previousJobID); err != nil {
				return err
			}
		}
		previousJobID = job.Job.Id
	}
	return nil
}
