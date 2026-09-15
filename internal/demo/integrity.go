package demo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	restorejob "github.com/samuelncui/yatm/executor/restore"
	scanjob "github.com/samuelncui/yatm/executor/scan"
)

func seedRecoveryAndIntegrity(ctx context.Context, exe *executor.Executor, volume *seededVolume, locationID, versionID int64) error {
	// An existing identical output exercises the ordinary Restore adoption/publication workflow.
	data, err := os.ReadFile(filepath.Join(volume.root, "featured/research/query-examples.txt"))
	if err != nil {
		return err
	}
	output := filepath.Join(exe.Paths().Target, "Recovered sample", "Research", "query-examples.txt")
	if err := os.MkdirAll(filepath.Dir(output), defaultPerm); err != nil {
		return err
	}
	if err := os.WriteFile(output, data, 0644); err != nil {
		return err
	}
	job, err := restorejob.Create(ctx, exe, 0, &entity.RestoreJobSpec{FileVersionIds: []int64{versionID},
		Destination: &entity.RestoreDestination{LocationId: locationID, Path: "Recovered sample"}})
	if err != nil {
		return fmt.Errorf("create Demo adopted Restore failed, %w", err)
	}
	if err := waitForStatus(ctx, exe, job.ID, entity.JobStatus_COMPLETED); err != nil {
		return err
	}

	// External drift leaves recoverable history intact and gives Verify real negative findings.
	if err := os.WriteFile(filepath.Join(volume.root, "featured/photos/location-notes.txt"), []byte("Damaged archive sample\n"), 0644); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(volume.root, "featured/documents/vendor-contract.txt")); err != nil {
		return err
	}
	check, err := scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{
		MediaId: volume.media.ID, ResultPolicy: entity.ScanResultPolicy_VERIFY_COPIES, SignaturePolicy: entity.ScanSignaturePolicy_FORCE_READ,
	}})
	if err != nil {
		return fmt.Errorf("create Demo integrity check failed, %w", err)
	}
	if err := waitForStatus(ctx, exe, check.Job.Id, entity.JobStatus_COMPLETED); err != nil {
		return err
	}

	// Keep a Tape-bound pending check for identity-constrained UI review without reading a device.
	tape, err := exe.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, TapeBarcode)
	if err != nil {
		return fmt.Errorf("find Demo Tape failed, %w", err)
	}
	check, err = scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{
		MediaId: tape.ID, ResultPolicy: entity.ScanResultPolicy_VERIFY_COPIES, SignaturePolicy: entity.ScanSignaturePolicy_FORCE_READ,
	}})
	if err != nil {
		return fmt.Errorf("create Demo Tape integrity check failed, %w", err)
	}
	return waitForStatus(ctx, exe, check.Job.Id, entity.JobStatus_PENDING)
}
