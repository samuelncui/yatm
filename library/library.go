package library

import (
	"gorm.io/gorm"
)

type Library struct {
	db     *gorm.DB
	prefix string
}

func NewLibrary(db *gorm.DB, prefix string) *Library {
	return &Library{db: db, prefix: prefix}
}
