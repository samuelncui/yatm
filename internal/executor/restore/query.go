package restore

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func (a *jobRestoreRunner) queryMedia(ctx context.Context, param *entity.ListRestoreJobMediaRequest) (*entity.ListRestoreJobMediaResponse, error) {
	type mediaCount struct {
		MediaID int64
		Total   int64
		Pending int64
	}

	// Resolve one bounded page request shared by every Job manifest listing.
	page, err := executor.NewJobResultPage(param.Limit, param.Cursor, param.Order, param.Offset, param.IncludeTotal)
	if err != nil {
		return nil, err
	}
	after, hasCursor, err := executor.JobResultCursorID(page.Cursor)
	if err != nil {
		return nil, err
	}

	// Resolve the public filter into the two valid Restore copy states.
	includePending := len(param.FilterStatus) == 0
	includeCompleted := len(param.FilterStatus) == 0
	for _, status := range param.FilterStatus {
		if status == entity.CopyStatus_COPY_STATUS_PENDING {
			includePending = true
		}
		if status == entity.CopyStatus_COPY_STATUS_COMPLETED {
			includeCompleted = true
		}
	}
	if !includePending && !includeCompleted {
		return &entity.ListRestoreJobMediaResponse{}, nil
	}

	// Aggregate the flat copy table without joining a logical-item table.
	groups := func() *gorm.DB {
		query := a.db.WithContext(ctx).Model(&Copy{}).Group("media_id")
		if includePending && !includeCompleted {
			query = query.Having("sum(CASE WHEN status = ? THEN 1 ELSE 0 END) > 0", entity.CopyStatus_COPY_STATUS_PENDING)
		}
		if includeCompleted && !includePending {
			query = query.Having("sum(CASE WHEN status = ? THEN 1 ELSE 0 END) = 0", entity.CopyStatus_COPY_STATUS_PENDING)
		}
		return query
	}
	reply := &entity.ListRestoreJobMediaResponse{}
	if page.IncludeTotal {
		var total int64
		if err := a.db.WithContext(ctx).Table("(?) AS groups", groups().Select("media_id")).Count(&total).Error; err != nil {
			return nil, fmt.Errorf("count Restore Media failed, %w", err)
		}
		reply.TotalMediaCount = proto.Int64(total)
	}
	query := groups().Select("media_id, count(id) AS total, sum(CASE WHEN status = ? THEN 1 ELSE 0 END) AS pending", entity.CopyStatus_COPY_STATUS_PENDING)
	if hasCursor {
		query = query.Where("media_id "+page.Comparison()+" ?", after)
	}

	// Hydrate only the selected Media page from the Library fact source.
	var counts []mediaCount
	if err := query.Order("media_id " + page.Direction()).Limit(page.SentinelLimit()).Offset(int(page.Offset)).Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("query Restore Media failed, %w", err)
	}
	reply.HasMore = len(counts) > page.Limit
	if reply.HasMore {
		counts = counts[:page.Limit]
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
		status := entity.CopyStatus_COPY_STATUS_PENDING
		if count.Pending == 0 {
			status = entity.CopyStatus_COPY_STATUS_COMPLETED
		}
		results = append(results, &entity.RestoreMedia{
			MediaId: media.ID, Identity: media.Identity, FileCount: count.Total, Status: status,
		})
	}
	reply.Media = results
	return reply, nil
}

func (a *jobRestoreRunner) queryFiles(ctx context.Context, param *entity.ListRestoreJobFilesRequest) (*entity.ListRestoreJobFilesResponse, error) {
	// Resolve one bounded page request shared by every Job manifest listing.
	page, err := executor.NewJobResultPage(param.Limit, param.Cursor, param.Order, param.Offset, param.IncludeTotal)
	if err != nil {
		return nil, err
	}
	mediaID, itemID, hasCursor, err := executor.JobResultCursorPair(page.Cursor)
	if err != nil {
		return nil, err
	}

	// Apply the optional Media filter and bounded pagination to the flat table.
	filtered := func() *gorm.DB {
		query := a.db.WithContext(ctx).Model(&Copy{})
		if param.MediaId != nil {
			query = query.Where("media_id = ?", *param.MediaId)
		}
		if len(param.FilterStatus) > 0 {
			query = query.Where("status IN ?", param.FilterStatus)
		}
		return query
	}
	reply := &entity.ListRestoreJobFilesResponse{}
	if page.IncludeTotal {
		var total int64
		if err := filtered().Count(&total).Error; err != nil {
			return nil, fmt.Errorf("count restore files failed, %w", err)
		}
		reply.TotalFileCount = proto.Int64(total)
	}

	// One composite (media_id, id) key spans every Media in a single sequence.
	query := filtered()
	if hasCursor {
		if page.Descending() {
			query = query.Where("media_id < ? OR (media_id = ? AND id < ?)", mediaID, mediaID, itemID)
		} else {
			query = query.Where("media_id > ? OR (media_id = ? AND id > ?)", mediaID, mediaID, itemID)
		}
	}
	var copies []*Copy
	if err := query.Order("media_id " + page.Direction() + ", id " + page.Direction()).Limit(page.SentinelLimit()).Offset(int(page.Offset)).Find(&copies).Error; err != nil {
		return nil, fmt.Errorf("query restore files failed, %w", err)
	}
	reply.HasMore = len(copies) > page.Limit
	if reply.HasMore {
		copies = copies[:page.Limit]
	}
	ids := make([]int64, 0, len(copies))
	for _, copy := range copies {
		ids = append(ids, copy.ItemID)
	}
	var outputs []File
	if err := a.db.WithContext(ctx).Where("item_id IN ?", ids).Find(&outputs).Error; err != nil {
		return nil, fmt.Errorf("query Restore outcomes failed, %w", err)
	}
	byID := make(map[int64]File, len(outputs))
	for _, output := range outputs {
		byID[output.ItemID] = output
	}

	// Translate storage rows into the public Media view.
	items := make([]*entity.RestoreItem, 0, len(copies))
	for _, copy := range copies {
		output := byID[copy.ItemID]
		candidate := newCopyCandidate(copy, &output)
		items = append(items, &entity.RestoreItem{
			Id: copy.ID, Status: copy.Status, SizeBytes: candidate.Size,
			ResultFileId: output.ResultFileID, Linked: output.Linked, Damaged: output.Damaged,
			ActualSha256: output.ActualHash, ActualSizeBytes: output.ActualSize, ResultMessage: output.ResultMessage,
			File: &entity.RestoreFile{
				FileId: candidate.FileID, FileVersionId: candidate.FileVersionID, Sha256: candidate.Hash, TargetPath: candidate.TargetPath,
			},
			Candidate: &entity.RestoreCandidate{
				Id: copy.ID, MediaId: copy.MediaID, MediaPath: copy.MediaPath,
			},
		})
	}
	reply.Items = items
	return reply, nil
}

func (a *jobRestoreRunner) resultSummary(ctx context.Context) (*entity.RestoreSummary, error) {
	// Completed items remain authoritative for migrated legacy manifests without modern output provenance.
	items := a.db.Model(&Copy{}).Select("item_id").Where("status = ?", entity.CopyStatus_COPY_STATUS_COMPLETED).Group("item_id")
	var counts struct {
		VerifiedFiles int64
		DamagedFiles  int64
		UnlinkedFiles int64
	}
	if err := a.db.WithContext(ctx).Table("(?) AS completed", items).Joins("LEFT JOIN files ON files.item_id = completed.item_id").
		Select("COALESCE(SUM(CASE WHEN COALESCE(damaged, ?) = ? THEN 1 ELSE 0 END), 0) AS verified_files, "+
			"COALESCE(SUM(CASE WHEN damaged = ? THEN 1 ELSE 0 END), 0) AS damaged_files, "+
			"COALESCE(SUM(CASE WHEN COALESCE(linked, ?) = ? THEN 1 ELSE 0 END), 0) AS unlinked_files", false, false, true, false, false).
		Scan(&counts).Error; err != nil {
		return nil, fmt.Errorf("query Restore result summary failed, %w", err)
	}

	// Pending candidates share completion state, but count only distinct restore items.
	var pending int64
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).
		Distinct("item_id").Count(&pending).Error; err != nil {
		return nil, fmt.Errorf("query pending Restore summary failed, %w", err)
	}
	return &entity.RestoreSummary{VerifiedFiles: counts.VerifiedFiles, DamagedFiles: counts.DamagedFiles,
		UnlinkedFiles: counts.UnlinkedFiles, PendingFiles: pending}, nil
}
