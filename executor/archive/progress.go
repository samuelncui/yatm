package archive

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
)

func (a *jobArchiveRunner) getProgress() *executor.Progress {
	a.lock.Lock()
	defer a.lock.Unlock()
	if a.progress != nil {
		return a.progress
	}

	a.progress = executor.NewProgress()
	var totalFiles, totalBytes int64
	if err := a.db.Model(&Item{}).
		Select("count(id)", "coalesce(sum(size), 0)").
		Row().Scan(&totalFiles, &totalBytes); err != nil {
		a.logger.WithError(err).Error("query archive total progress failed")
	}
	a.progress.SetGlobalTotal(totalBytes, totalFiles)

	var copiedFiles, copiedBytes int64
	if err := a.db.Model(&Item{}).
		Select("count(id)", "coalesce(sum(size), 0)").
		Where("status = ?", entity.CopyStatus_SUBMITTED).
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
