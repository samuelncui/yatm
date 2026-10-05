package archive

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func (a *jobArchiveRunner) queryFiles(ctx context.Context, param *entity.ListArchiveJobFilesRequest) (*entity.ListArchiveJobFilesResponse, error) {
	// Resolve one bounded page request shared by every Job manifest listing.
	page, err := executor.NewJobResultPage(param.Limit, param.Cursor, param.Order, param.Offset, param.IncludeTotal)
	if err != nil {
		return nil, err
	}

	// The manifest holds the frozen logical targets, so its path is the order key.
	filtered := func() *gorm.DB {
		query := a.db.WithContext(ctx).Model(&Item{})
		if len(param.FilterStatus) > 0 {
			query = query.Where("status IN ?", param.FilterStatus)
		}
		return query
	}
	reply := &entity.ListArchiveJobFilesResponse{}
	if page.IncludeTotal {
		var total int64
		if err := filtered().Count(&total).Error; err != nil {
			return nil, fmt.Errorf("count archive manifest failed, %w", err)
		}
		reply.TotalFileCount = proto.Int64(total)
	}

	// Load only the requested page, plus one sentinel row for continuation.
	query := filtered()
	if page.Cursor != "" {
		query = query.Where("target_path "+page.Comparison()+" ?", page.Cursor)
	}
	var items []*Item
	if err := query.Order("target_path " + page.Direction()).Limit(page.SentinelLimit()).Offset(int(page.Offset)).Find(&items).Error; err != nil {
		return nil, fmt.Errorf("query archive manifest failed, %w", err)
	}
	reply.HasMore = len(items) > page.Limit
	if reply.HasMore {
		items = items[:page.Limit]
	}

	// Translate storage-only protobufs into the public RPC view.
	results := make([]*entity.ArchiveItem, 0, len(items))
	for _, item := range items {
		results = append(results, &entity.ArchiveItem{
			Id:        item.ID,
			Status:    item.Status,
			SizeBytes: item.Size,
			MediaId:   item.MediaID,
			File: &entity.ArchiveFile{
				SourcePath: item.Data.SourcePath,
				MediaPath:  item.MediaPath,
				TargetPath: item.TargetPath,
				Expected:   item.Data.Expected,
			},
		})
	}
	reply.Items = results
	return reply, nil
}
