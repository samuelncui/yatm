package resource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gorm.io/gorm"
)

// TemporaryDB owns a request's disposable database and directory. Callers own
// its schema and must finish using DB before Close; durable stores use other owners.
type TemporaryDB struct {
	DB        *gorm.DB
	Directory string
}

func OpenTemporaryDB(root, prefix string) (_ *TemporaryDB, rerr error) {
	// Retain ownership during construction so a failed open cannot leave a directory.
	directory, err := os.MkdirTemp(root, prefix)
	if err != nil {
		return nil, fmt.Errorf("create temporary database directory failed, %w", err)
	}
	stage := &TemporaryDB{Directory: directory}
	defer func() {
		if rerr != nil {
			rerr = errors.Join(rerr, stage.Close())
		}
	}()
	stage.DB, err = OpenSQLite(filepath.Join(directory, "state.sqlite"))
	if err != nil {
		return nil, err
	}
	return stage, nil
}

func (s *TemporaryDB) Close() error {
	// Release SQLite and its sidecars before removing the uniquely owned directory.
	var result error
	if s.DB != nil {
		result = closeDB(s.DB)
		s.DB = nil
	}
	if s.Directory != "" {
		result = errors.Join(result, os.RemoveAll(s.Directory))
		s.Directory = ""
	}
	return result
}
