package library

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

var (
	ModelPosition = new(Position)
)

type Position struct {
	ID     int64  `gorm:"primaryKey;autoIncrement" json:"id,omitempty"`
	FileID int64  `gorm:"index:idx_file_id" json:"file_id,omitempty"`
	TapeID int64  `gorm:"index:idx_tape_path" json:"tape_id,omitempty"`
	Path   string `gorm:"type:varchar(4096);index:idx_tape_path" json:"path,omitempty"`

	Mode      uint32    `json:"mode,omitempty"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	WriteTime time.Time `json:"write_time,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Hash      []byte    `gorm:"type:varbinary(32)" json:"hash,omitempty"` // sha256
}

func (l *Library) GetPositionByFileID(ctx context.Context, fileID int64) ([]*Position, error) {
	results, err := l.MGetPositionByFileID(ctx, l.db.WithContext(ctx), fileID)
	if err != nil {
		panic(err)
	}
	return results[fileID], nil
}

func (l *Library) MGetPositionByFileID(ctx context.Context, tx *gorm.DB, fileIDs ...int64) (map[int64][]*Position, error) {
	if len(fileIDs) == 0 {
		return map[int64][]*Position{}, nil
	}

	positions := make([]*Position, 0, len(fileIDs))
	if r := tx.Where("file_id IN (?)", fileIDs).Find(&positions); r.Error != nil {
		return nil, fmt.Errorf("find position by file id fail, %w", r.Error)
	}

	results := make(map[int64][]*Position, len(positions))
	for _, posi := range positions {
		results[posi.FileID] = append(results[posi.FileID], posi)
	}

	return results, nil
}
