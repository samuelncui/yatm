package library

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/samuelncui/tapemanager/entity"
)

var (
	ModelTape = new(Tape)
)

type Tape struct {
	ID            int64      `gorm:"primaryKey;autoIncrement" json:"id,omitempty"`
	Barcode       string     `gorm:"type:varchar(15);index:idx_barcode,unique" json:"barcode,omitempty"`
	Name          string     `gorm:"type:varchar(256)" json:"name,omitempty"`
	Encryption    string     `gorm:"type:varchar(2048)" json:"encryption,omitempty"`
	CreateTime    time.Time  `json:"create_time,omitempty"`
	DestroyTime   *time.Time `json:"destroy_time,omitempty"`
	CapacityBytes int64      `json:"capacity_bytes,omitempty"`
	WritenBytes   int64      `json:"writen_bytes,omitempty"`
}

type TapeFile struct {
	Path      string      `json:"path"`
	Size      int64       `json:"size"`
	Mode      os.FileMode `json:"mode"`
	ModTime   time.Time   `json:"mod_time"`
	WriteTime time.Time   `json:"write_time"`
	Hash      []byte      `json:"hash"` // sha256
}

func (l *Library) CreateTape(ctx context.Context, tape *Tape, files []*TapeFile) (*Tape, error) {
	tape.WritenBytes = 0
	for _, file := range files {
		tape.WritenBytes += file.Size
	}
	if tape.CapacityBytes == 0 {
		tape.CapacityBytes = tape.WritenBytes
	}

	if r := l.db.WithContext(ctx).Save(tape); r.Error != nil {
		return nil, fmt.Errorf("save tape fail, err= %w", r.Error)
	}

	positions := make([]*Position, 0, len(files))
	for _, file := range files {
		positions = append(positions, &Position{
			TapeID:    tape.ID,
			Path:      file.Path,
			Mode:      uint32(file.Mode),
			ModTime:   file.ModTime,
			WriteTime: file.WriteTime,
			Size:      file.Size,
			Hash:      file.Hash,
		})
	}

	if r := l.db.WithContext(ctx).CreateInBatches(positions, batchSize); r.Error != nil {
		return nil, fmt.Errorf("save tape position fail, %w", r.Error)
	}

	return tape, nil
}

func (l *Library) GetTape(ctx context.Context, id int64) (*Tape, error) {
	tapes, err := l.MGetTape(ctx, id)
	if err != nil {
		return nil, err
	}

	tape, ok := tapes[id]
	if !ok || tape == nil {
		return nil, ErrFileNotFound
	}

	return tape, nil
}

func (l *Library) DeleteTapes(ctx context.Context, ids ...int64) error {
	// if r := l.db.WithContext(ctx).Where("tape_id IN (?)", ids).Delete(ModelPosition); r.Error != nil {
	// 	return fmt.Errorf("delete file position fail, err= %w", r.Error)
	// }
	if r := l.db.WithContext(ctx).Where("id IN (?)", ids).Delete(ModelTape); r.Error != nil {
		return fmt.Errorf("delete tapes fail, err= %w", r.Error)
	}

	return nil
}

func (l *Library) ListTape(ctx context.Context, filter *entity.TapeFilter) ([]*Tape, error) {
	db := l.db.WithContext(ctx)
	if filter.Limit != nil {
		db = db.Limit(int(*filter.Limit))
	} else {
		db = db.Limit(20)
	}
	if filter.Offset != nil {
		db = db.Offset(int(*filter.Offset))
	}

	db = db.Order("create_time DESC")

	tapes := make([]*Tape, 0, 20)
	if r := db.Find(&tapes); r.Error != nil {
		return nil, fmt.Errorf("list tapes fail, err= %w", r.Error)
	}

	return tapes, nil
}

func (l *Library) MGetTape(ctx context.Context, tapeIDs ...int64) (map[int64]*Tape, error) {
	if len(tapeIDs) == 0 {
		return map[int64]*Tape{}, nil
	}

	tapes := make([]*Tape, 0, len(tapeIDs))
	if r := l.db.WithContext(ctx).Where("id IN (?)", tapeIDs).Find(&tapes); r.Error != nil {
		return nil, fmt.Errorf("mget tapes fail, err= %w", r.Error)
	}

	result := make(map[int64]*Tape, len(tapes))
	for _, tape := range tapes {
		result[tape.ID] = tape
	}

	return result, nil
}

func (l *Library) MGetTapeByBarcode(ctx context.Context, barcodes ...string) (map[string]*Tape, error) {
	if len(barcodes) == 0 {
		return map[string]*Tape{}, nil
	}

	tapes := make([]*Tape, 0, len(barcodes))
	if r := l.db.WithContext(ctx).Where("barcode IN (?)", barcodes).Find(&tapes); r.Error != nil {
		return nil, fmt.Errorf("mget tapes by barcode fail, err= %w", r.Error)
	}

	result := make(map[string]*Tape, len(tapes))
	for _, tape := range tapes {
		result[tape.Barcode] = tape
	}

	return result, nil
}
