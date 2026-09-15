package restore

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

const maxQueryItems = 1000

func (a *jobRestoreRunner) queryMedia(ctx context.Context, param *entity.ListRestoreJobMediaRequest) (*entity.ListRestoreJobMediaReply, error) {
	type mediaCount struct {
		MediaID int64
		Total   int64
		Pending int64
	}

	// Resolve the public filter into the two valid Restore copy states.
	includePending := len(param.FilterStatus) == 0
	includeCompleted := len(param.FilterStatus) == 0
	for _, status := range param.FilterStatus {
		if status == entity.CopyStatus_PENDING {
			includePending = true
		}
		if status == entity.CopyStatus_COMPLETED {
			includeCompleted = true
		}
	}
	if !includePending && !includeCompleted {
		return &entity.ListRestoreJobMediaReply{}, nil
	}

	// Aggregate the flat copy table without joining a logical-item table.
	query := a.db.WithContext(ctx).Model(&Copy{}).
		Select("media_id, count(id) AS total, sum(CASE WHEN status = ? THEN 1 ELSE 0 END) AS pending", entity.CopyStatus_PENDING).
		Group("media_id").
		Order("media_id")
	if includePending && !includeCompleted {
		query = query.Having("sum(CASE WHEN status = ? THEN 1 ELSE 0 END) > 0", entity.CopyStatus_PENDING)
	}
	if includeCompleted && !includePending {
		query = query.Having("sum(CASE WHEN status = ? THEN 1 ELSE 0 END) = 0", entity.CopyStatus_PENDING)
	}
	if param.Offset != nil && *param.Offset > 0 {
		query = query.Offset(int(*param.Offset))
	}
	limit := int(param.Limit)
	if limit <= 0 {
		limit = 100
	}
	if limit > maxQueryItems {
		limit = maxQueryItems
	}
	query = query.Limit(limit + 1)

	// Hydrate only the selected Media page from the Library fact source.
	var counts []mediaCount
	if err := query.Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("query Restore Media failed, %w", err)
	}
	hasMore := len(counts) > limit
	if hasMore {
		counts = counts[:limit]
	}
	mediaIDs := make([]int64, 0, len(counts))
	for _, count := range counts {
		mediaIDs = append(mediaIDs, count.MediaID)
	}
	stored, err := a.exe.Lib().MGetMedia(ctx, mediaIDs...)
	if err != nil {
		return nil, fmt.Errorf("query Library Media failed, %w", err)
	}

	// Translate the aggregate into the bounded RPC response.
	results := make([]*entity.RestoreMedia, 0, len(counts))
	for _, count := range counts {
		media := stored[count.MediaID]
		if media == nil {
			return nil, fmt.Errorf("Restore Media is no longer in Library, media_id=%d; restore its catalog metadata before continuing", count.MediaID)
		}
		status := entity.CopyStatus_PENDING
		if count.Pending == 0 {
			status = entity.CopyStatus_COMPLETED
		}
		results = append(results, &entity.RestoreMedia{
			MediaId: media.ID, Identity: media.Identity, Total: count.Total, Status: status,
		})
	}
	return &entity.ListRestoreJobMediaReply{Media: results, HasMore: hasMore}, nil
}

func (a *jobRestoreRunner) queryFiles(ctx context.Context, param *entity.ListRestoreJobFilesRequest) (*entity.ListRestoreJobFilesReply, error) {
	// Apply the Media filter and bounded pagination directly to the flat table.
	query := a.db.WithContext(ctx).Where("media_id = ?", param.MediaId).Order("id")
	if len(param.FilterStatus) > 0 {
		query = query.Where("status IN ?", param.FilterStatus)
	}
	if param.Offset != nil {
		query = query.Offset(int(*param.Offset))
	}
	limit := int(param.Limit)
	if limit <= 0 {
		limit = 100
	}
	if limit > maxQueryItems {
		limit = maxQueryItems
	}
	var copies []*Copy
	if err := query.Limit(limit).Find(&copies).Error; err != nil {
		return nil, fmt.Errorf("query restore files failed, %w", err)
	}
	ids := make([]int64, 0, len(copies))
	for _, copy := range copies {
		ids = append(ids, copy.ItemID)
	}
	var outputs []Output
	if err := a.db.WithContext(ctx).Where("item_id IN ?", ids).Find(&outputs).Error; err != nil {
		return nil, fmt.Errorf("query Restore outcomes failed, %w", err)
	}
	byID := make(map[int64]Output, len(outputs))
	for _, output := range outputs {
		byID[output.ItemID] = output
	}

	// Translate storage rows into the public Media view.
	items := make([]*entity.RestoreItem, 0, len(copies))
	for _, copy := range copies {
		output := byID[copy.ItemID]
		items = append(items, &entity.RestoreItem{
			Id: copy.ID, Status: copy.Status, Size: copy.Size,
			ResultFileId: output.ResultFileID, Linked: output.Linked, Damaged: output.Damaged,
			ActualSha256: output.ActualHash, ActualSize: output.ActualSize, ResultMessage: output.ResultMessage,
			File: &entity.RestoreFile{
				FileId: copy.FileID, FileVersionId: copy.FileVersionID, Hash: copy.Hash, TargetPath: copy.TargetPath,
			},
			Candidate: &entity.RestoreCandidate{
				Id: copy.ID, MediaId: copy.MediaID, MediaPath: copy.MediaPath,
			},
		})
	}
	return &entity.ListRestoreJobFilesReply{Items: items}, nil
}

func (a *jobRestoreRunner) resultSummary(ctx context.Context) (*entity.RestoreSummary, error) {
	// Completed items remain authoritative for migrated legacy manifests without modern output provenance.
	items := a.db.Model(&Copy{}).Select("item_id").Where("status = ?", entity.CopyStatus_COMPLETED).Group("item_id")
	var counts struct {
		VerifiedFiles int64
		DamagedFiles  int64
		UnlinkedFiles int64
	}
	if err := a.db.WithContext(ctx).Table("(?) AS completed", items).Joins("LEFT JOIN outputs ON outputs.item_id = completed.item_id").
		Select("COALESCE(SUM(CASE WHEN COALESCE(damaged, ?) = ? THEN 1 ELSE 0 END), 0) AS verified_files, "+
			"COALESCE(SUM(CASE WHEN damaged = ? THEN 1 ELSE 0 END), 0) AS damaged_files, "+
			"COALESCE(SUM(CASE WHEN COALESCE(linked, ?) = ? THEN 1 ELSE 0 END), 0) AS unlinked_files", false, false, true, false, false).
		Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("query Restore result summary failed, %w", err)
	}

	// Pending candidates share completion state, but count only distinct restore items.
	var pending int64
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("status = ?", entity.CopyStatus_PENDING).
		Distinct("item_id").Count(&pending).Error; err != nil {
		return nil, fmt.Errorf("query pending Restore summary failed, %w", err)
	}
	return &entity.RestoreSummary{VerifiedFiles: counts.VerifiedFiles, DamagedFiles: counts.DamagedFiles,
		UnlinkedFiles: counts.UnlinkedFiles, PendingFiles: pending}, nil
}
