package library

import (
	"context"
	"encoding/json"
	"fmt"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/modern-go/reflect2"
	"github.com/samber/lo"
	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

const (
	batchSize = 100
)

type Library struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Library {
	return &Library{db: db}
}

func (l *Library) AutoMigrate() error {
	return l.db.AutoMigrate(ModelFile, ModelPosition, ModelTape)
}

type ExportLibrary struct {
	Files     *[]*File     `json:"files,omitempty"`
	Tapes     *[]*Tape     `json:"tapes,omitempty"`
	Positions *[]*Position `json:"positions,omitempty"`
}

func (l *Library) Export(ctx context.Context, types []entity.LibraryEntityType) ([]byte, error) {
	results := new(ExportLibrary)

	for _, t := range lo.Uniq(types) {
		switch t {
		case entity.LibraryEntityType_FILE:
			files, err := listAll(ctx, l, make([]*File, 0, batchSize))
			if err != nil {
				return nil, fmt.Errorf("list all files fail, %w", err)
			}
			results.Files = &files
		case entity.LibraryEntityType_TAPE:
			tapes, err := listAll(ctx, l, make([]*Tape, 0, batchSize))
			if err != nil {
				return nil, fmt.Errorf("list all tapes fail, %w", err)
			}
			results.Tapes = &tapes
		case entity.LibraryEntityType_POSITION:
			positions, err := listAll(ctx, l, make([]*Position, 0, batchSize))
			if err != nil {
				return nil, fmt.Errorf("list all positions fail, %w", err)
			}
			results.Positions = &positions
		}
	}

	return json.Marshal(results)
}

func (l *Library) Import(ctx context.Context, buf []byte) error {
	results := new(ExportLibrary)
	if err := json.Unmarshal(buf, results); err != nil {
		return fmt.Errorf("unmarshal import data fail, %w", err)
	}

	if results.Files != nil {
		if r := l.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(ModelFile); r.Error != nil {
			return fmt.Errorf("cleanup file fail, %w", r.Error)
		}
		if r := l.db.WithContext(ctx).CreateInBatches(*results.Files, 100); r.Error != nil {
			return fmt.Errorf("insert file fail, %w", r.Error)
		}
	}

	if results.Tapes != nil {
		if r := l.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(ModelTape); r.Error != nil {
			return fmt.Errorf("cleanup tape fail, %w", r.Error)
		}
		if r := l.db.WithContext(ctx).CreateInBatches(*results.Tapes, 100); r.Error != nil {
			return fmt.Errorf("insert tape fail, %w", r.Error)
		}
	}

	if results.Positions != nil {
		if r := l.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(ModelPosition); r.Error != nil {
			return fmt.Errorf("cleanup position fail, %w", r.Error)
		}
		if r := l.db.WithContext(ctx).CreateInBatches(*results.Positions, 100); r.Error != nil {
			return fmt.Errorf("insert position fail, %w", r.Error)
		}
	}

	return nil
}

func (l *Library) Trim(ctx context.Context, position, file bool) error {
	if !position {
		return nil
	}

	var current int64
	for {
		positions := make([]*Position, 0, batchSize)
		if r := l.db.WithContext(ctx).Where("id > ?", current).Order("id ASC").Limit(batchSize).Find(&positions); r.Error != nil {
			return fmt.Errorf("scan position fail, err= %w", r.Error)
		}
		if len(positions) == 0 {
			break
		}
		current = positions[len(positions)-1].ID

		tapeIDs := mapset.NewThreadUnsafeSetWithSize[int64](1)
		for _, posi := range positions {
			tapeIDs.Add(posi.TapeID)
		}

		tapes, err := l.MGetTape(ctx, tapeIDs.ToSlice()...)
		if err != nil {
			return fmt.Errorf("mget tape fail, %w", err)
		}

		needDelete := make([]int64, 0)
		for _, posi := range positions {
			if tape, has := tapes[posi.TapeID]; has && tape != nil {
				continue
			}

			needDelete = append(needDelete, posi.ID)
		}
		if len(needDelete) == 0 {
			continue
		}

		if err := l.DeletePositions(ctx, needDelete...); err != nil {
			return fmt.Errorf("delete position fail, %w", err)
		}
	}

	if !file {
		return nil
	}

	current = 0
	for {
		files := make([]*File, 0, batchSize)
		if r := l.db.WithContext(ctx).Where("id > ?", current).Order("id ASC").Limit(batchSize).Find(&files); r.Error != nil {
			return fmt.Errorf("scan file fail, err= %w", r.Error)
		}
		if len(files) == 0 {
			break
		}
		current = files[len(files)-1].ID

		fileIDs := lo.Map(files, func(f *File, _ int) int64 { return f.ID })
		positions, err := l.MGetPositionByFileID(ctx, fileIDs...)
		if err != nil {
			return fmt.Errorf("mget position by file id fail, %w", err)
		}

		needDelete := make([]int64, 0)
		for _, file := range files {
			if posis, has := positions[file.ID]; has && len(posis) > 0 {
				continue
			}

			needDelete = append(needDelete, file.ID)
		}
		if len(needDelete) == 0 {
			continue
		}

		if r := l.db.WithContext(ctx).Where("id IN (?)", needDelete).Delete(ModelFile); r.Error != nil {
			return fmt.Errorf("delete files fail, err= %w", r.Error)
		}
	}

	return nil
}

func listAll[T any](ctx context.Context, l *Library, items []T) ([]T, error) {
	v := new(T)
	id := reflect2.TypeOfPtr(*v).Elem().(reflect2.StructType).FieldByName("ID")

	var cursor int64
	for {
		batch := make([]T, 0, batchSize)
		if r := l.db.WithContext(ctx).Where("id > ?", cursor).Order("id ASC").Limit(batchSize).Find(&batch); r.Error != nil {
			return nil, fmt.Errorf("list files fail, cursor= %d, %w", cursor, r.Error)
		}
		if len(batch) == 0 {
			return items, nil
		}

		c := id.Get(batch[len(batch)-1]).(*int64)
		cursor = *c
		items = append(items, batch...)
	}
}
