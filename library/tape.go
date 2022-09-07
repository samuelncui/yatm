package library

import (
	"os"
	"time"

	"gorm.io/gorm"
)

type Tape struct {
	ID               int64 `gorm:"primaryKey;autoIncrement"`
	Barcode          string
	Name             string
	Encryption       string
	CreateTimestamp  int64
	DestroyTimestamp int64
}

func (l *Library) TapeScope(db *gorm.DB) *gorm.DB {
	if l.prefix == "" {
		return db
	}
	return db.Table(l.prefix + "_tape")
}

type TapeFile struct {
	Path      string      `json:"path"`
	Size      int64       `json:"size"`
	Mode      os.FileMode `json:"mode"`
	ModTime   time.Time   `json:"mod_time"`
	WriteTime time.Time   `json:"write_time"`
	Hash      []byte      `json:"hash"` // sha256
}

// func (l *Library) SaveTape(ctx context.Context, tape *Tape, files []*TapeFile) (*Tape, error) {
// 	if r := l.db.WithContext(ctx).Scopes(l.TapeScope).Save(tape); r.Error != nil {
// 		return nil, fmt.Errorf("save tape fail, err= %w", r.Error)
// 	}

// 	positions := make([]*Position, 0, len(files))
// 	for _, file := range files {

// 	}
// 	l.db.WithContext(ctx).Scopes(l.PositionScope).CreateBatchSize()
// }
