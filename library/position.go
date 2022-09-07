package library

import (
	"time"

	"gorm.io/gorm"
)

type Position struct {
	ID     int64 `gorm:"primaryKey;autoIncrement"`
	FileID int64
	TapeID int64
	Path   string `gorm:"type:varchar(4096)"`

	Mode      uint32
	ModTime   time.Time
	WriteTime time.Time
	Size      int64
	Hash      []byte `gorm:"type:varbinary(32)"` // sha256
}

func (l *Library) PositionScope(db *gorm.DB) *gorm.DB {
	if l.prefix == "" {
		return db
	}
	return db.Table(l.prefix + "_position")
}
