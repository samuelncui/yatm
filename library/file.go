package library

type File struct {
	ID   int64  `gorm:"primaryKey;autoIncrement"`
	Path string `gorm:"type:varchar(4096)"`

	Name string `gorm:"type:varchar(256)"`
	Hash []byte `gorm:"type:varbinary(32)"` // sha256
	Size int64
}
