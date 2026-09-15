package archive

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

const maxQueryItems = 1000

func (a *jobArchiveRunner) queryFiles(ctx context.Context, param *entity.ListArchiveJobFilesRequest) (*entity.ListArchiveJobFilesReply, error) {
	// Apply bounded RPC filters directly to the Job DB manifest.
	q := gorm.G[*Item](a.db).Order("target_path")
	if len(param.FilterStatus) > 0 {
		q = q.Where("status IN ?", param.FilterStatus)
	}
	if param.Offset != nil {
		q = q.Offset(int(*param.Offset))
	}
	limit := int(param.Limit)
	if limit <= 0 {
		limit = 100
	}
	if limit > maxQueryItems {
		limit = maxQueryItems
	}
	q = q.Limit(limit)

	// Load only the requested page.
	items, err := q.Find(ctx)
	if err != nil {
		return nil, fmt.Errorf("query archive manifest failed, %w", err)
	}

	// Translate storage-only protobufs into the public RPC view.
	results := make([]*entity.ArchiveItem, 0, len(items))
	for _, item := range items {
		results = append(results, &entity.ArchiveItem{
			Id:      item.ID,
			Status:  item.Status,
			Size:    item.Size,
			MediaId: item.MediaID,
			File: &entity.ArchiveFile{
				SourcePath: item.Data.SourcePath,
				MediaPath:  item.MediaPath,
				TargetPath: item.TargetPath,
				Expected:   item.Data.Expected,
			},
		})
	}

	return &entity.ListArchiveJobFilesReply{
		Items: results,
	}, nil
}
