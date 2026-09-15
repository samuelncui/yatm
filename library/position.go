package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ModelPosition = new(Position)
)

type Position struct {
	ID        int64  `gorm:"primaryKey;autoIncrement" json:"id,omitempty"`
	Signature []byte `gorm:"type:varbinary(256);index:idx_positions_signature" json:"signature,omitempty"`
	MediaID   int64  `gorm:"uniqueIndex:idx_positions_media_path,priority:1;index:idx_positions_media_parent,priority:1;index:idx_positions_media_files,priority:1" json:"media_id,omitempty"`
	Path      string `gorm:"type:varchar(4096);uniqueIndex:idx_positions_media_path,priority:2,length:750;index:idx_positions_media_parent,priority:3,length:750" json:"path,omitempty"`

	ParentPath string `gorm:"type:varchar(4096);index:idx_positions_media_parent,priority:2,length:750" json:"parent_path,omitempty"`
	IsDir      bool   `gorm:"not null;default:false;index:idx_positions_media_files,priority:2" json:"is_dir,omitempty"`

	Mode      uint32    `json:"mode,omitempty"`
	ModTime   time.Time `json:"mod_time,omitempty"`
	WriteTime time.Time `gorm:"index:idx_positions_media_files,priority:3" json:"write_time,omitempty"`
	Size      int64     `json:"size,omitempty"`
	Hash      []byte    `gorm:"type:varbinary(32)" json:"hash,omitempty"` // sha256

	StorageOrder    []byte                  `gorm:"type:varbinary(32);not null;default:''" json:"storage_order,omitempty"`
	StorageMetadata *entity.StorageMetadata `gorm:"type:blob" json:"storage_metadata,omitempty"`

	Health      entity.PositionHealth `gorm:"not null;default:0;index:idx_positions_health" json:"health,omitempty"`
	CheckedAt   int64                 `json:"checked_at,omitempty"`
	HealthJobID int64                 `json:"health_job_id,omitempty"`
}

func (position *Position) BeforeSave(*gorm.DB) error {
	// Both inserts and inventory updates persist a non-null ordering key on every supported SQL driver.
	if position.StorageOrder == nil {
		position.StorageOrder = []byte{}
	}
	position.Signature = nullableSignature(position.Signature)
	return nil
}

// PositionSource yields physical file positions in path order.
type PositionSource func(context.Context, func(*Position) error) error

func (l *Library) SavePosition(ctx context.Context, position *Position) error {
	// Direct inventory publication follows the same coverage rule as Archive and Scan.
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// An inventory edit cannot carry a health observation onto different expected content.
		if position.ID != 0 {
			var previous Position
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&previous, position.ID).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("read previous Position failed, %w", err)
			}
			if err == nil {
				preservePositionHealth(position, &previous)
			}
		}

		// Save the inventory and reconcile known archived coverage atomically.
		if err := tx.Save(position).Error; err != nil {
			return fmt.Errorf("save Position failed, %w", err)
		}

		// Directory summaries never establish saved content.
		if position.IsDir || len(position.Signature) == 0 {
			return nil
		}
		return reconcileCoveredVersions(tx, tx.Where("file_locations.signature = ?", position.Signature))
	})
}

func (l *Library) GetPositionByFileID(ctx context.Context, fileID int64) ([]*Position, error) {
	results, err := l.MGetPositionByFileID(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return results[fileID], nil
}

// GetPosition returns one physical Position by its Library identity.
func (l *Library) GetPosition(ctx context.Context, id int64) (*Position, error) {
	position := new(Position)
	if err := l.db.WithContext(ctx).First(position, id).Error; err != nil {
		return nil, fmt.Errorf("get Position failed, id=%d, %w", id, err)
	}
	return position, nil
}

func (l *Library) MGetPositionByFileID(ctx context.Context, fileIDs ...int64) (map[int64][]*Position, error) {
	if len(fileIDs) == 0 {
		return map[int64][]*Position{}, nil
	}

	var positions []struct {
		OwnerID int64
		Position
	}
	if err := l.db.WithContext(ctx).Model(ModelPosition).Select("positions.*, file_versions.file_id AS owner_id").
		Joins("JOIN file_versions ON file_versions.signature = positions.signature").
		Where("file_versions.file_id IN ? AND positions.is_dir = ?", fileIDs, false).Scan(&positions).Error; err != nil {
		return nil, fmt.Errorf("find positions by file ID failed, %w", err)
	}

	results := make(map[int64][]*Position, len(positions))
	for index := range positions {
		row := &positions[index]
		results[row.OwnerID] = append(results[row.OwnerID], &row.Position)
	}
	return results, nil
}

func (l *Library) ListPositions(ctx context.Context, mediaID int64, parentPath string) ([]*Position, error) {
	positions := make([]*Position, 0, 128)
	if err := l.db.WithContext(ctx).
		Where("media_id = ? AND parent_path = ?", mediaID, parentPath).
		Order("path ASC").Find(&positions).Error; err != nil {
		return nil, fmt.Errorf("list Media positions failed, media_id=%d parent=%q, %w", mediaID, parentPath, err)
	}
	return positions, nil
}

// ListPositionsPage returns immediate children using a stable path cursor.
func (l *Library) ListPositionsPage(
	ctx context.Context,
	mediaID int64,
	parentPath string,
	after string,
	limit int,
) ([]*Position, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("list Media positions failed, limit=%d", limit)
	}

	// Read one sentinel row from the composite parent-and-path index.
	positions := make([]*Position, 0, limit+1)
	if err := l.db.WithContext(ctx).
		Where("media_id = ? AND parent_path = ? AND path > ?", mediaID, parentPath, after).
		Order("path ASC").Limit(limit + 1).Find(&positions).Error; err != nil {
		return nil, false, fmt.Errorf(
			"list Media positions failed, media_id=%d parent=%q after=%q, %w",
			mediaID, parentPath, after, err,
		)
	}
	hasMore := len(positions) > limit
	if hasMore {
		positions = positions[:limit]
	}
	return positions, hasMore, nil
}

// ListMediaFilePositions returns one physical Position page in path order.
func (l *Library) ListMediaFilePositions(
	ctx context.Context,
	mediaID int64,
	after string,
	limit int,
) ([]*Position, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list Media file positions failed, limit=%d", limit)
	}
	positions := make([]*Position, 0, limit)
	if err := l.db.WithContext(ctx).
		Where("media_id = ? AND is_dir = ? AND path > ?", mediaID, false, after).
		Order("path").Limit(limit).Find(&positions).Error; err != nil {
		return nil, fmt.Errorf(
			"list Media file positions failed, media_id=%d after=%q, %w", mediaID, after, err,
		)
	}
	return positions, nil
}

func (l *Library) DeletePositions(ctx context.Context, ids ...int64) error {
	if err := l.db.WithContext(ctx).Where("id IN (?)", ids).Delete(ModelPosition).Error; err != nil {
		return fmt.Errorf("delete positions failed, %w", err)
	}
	return nil
}

// BuildPositionTree adds directly queryable parent paths and derived directory rows to a sorted manifest.
func BuildPositionTree(ctx context.Context, source PositionSource, yield func(*Position) error) error {
	if source == nil {
		return fmt.Errorf("build Position tree failed, source is nil")
	}
	if yield == nil {
		return fmt.Errorf("build Position tree failed, yield is nil")
	}

	// Close completed directories deepest-first and propagate their aggregate to their parent.
	stack := make([]*Position, 0, 8)
	closeDirectories := func(keep int) error {
		for len(stack) > keep {
			directory := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				addPositionSummary(stack[len(stack)-1], directory)
			}
			if err := yield(directory); err != nil {
				return err
			}
		}
		return nil
	}

	// Stream files while retaining only the currently open directory chain.
	if err := source(ctx, func(position *Position) error {
		if position == nil {
			return fmt.Errorf("build Position tree failed, position is nil")
		}
		if err := entity.ValidateRelativePath(position.Path); err != nil {
			return fmt.Errorf("build Position tree failed, %w", err)
		}

		directoryPaths := make([]string, 0, 8)
		if directory := path.Dir(position.Path); directory != "." {
			var current string
			for _, name := range strings.Split(directory, "/") {
				current += name + "/"
				directoryPaths = append(directoryPaths, current)
			}
		}

		common := 0
		for common < len(stack) && common < len(directoryPaths) && stack[common].Path == directoryPaths[common] {
			common++
		}
		if err := closeDirectories(common); err != nil {
			return err
		}
		for index := common; index < len(directoryPaths); index++ {
			parentPath := ""
			if index > 0 {
				parentPath = directoryPaths[index-1]
			}
			stack = append(stack, &Position{
				MediaID: position.MediaID, Path: directoryPaths[index], ParentPath: parentPath,
				IsDir: true, Mode: uint32(fs.ModeDir | fs.ModePerm), StorageOrder: []byte{},
			})
		}

		position.IsDir = false
		position.StorageOrder = append([]byte{}, position.StorageOrder...)
		if len(stack) == 0 {
			position.ParentPath = ""
		} else {
			position.ParentPath = stack[len(stack)-1].Path
			addPositionSummary(stack[len(stack)-1], position)
		}
		return yield(position)
	}); err != nil {
		return err
	}
	return closeDirectories(0)
}

func addPositionSummary(target, child *Position) {
	target.Size += child.Size
	if target.ModTime.Before(child.ModTime) {
		target.ModTime = child.ModTime
	}
	if target.WriteTime.Before(child.WriteTime) {
		target.WriteTime = child.WriteTime
	}
}

func rebuildPositionIndex(ctx context.Context, db *gorm.DB) error {
	// Rebuild one Media at a time so every derived tree changes atomically.
	var mediaIDs []int64
	if err := db.WithContext(ctx).Model(&Position{}).Distinct("media_id").Order("media_id").Pluck("media_id", &mediaIDs).Error; err != nil {
		return fmt.Errorf("list Position Media failed, %w", err)
	}
	for _, mediaID := range mediaIDs {
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			return rebuildMediaPositionIndex(ctx, tx, mediaID)
		}); err != nil {
			return err
		}
	}
	return nil
}

func rebuildMediaPositionIndex(ctx context.Context, tx *gorm.DB, mediaID int64) error {
	if err := tx.Where("media_id = ? AND is_dir = ?", mediaID, true).Delete(&Position{}).Error; err != nil {
		return fmt.Errorf("clear derived Position directories failed, media_id=%d, %w", mediaID, err)
	}

	positions := make([]*Position, 0, batchSize)
	flush := func() error {
		if len(positions) == 0 {
			return nil
		}
		result := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}}, UpdateAll: true,
		}).CreateInBatches(positions, batchSize)
		if result.Error != nil {
			return fmt.Errorf("write Position index failed, media_id=%d, %w", mediaID, result.Error)
		}
		positions = positions[:0]
		return nil
	}
	yield := func(position *Position) error {
		positions = append(positions, position)
		if len(positions) == batchSize {
			return flush()
		}
		return nil
	}

	var cursorPath string
	var cursorID int64
	source := func(_ context.Context, emit func(*Position) error) error {
		for {
			var files []*Position
			result := tx.Where(
				"media_id = ? AND is_dir = ? AND (path > ? OR (path = ? AND id > ?))",
				mediaID, false, cursorPath, cursorPath, cursorID,
			).Order("path ASC, id ASC").Limit(batchSize).Find(&files)
			if result.Error != nil {
				return fmt.Errorf("read physical Positions failed, media_id=%d, %w", mediaID, result.Error)
			}
			if len(files) == 0 {
				return nil
			}
			for _, file := range files {
				if err := emit(file); err != nil {
					return err
				}
				cursorPath = file.Path
				cursorID = file.ID
			}
		}
	}
	if err := BuildPositionTree(ctx, source, yield); err != nil {
		return err
	}
	return flush()
}
