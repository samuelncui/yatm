package restore

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
)

func (a *jobRestoreRunner) getProgress() *executor.Progress {
	a.lock.Lock()
	defer a.lock.Unlock()
	if a.progress != nil {
		return a.progress
	}

	a.progress = executor.NewProgress()

	// Aggregate each version once even when it has multiple Media copies.
	totalQuery := a.db.Model(&Copy{}).Select("item_id, max(size) AS size").Group("item_id")
	var totalFiles, totalBytes int64
	if err := a.db.Table("(?) AS files", totalQuery).
		Select("count(item_id)", "coalesce(sum(size), 0)").
		Row().Scan(&totalFiles, &totalBytes); err != nil {
		a.logger.WithError(err).Error("query restore total progress failed")
	}
	a.progress.SetGlobalTotal(totalBytes, totalFiles)

	// Completed alternatives share one status, so group them by version.
	copiedQuery := a.db.Model(&Copy{}).Select("item_id, max(size) AS size").
		Where("status = ?", entity.CopyStatus_COMPLETED).Group("item_id")
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
