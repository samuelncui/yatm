package legacy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type legacyLibraryFile struct {
	ID        int64 `gorm:"primaryKey;autoIncrement"`
	ParentID  int64
	Name      string
	Mode      uint32
	ModTime   time.Time
	Hash      []byte
	Size      int64
	Signature []byte
}

func (legacyLibraryFile) TableName() string { return "files" }

type legacyLibraryTape struct {
	ID            int64 `gorm:"primaryKey;autoIncrement"`
	Barcode       string
	Name          string
	Encryption    string
	CreateTime    time.Time
	DestroyTime   *time.Time
	CapacityBytes int64
	WritenBytes   int64
}

func (legacyLibraryTape) TableName() string { return "tapes" }

type legacyLibraryPosition struct {
	ID        int64 `gorm:"primaryKey;autoIncrement"`
	FileID    int64
	TapeID    int64
	Path      string
	Mode      uint32
	ModTime   time.Time
	WriteTime time.Time
	Size      int64
	Hash      []byte
}

func (legacyLibraryPosition) TableName() string { return "positions" }

type stagedLibraryFile struct {
	ID        int64           `gorm:"primaryKey;autoIncrement"`
	ParentID  int64           `gorm:"index:idx_files_parent_name,unique"`
	Name      string          `gorm:"type:varchar(256);index:idx_files_parent_name,unique"`
	Kind      entity.FileKind `gorm:"not null"`
	CreatedAt int64           `gorm:"autoCreateTime:milli"`
	UpdatedAt int64           `gorm:"autoUpdateTime:milli"`
	Mode      uint32
	ModTime   time.Time
	Hash      []byte `gorm:"type:varbinary(32)"`
	Size      int64
	Note      string `gorm:"type:varchar(4096);not null;default:''"`

	Signature []byte `gorm:"type:varbinary(256)"`
}

func (stagedLibraryFile) TableName() string { return "files_staging" }

// BeforeSave uses the runtime Library's nullable representation for unset signatures.
func (file *stagedLibraryFile) BeforeSave(*gorm.DB) error {
	if len(file.Signature) == 0 {
		file.Signature = nil
	}
	return nil
}

type stagedLibraryMedia struct {
	ID            int64                `gorm:"primaryKey;autoIncrement"`
	Kind          entity.MediaKind     `gorm:"not null;index:idx_media_kind_identity,unique,priority:1"`
	Identity      string               `gorm:"type:varchar(128);not null;index:idx_media_kind_identity,unique,priority:2"`
	Name          string               `gorm:"type:varchar(256)"`
	Profile       *entity.MediaProfile `gorm:"type:blob;not null"`
	CreateTime    time.Time
	DestroyTime   *time.Time
	CapacityBytes int64
	WrittenBytes  int64
}

func (stagedLibraryMedia) TableName() string { return "media_staging" }

type stagedLibraryPosition struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	FileID    int64  `gorm:"index:idx_positions_file_id"`
	Signature []byte `gorm:"type:varbinary(256);index:idx_positions_signature"`
	MediaID   int64  `gorm:"uniqueIndex:idx_positions_media_path,priority:1;index:idx_positions_media_parent,priority:1;index:idx_positions_media_files,priority:1"`
	Path      string `gorm:"type:varchar(4096);uniqueIndex:idx_positions_media_path,priority:2,length:750"`

	ParentPath string `gorm:"type:varchar(4096);index:idx_positions_media_parent,priority:2,length:750"`
	IsDir      bool   `gorm:"not null;default:false;index:idx_positions_media_files,priority:2"`

	Mode      uint32
	ModTime   time.Time
	WriteTime time.Time `gorm:"index:idx_positions_media_files,priority:3"`
	Size      int64
	Hash      []byte `gorm:"type:varbinary(32)"`

	StorageOrder    []byte                  `gorm:"type:varbinary(32);not null;default:''"`
	StorageMetadata *entity.StorageMetadata `gorm:"type:blob"`
}

func (stagedLibraryPosition) TableName() string { return "positions_staging" }

type stagedLibraryVersion library.FileVersion

func (stagedLibraryVersion) TableName() string { return "file_versions_staging" }

func prepareLibrary(ctx context.Context, db *gorm.DB, indexRoot string, report *Report) error {
	// Require the complete legacy Library before creating its replacement tables.
	for _, table := range []string{"files", "tapes", "positions"} {
		if !db.Migrator().HasTable(table) {
			return fmt.Errorf("legacy Library table %q is missing", table)
		}
	}
	if err := db.WithContext(ctx).AutoMigrate(
		&stagedLibraryFile{}, &stagedLibraryMedia{}, &stagedLibraryPosition{}, &stagedLibraryVersion{},
	); err != nil {
		return fmt.Errorf("create current Library staging tables failed, %w", err)
	}

	// Copy the legacy catalog without retaining complete tables in memory.
	files, err := copyLibraryFiles(ctx, db)
	if err != nil {
		return err
	}
	tapes, err := copyLibraryTapes(ctx, db)
	if err != nil {
		return err
	}
	if _, _, err := copyLibraryPositions(ctx, db); err != nil {
		return err
	}

	// Reconcile physical copies before counting retained Positions for the report.
	storagePositions, warnings, err := indexLibraryPositions(ctx, db, indexRoot)
	if err != nil {
		return err
	}
	if err := rebuildStagedPositionIndex(ctx, db); err != nil {
		return err
	}
	if err := prepareArchivedVersions(ctx, db, report); err != nil {
		return err
	}
	var positions int64
	if err := db.WithContext(ctx).Model(&stagedLibraryPosition{}).Where("is_dir = ?", false).
		Count(&positions).Error; err != nil {
		return fmt.Errorf("count migrated physical Positions failed, %w", err)
	}
	var directories int64
	if err := db.WithContext(ctx).Model(&stagedLibraryPosition{}).Where("is_dir = ?", true).
		Count(&directories).Error; err != nil {
		return fmt.Errorf("count migrated Position directories failed, %w", err)
	}

	// Publish counts from the reconciled staging tables.
	report.LibraryFiles = files
	report.LibraryTapes = tapes
	report.LibraryPositions = int(positions)
	report.LibraryDirectories = int(directories)
	report.LibraryStoragePositions = storagePositions
	report.Warnings = append(report.Warnings, warnings...)
	return nil
}

func indexLibraryPositions(ctx context.Context, db *gorm.DB, root string) (int, []string, error) {
	// Read one staged Tape at a time so captured indexes and database rows remain bounded.
	var cursor int64
	total := 0
	var warnings []string
	for {
		var tapes []*stagedLibraryMedia
		if err := db.WithContext(ctx).Where("id > ?", cursor).Order("id").Limit(256).Find(&tapes).Error; err != nil {
			return 0, nil, fmt.Errorf("read current Library tapes for LTFS indexing failed, cursor=%d, %w", cursor, err)
		}
		if len(tapes) == 0 {
			return total, warnings, nil
		}
		for _, tape := range tapes {
			indexed, tapeWarnings, err := indexLibraryTape(ctx, db, root, tape)
			if err != nil {
				return 0, nil, err
			}
			total += indexed
			warnings = append(warnings, tapeWarnings...)
			cursor = tape.ID
		}
	}
}

func indexLibraryTape(
	ctx context.Context,
	db *gorm.DB,
	root string,
	tape *stagedLibraryMedia,
) (int, []string, error) {
	// Treat a missing or empty captured index as a legacy path-ordered Tape.
	filename := filepath.Join(root, tape.Identity+".schema")
	file, err := os.Open(filename)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("open legacy LTFS index failed, barcode=%q, %w", tape.Identity, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, nil, fmt.Errorf("stat legacy LTFS index failed, barcode=%q, %w", tape.Identity, err)
	}
	if info.Size() == 0 {
		return 0, nil, nil
	}

	// Reconcile every physical Position against the durable captured index.
	var indexed int64
	var warnings []string
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		yield := func(entry *mediapkg.LTFSIndexEntry) error {
			var positions []*stagedLibraryPosition
			if err := tx.Where("media_id = ? AND path = ? AND is_dir = ?", tape.ID, entry.Path, false).
				Limit(2).Find(&positions).Error; err != nil {
				return fmt.Errorf("query legacy LTFS Position failed, barcode=%q path=%q, %w", tape.Identity, entry.Path, err)
			}
			if len(positions) == 0 {
				return nil
			}
			if len(positions) > 1 {
				return fmt.Errorf("legacy Library contains duplicate Positions, barcode=%q path=%q", tape.Identity, entry.Path)
			}
			position := positions[0]
			if position.StorageMetadata != nil {
				return fmt.Errorf("legacy LTFS index contains duplicate path, barcode=%q path=%q", tape.Identity, entry.Path)
			}
			if position.Size != entry.Size {
				if err := tx.Delete(position).Error; err != nil {
					return fmt.Errorf("remove mismatched legacy Position failed, barcode=%q path=%q, %w", tape.Identity, entry.Path, err)
				}
				warnings = append(warnings, fmt.Sprintf(
					"Tape %s Position %q was omitted: legacy size=%d LTFS size=%d",
					tape.Identity,
					entry.Path,
					position.Size,
					entry.Size,
				))
				return nil
			}

			result := tx.Model(position).Updates(map[string]any{
				"storage_order":    entry.Storage.Order,
				"storage_metadata": entry.Storage.Metadata,
			})
			if result.Error != nil {
				return fmt.Errorf("write current LTFS Position failed, barcode=%q path=%q, %w", tape.Identity, entry.Path, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("write current LTFS Position failed, barcode=%q path=%q affected=%d", tape.Identity, entry.Path, result.RowsAffected)
			}
			indexed++
			return nil
		}
		if err := mediapkg.ParseLTFSIndex(ctx, file, yield); err != nil {
			return fmt.Errorf("parse legacy LTFS index failed, barcode=%q, %w", tape.Identity, err)
		}

		// Remove legacy Positions absent from this authoritative index in bounded pages.
		var cursor int64
		for {
			var missing []*stagedLibraryPosition
			if err := tx.Select("id", "path").Where(
				"media_id = ? AND is_dir = ? AND storage_metadata IS NULL AND id > ?",
				tape.ID,
				false,
				cursor,
			).Order("id").Limit(256).Find(&missing).Error; err != nil {
				return fmt.Errorf("query missing legacy Positions failed, barcode=%q cursor=%d, %w", tape.Identity, cursor, err)
			}
			if len(missing) == 0 {
				break
			}
			ids := make([]int64, 0, len(missing))
			for _, position := range missing {
				ids = append(ids, position.ID)
				warnings = append(warnings, fmt.Sprintf(
					"Tape %s Position %q was omitted because it is absent from the captured LTFS index",
					tape.Identity,
					position.Path,
				))
				cursor = position.ID
			}
			if err := tx.Where("id IN ?", ids).Delete(&stagedLibraryPosition{}).Error; err != nil {
				return fmt.Errorf("remove missing legacy Positions failed, barcode=%q, %w", tape.Identity, err)
			}
		}

		// Derive written bytes from the same verified physical rows.
		var written int64
		if err := tx.Model(&stagedLibraryPosition{}).
			Where("media_id = ? AND is_dir = ?", tape.ID, false).
			Select("COALESCE(SUM(size), 0)").Scan(&written).Error; err != nil {
			return fmt.Errorf("sum migrated Tape bytes failed, barcode=%q, %w", tape.Identity, err)
		}
		if err := tx.Model(tape).Update("written_bytes", written).Error; err != nil {
			return fmt.Errorf("update migrated Tape bytes failed, barcode=%q, %w", tape.Identity, err)
		}
		profile := tape.Profile.GetTape()
		if profile == nil {
			return fmt.Errorf("migrated Tape profile is invalid, barcode=%q", tape.Identity)
		}
		profile.Format = library.TapeFormatLTFSV1
		if err := tx.Model(tape).Update("profile", tape.Profile).Error; err != nil {
			return fmt.Errorf("update migrated Tape format failed, barcode=%q, %w", tape.Identity, err)
		}
		return nil
	}); err != nil {
		return 0, nil, err
	}
	return int(indexed), warnings, nil
}

func rebuildStagedPositionIndex(ctx context.Context, db *gorm.DB) error {
	// Rebuild one reconciled Tape tree at a time without retaining its manifest.
	var tapeCursor int64
	for {
		var tapes []*stagedLibraryMedia
		if err := db.WithContext(ctx).Where("id > ?", tapeCursor).Order("id").Limit(256).Find(&tapes).Error; err != nil {
			return fmt.Errorf("read migrated Tapes for Position indexing failed, cursor=%d, %w", tapeCursor, err)
		}
		if len(tapes) == 0 {
			return nil
		}
		for _, tape := range tapes {
			if err := rebuildStagedTapePositionIndex(ctx, db, tape.ID); err != nil {
				return err
			}
			tapeCursor = tape.ID
		}
	}
}

func rebuildStagedTapePositionIndex(ctx context.Context, db *gorm.DB, tapeID int64) error {
	// Replace every derived directory and parent field atomically for this Tape.
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("media_id = ? AND is_dir = ?", tapeID, true).
			Delete(&stagedLibraryPosition{}).Error; err != nil {
			return fmt.Errorf("clear migrated Position directories failed, tape_id=%d, %w", tapeID, err)
		}

		positions := make([]*stagedLibraryPosition, 0, 256)
		flush := func() error {
			if len(positions) == 0 {
				return nil
			}
			result := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}}, UpdateAll: true,
			}).CreateInBatches(positions, 256)
			if result.Error != nil {
				return fmt.Errorf("write migrated Position index failed, tape_id=%d, %w", tapeID, result.Error)
			}
			positions = positions[:0]
			return nil
		}
		// Preserve legacy-only ownership across the synchronous tree builder, outside the target Position model.
		owners := make(map[int64]int64)
		yield := func(position *library.Position) error {
			positions = append(positions, &stagedLibraryPosition{
				ID: position.ID, FileID: owners[position.ID], MediaID: position.MediaID, Path: position.Path,
				ParentPath: position.ParentPath, IsDir: position.IsDir,
				Mode: position.Mode, ModTime: position.ModTime, WriteTime: position.WriteTime,
				Size: position.Size, Hash: position.Hash, StorageOrder: position.StorageOrder,
				StorageMetadata: position.StorageMetadata,
			})
			delete(owners, position.ID)
			if len(positions) == cap(positions) {
				return flush()
			}
			return nil
		}

		var cursorPath string
		var cursorID int64
		source := func(_ context.Context, emit func(*library.Position) error) error {
			for {
				var files []*stagedLibraryPosition
				if err := tx.Where(
					"media_id = ? AND is_dir = ? AND (path > ? OR (path = ? AND id > ?))",
					tapeID,
					false,
					cursorPath,
					cursorPath,
					cursorID,
				).Order("path, id").Limit(256).Find(&files).Error; err != nil {
					return fmt.Errorf("read migrated physical Positions failed, tape_id=%d, %w", tapeID, err)
				}
				if len(files) == 0 {
					return nil
				}
				for _, file := range files {
					owners[file.ID] = file.FileID
					if err := emit(&library.Position{
						ID: file.ID, MediaID: file.MediaID, Path: file.Path,
						ParentPath: file.ParentPath, IsDir: file.IsDir,
						Mode: file.Mode, ModTime: file.ModTime, WriteTime: file.WriteTime,
						Size: file.Size, Hash: file.Hash, StorageOrder: file.StorageOrder,
						StorageMetadata: file.StorageMetadata,
					}); err != nil {
						return err
					}
					cursorPath = file.Path
					cursorID = file.ID
				}
			}
		}
		if err := library.BuildPositionTree(ctx, source, yield); err != nil {
			return err
		}
		return flush()
	}); err != nil {
		return err
	}
	return nil
}

func copyLibraryFiles(ctx context.Context, db *gorm.DB) (int, error) {
	var cursor int64
	count := 0
	for {
		var source []*legacyLibraryFile
		query := db.WithContext(ctx).Order("id").Limit(256)
		if count > 0 {
			query = query.Where("id > ?", cursor)
		}
		if err := query.Find(&source).Error; err != nil {
			return 0, fmt.Errorf("read legacy Library files failed, cursor=%d, %w", cursor, err)
		}
		if len(source) == 0 {
			return count, nil
		}

		rows := make([]*stagedLibraryFile, 0, len(source))
		for _, file := range source {
			kind := entity.FileKind_FILE_KIND_REGULAR
			if os.FileMode(file.Mode).IsDir() {
				kind = entity.FileKind_FILE_KIND_DIRECTORY
			}
			rows = append(rows, &stagedLibraryFile{
				ID: file.ID, ParentID: file.ParentID, Name: file.Name, Mode: file.Mode,
				Kind: kind, CreatedAt: file.ModTime.UnixMilli(), UpdatedAt: file.ModTime.UnixMilli(),
				ModTime: file.ModTime, Hash: file.Hash, Size: file.Size, Signature: file.Signature,
			})
		}
		if err := db.WithContext(ctx).CreateInBatches(rows, 256).Error; err != nil {
			return 0, fmt.Errorf("write current Library files failed, cursor=%d, %w", cursor, err)
		}
		cursor = source[len(source)-1].ID
		count += len(source)
	}
}

func copyLibraryTapes(ctx context.Context, db *gorm.DB) (int, error) {
	var cursor int64
	count := 0
	for {
		var source []*legacyLibraryTape
		if err := db.WithContext(ctx).Where("id > ?", cursor).Order("id").Limit(256).Find(&source).Error; err != nil {
			return 0, fmt.Errorf("read legacy Library tapes failed, cursor=%d, %w", cursor, err)
		}
		if len(source) == 0 {
			return count, nil
		}

		rows := make([]*stagedLibraryMedia, 0, len(source))
		for _, tape := range source {
			rows = append(rows, &stagedLibraryMedia{
				ID: tape.ID, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: tape.Barcode,
				Name: tape.Name, Profile: (&entity.TapeMediaProfile{
					Encryption: tape.Encryption, Format: library.TapeFormatLTFSV0,
				}).Pack(),
				CreateTime: tape.CreateTime, DestroyTime: tape.DestroyTime,
				CapacityBytes: tape.CapacityBytes, WrittenBytes: tape.WritenBytes,
			})
		}
		if err := db.WithContext(ctx).CreateInBatches(rows, 256).Error; err != nil {
			return 0, fmt.Errorf("write current Library tapes failed, cursor=%d, %w", cursor, err)
		}
		cursor = source[len(source)-1].ID
		count += len(source)
	}
}

func copyLibraryPositions(ctx context.Context, db *gorm.DB) (int, int, error) {
	// Reserve derived directory IDs above every physical legacy Position ID.
	var nextID int64
	if err := db.WithContext(ctx).Model(&legacyLibraryPosition{}).
		Select("COALESCE(MAX(id), 0)").Scan(&nextID).Error; err != nil {
		return 0, 0, fmt.Errorf("read legacy Position maximum ID failed, %w", err)
	}
	var tapeIDs []int64
	if err := db.WithContext(ctx).Model(&legacyLibraryPosition{}).
		Distinct("tape_id").Order("tape_id").Pluck("tape_id", &tapeIDs).Error; err != nil {
		return 0, 0, fmt.Errorf("list legacy Position tapes failed, %w", err)
	}

	physicalCount := 0
	directoryCount := 0
	for _, tapeID := range tapeIDs {
		rows := make([]*stagedLibraryPosition, 0, 256)
		flush := func() error {
			if len(rows) == 0 {
				return nil
			}
			if err := db.WithContext(ctx).CreateInBatches(rows, 256).Error; err != nil {
				return fmt.Errorf("write current Positions failed, tape_id=%d, %w", tapeID, err)
			}
			rows = rows[:0]
			return nil
		}
		// Preserve legacy-only ownership across the synchronous tree builder, outside the target Position model.
		owners := make(map[int64]int64)
		yield := func(position *library.Position) error {
			if position.IsDir {
				nextID++
				position.ID = nextID
				directoryCount++
			} else {
				physicalCount++
			}
			rows = append(rows, &stagedLibraryPosition{
				ID: position.ID, FileID: owners[position.ID], MediaID: position.MediaID, Path: position.Path,
				ParentPath: position.ParentPath, IsDir: position.IsDir,
				Mode: position.Mode, ModTime: position.ModTime, WriteTime: position.WriteTime,
				Size: position.Size, Hash: position.Hash, StorageOrder: position.StorageOrder,
			})
			delete(owners, position.ID)
			if len(rows) == 256 {
				return flush()
			}
			return nil
		}

		var cursorPath string
		var cursorID int64
		source := func(_ context.Context, emit func(*library.Position) error) error {
			for {
				var positions []*legacyLibraryPosition
				result := db.WithContext(ctx).Where(
					"tape_id = ? AND (path > ? OR (path = ? AND id > ?))",
					tapeID, cursorPath, cursorPath, cursorID,
				).Order("path, id").Limit(256).Find(&positions)
				if result.Error != nil {
					return fmt.Errorf("read legacy Positions failed, tape_id=%d path=%q id=%d, %w", tapeID, cursorPath, cursorID, result.Error)
				}
				if len(positions) == 0 {
					return nil
				}
				for _, position := range positions {
					owners[position.ID] = position.FileID
					if err := emit(&library.Position{
						ID: position.ID, MediaID: position.TapeID,
						Path: position.Path, Mode: position.Mode, ModTime: position.ModTime,
						WriteTime: position.WriteTime, Size: position.Size, Hash: position.Hash,
					}); err != nil {
						return err
					}
					cursorPath = position.Path
					cursorID = position.ID
				}
			}
		}
		if err := library.BuildPositionTree(ctx, source, yield); err != nil {
			return 0, 0, fmt.Errorf("index legacy Positions failed, tape_id=%d, %w", tapeID, err)
		}
		if err := flush(); err != nil {
			return 0, 0, err
		}
	}
	return physicalCount, directoryCount, nil
}
