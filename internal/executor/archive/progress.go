package archive

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
)

func (a *jobArchiveRunner) getProgress() *executor.Progress {
	a.lock.Lock()
	defer a.lock.Unlock()
	return a.progressLocked()
}

func (a *jobArchiveRunner) progressLocked() *executor.Progress {
	if a.progress != nil {
		return a.progress
	}

	// Reconstruct frozen totals without exposing a partially initialized runtime counter.
	a.progress = executor.NewProgress()
	var totalFiles, totalBytes int64
	if err := a.db.Model(&Item{}).
		Select("count(id)", "coalesce(sum(size), 0)").
		Row().Scan(&totalFiles, &totalBytes); err != nil {
		a.logger.WithError(err).Error("query archive total progress failed")
	}
	a.progress.SetGlobalTotal(totalBytes, totalFiles)

	// Only published Media copies seed durable completed work after reopening or retry.
	var copiedFiles, copiedBytes int64
	if err := a.db.Model(&Item{}).
		Select("count(id)", "coalesce(sum(size), 0)").
		Where("status = ?", entity.CopyStatus_COPY_STATUS_SUBMITTED).
		Row().Scan(&copiedFiles, &copiedBytes); err != nil {
		a.logger.WithError(err).Error("query archive copied progress failed")
	}
	a.progress.SetGlobalCopied(copiedBytes, copiedFiles)
	return a.progress
}

func (a *jobArchiveRunner) dropProgress() {
	a.lock.Lock()
	a.progress = nil
	a.lock.Unlock()
}

func (a *jobArchiveRunner) progressSnapshot() *entity.Progress {
	// Counter initialization and phase observation share the runner state lock.
	a.lock.Lock()
	defer a.lock.Unlock()
	progress := a.progressLocked()
	return progress.StageReport(progress.CopyStageInput(a.phaseLocked(), a.stageKey))
}

// sampleStage records one copy work sample for the phase this attempt is in. The ACP work path
// calls it next to the counter update, so a progress request only reads the recorded window and a
// hidden Job card can no longer freeze the estimate or change it with its polling cadence.
func (a *jobArchiveRunner) sampleStage(phase entity.JobPhase) {
	progress := a.getProgress()
	progress.ObserveStage(progress.CopyStageInput(phase, a.stageKey))
}
