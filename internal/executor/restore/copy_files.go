package restore

import (
	"context"
	"fmt"
)

func newCopyCandidate(copy *Copy, file *File) *copyCandidate {
	return &copyCandidate{
		ID:            copy.ID,
		ItemID:        copy.ItemID,
		MediaID:       copy.MediaID,
		MediaPath:     copy.MediaPath,
		StorageOrder:  copy.StorageOrder,
		PositionID:    copy.PositionID,
		FileID:        file.FileID,
		FileVersionID: file.FileVersionID,
		Signature:     file.Signature,
		Hash:          file.Hash,
		Size:          file.Size,
		Mode:          file.Mode,
		MtimeNS:       file.MtimeNS,
		TargetPath:    file.Path,
	}
}

func (a *jobRestoreRunner) copyCandidates(ctx context.Context, copies []*Copy) ([]*copyCandidate, error) {
	// Collect the selected File rows required by the complete candidate page.
	if len(copies) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(copies))
	for _, copy := range copies {
		ids = append(ids, copy.ItemID)
	}
	var files []File
	if err := a.db.WithContext(ctx).Where("item_id IN ?", ids).Find(&files).Error; err != nil {
		return nil, err
	}

	// Join each physical row to its required frozen File facts in caller order.
	byID := make(map[int64]*File, len(files))
	for i := range files {
		byID[files[i].ItemID] = &files[i]
	}
	candidates := make([]*copyCandidate, 0, len(copies))
	for _, copy := range copies {
		file := byID[copy.ItemID]
		if file == nil {
			return nil, fmt.Errorf("Restore file is missing, item_id=%d", copy.ItemID)
		}
		candidates = append(candidates, newCopyCandidate(copy, file))
	}
	return candidates, nil
}
