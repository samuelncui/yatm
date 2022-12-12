package library

import (
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
