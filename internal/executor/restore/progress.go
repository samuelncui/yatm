package restore

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
)

func (a *jobRestoreRunner) getProgress() *executor.Progress {
	a.lock.Lock()
	defer a.lock.Unlock()
	return a.progressLocked()
}

func (a *jobRestoreRunner) progressLocked() *executor.Progress {
	if a.progress != nil {
		return a.progress
	}

	a.progress = executor.NewProgress()

	// Aggregate each version once even when it has multiple Media copies.
	totalQuery := a.db.Model(&File{}).Select("item_id, size")
	var totalFiles, totalBytes int64
	if err := a.db.Table("(?) AS files", totalQuery).
		Select("count(item_id)", "coalesce(sum(size), 0)").
		Row().Scan(&totalFiles, &totalBytes); err != nil {
		a.logger.WithError(err).Error("query restore total progress failed")
	}
	a.progress.SetGlobalTotal(totalBytes, totalFiles)

	// Completed alternatives share one status, so group them by version.
	copiedQuery := a.db.Model(&File{}).Select("item_id, size").Where("completed = ?", true)
	var copiedFiles, copiedBytes int64
	if err := a.db.Table("(?) AS files", copiedQuery).
		Select("count(item_id)", "coalesce(sum(size), 0)").
		Row().Scan(&copiedFiles, &copiedBytes); err != nil {
		a.logger.WithError(err).Error("query restore copied progress failed")
	}
	a.progress.SetGlobalCopied(copiedBytes, copiedFiles)
	return a.progress
}

func (a *jobRestoreRunner) dropProgress() {
	a.lock.Lock()
	a.progress = nil
	a.lock.Unlock()
}

func (a *jobRestoreRunner) progressSnapshot() *entity.Progress {
	// Counter initialization and phase observation share the runner state lock.
	a.lock.Lock()
	defer a.lock.Unlock()
	progress := a.progressLocked()
	return progress.StageReport(progress.CopyStageInput(a.phaseLocked(), a.stageKey))
}

// sampleStage records one copy work sample for the phase this attempt is in. The ACP work path
// calls it next to the counter update, so a progress request only reads the recorded window and a
// hidden Job card can no longer freeze the estimate or change it with its polling cadence.
func (a *jobRestoreRunner) sampleStage(phase entity.JobPhase) {
	progress := a.getProgress()
	progress.ObserveStage(progress.CopyStageInput(phase, a.stageKey))
}
