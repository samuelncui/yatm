package demo

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	archivejob "github.com/samuelncui/yatm/executor/archive"
	restorejob "github.com/samuelncui/yatm/executor/restore"
	scanjob "github.com/samuelncui/yatm/executor/scan"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

func seedJobs(ctx context.Context, exe *executor.Executor, volume *seededVolume) error {
	// New job inputs use registered directories, including an unscanned recommended destination.
	root, err := exe.OnlineRoot(filepath.Join(exe.Paths().Source, "Incoming Review"))
	if err != nil {
		return err
	}
	location := &library.Location{Name: "Incoming", RootPath: root, ExecutorID: "local"}
	if err := exe.Lib().CreateOnlineSource(ctx, location); err != nil {
		return err
	}
	if err := seedAnalyze(ctx, exe, location.ID, entity.JobStatus_COMPLETED); err != nil {
		return err
	}
	location, err = exe.Lib().GetOnlineSource(ctx, location.ID)
	if err != nil {
		return err
	}
	selections := []*entity.FileSelection{{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: location.ID, Revision: location.Revision}}, Scope: entity.FileScope_FILE_SCOPE_ALL}}
	targetRoot, err := exe.OnlineRoot(exe.Paths().Target)
	if err != nil {
		return err
	}
	target := &library.Location{Name: "Restored files", RootPath: targetRoot, ExecutorID: "local", RestoreTarget: true}
	if err := exe.Lib().CreateOnlineSource(ctx, target); err != nil {
		return err
	}

	// Create a pending Archive whose source files can be written to either mounted Volume.
	archive, err := exe.CreateJob(ctx, entity.JobKind_ARCHIVE, 30, func(db *gorm.DB) error {
		if err := db.AutoMigrate(&archivejob.Config{}, &archivejob.Item{}); err != nil {
			return fmt.Errorf("create Demo Archive schema failed, %w", err)
		}
		config := &archivejob.Config{ID: 1, Spec: &entity.ArchiveJobSpec{Selections: selections}}
		if err := db.Create(config).Error; err != nil {
			return fmt.Errorf("create Demo Archive config failed, %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("create Demo Archive Job failed, %w", err)
	}
	// Admission is a Location mutation; finish this preparation before another Job observes that Location.
	if err := waitForStatus(ctx, exe, archive.ID, entity.JobStatus_PENDING); err != nil {
		return err
	}

	// Create a pending Restore with three online Volume-backed Files.
	restoreIDs := []int64{
		volume.files["featured/documents/team-handbook.md"],
		volume.files["featured/projects/aurora/budget.csv"],
		volume.files["featured/research/query-examples.txt"],
	}
	versionIDs := make([]int64, 0, len(restoreIDs))
	for _, fileID := range restoreIDs {
		versions, _, err := exe.Lib().ListFileVersions(ctx, fileID, 0, 2)
		if err != nil {
			return err
		}
		if len(versions) != 1 {
			return fmt.Errorf("Demo Restore requires exactly one version, file_id=%d", fileID)
		}
		versionIDs = append(versionIDs, versions[0].ID)
	}
	restore, err := restorejob.Create(ctx, exe, 20, &entity.RestoreJobSpec{FileVersionIds: versionIDs,
		Destination: &entity.RestoreDestination{LocationId: target.ID}})
	if err != nil {
		return fmt.Errorf("create Demo Restore Job failed, %w", err)
	}

	// A completed automatic inventory scan retains its inspectable differences.
	scan, err := scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Priority: 10,
		Spec: &entity.ScanJobSpec{MediaId: volume.media.ID, ResultPolicy: entity.ScanResultPolicy_PUBLISH_INVENTORY}})
	if err != nil {
		return fmt.Errorf("create Demo Scan Job failed, %w", err)
	}

	// Complete one Preview Job so the Jobs page and Library inspector expose real Preview output.
	preview, err := scanjob.Create(ctx, exe, &entity.CreateScanJobRequest{Priority: 15, Spec: &entity.ScanJobSpec{
		Selections: selections, PreviewPolicy: entity.PreviewPolicy_PREVIEW_REGENERATE_ALL,
	}})
	if err != nil {
		return fmt.Errorf("create Demo Preview Job failed, %w", err)
	}

	// Wait for every asynchronous index to reach its stable, operable phase.
	jobs := []struct {
		id     int64
		status entity.JobStatus
	}{
		{id: archive.ID, status: entity.JobStatus_PENDING},
		{id: restore.ID, status: entity.JobStatus_PENDING},
		{id: scan.Job.Id, status: entity.JobStatus_COMPLETED},
		{id: preview.Job.Id, status: entity.JobStatus_COMPLETED},
	}
	for _, item := range jobs {
		if err := waitForStatus(ctx, exe, item.id, item.status); err != nil {
			return err
		}
	}
	return seedRecoveryAndIntegrity(ctx, exe, volume, target.ID, versionIDs[2])
}

func waitForStatus(ctx context.Context, exe *executor.Executor, id int64, status entity.JobStatus) error {
	// Bound fixture creation so a runner failure cannot leave setup hanging.
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for Demo Job indexing canceled, id=%d, %w", id, ctx.Err())
		case <-deadline.C:
			return fmt.Errorf("wait for Demo Job indexing failed, id=%d, deadline exceeded", id)
		case <-ticker.C:
			if exe.IsRunning(id) {
				continue
			}
			job, err := exe.GetJob(ctx, id)
			if err != nil {
				return fmt.Errorf("read indexed Demo Job failed, id=%d, %w", id, err)
			}
			if job.Status != status {
				return fmt.Errorf(
					"Demo Job indexing failed, id=%d status=%s want=%s phase=%s",
					id, job.Status, status, job.Phase,
				)
			}
			return nil
		}
	}
}
