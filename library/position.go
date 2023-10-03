package library

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/samuelncui/yatm/resource"
	"github.com/samuelncui/yatm/tools"
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

func (l *Library) SavePosition(ctx context.Context, posi *Position) error {
	return l.db.WithContext(ctx).Save(posi).Error
}

func (l *Library) GetPositionByFileID(ctx context.Context, fileID int64) ([]*Position, error) {
	results, err := l.MGetPositionByFileID(ctx, fileID)
	if err != nil {
		panic(err)
	}
	return results[fileID], nil
}

func (l *Library) MGetPositionByFileID(ctx context.Context, fileIDs ...int64) (map[int64][]*Position, error) {
	if len(fileIDs) == 0 {
		return map[int64][]*Position{}, nil
	}

	positions := make([]*Position, 0, len(fileIDs))
	if r := l.db.WithContext(ctx).Where("file_id IN (?)", fileIDs).Find(&positions); r.Error != nil {
		return nil, fmt.Errorf("find position by file id fail, %w", r.Error)
	}

	results := make(map[int64][]*Position, len(positions))
	for _, posi := range positions {
		results[posi.FileID] = append(results[posi.FileID], posi)
	}

	return results, nil
}

func (l *Library) ListPositions(ctx context.Context, tapeID int64, prefix string) ([]*Position, error) {
	positions := make([]*Position, 0, 128)
	if r := l.db.WithContext(ctx).Where("tape_id = ? AND path LIKE ?", tapeID, resource.SQLEscape(prefix)+"%").Order("path ASC").Find(&positions); r.Error != nil {
		return nil, fmt.Errorf("find position by file id fail, %w", r.Error)
	}

	convertPath := tools.Cache(func(p string) string { return strings.ReplaceAll(p, "/", "\x00") })
	sort.Slice(positions, func(i int, j int) bool {
		return convertPath(positions[i].Path) < convertPath(positions[j].Path)
	})

	filtered := make([]*Position, 0, 128)
	for _, posi := range positions {
		if !strings.HasPrefix(posi.Path, prefix) {
			continue
		}

		suffix := posi.Path[len(prefix):]
		idx := strings.IndexRune(suffix, '/')
		if idx < 0 {
			filtered = append(filtered, posi)
			continue
		}

		path := prefix + suffix[:idx+1]
		if len(filtered) > 0 && filtered[len(filtered)-1].Path == path {
			target := filtered[len(filtered)-1]
			target.Size += posi.Size

			if target.ModTime.Before(posi.ModTime) {
				target.ModTime = posi.ModTime
			}
			if target.WriteTime.Before(posi.WriteTime) {
				target.WriteTime = posi.WriteTime
			}

			continue
		}

		filtered = append(filtered, &Position{
			TapeID:    posi.TapeID,
			Path:      path,
			Mode:      uint32(fs.ModeDir | fs.ModePerm),
			ModTime:   posi.ModTime,
			WriteTime: posi.WriteTime,
			Size:      posi.Size,
		})
	}

	return filtered, nil
}

func (l *Library) DeletePositions(ctx context.Context, ids ...int64) error {
	if r := l.db.WithContext(ctx).Where("id IN (?)", ids).Delete(ModelPosition); r.Error != nil {
		return fmt.Errorf("delete positions fail, err= %w", r.Error)
	}

	return nil
}
