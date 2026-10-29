package resource

import (
	"errors"
	"fmt"
	"path/filepath"

	"gorm.io/gorm"
)

// TemporaryDB owns a request's disposable database and directory. Callers own
// its schema and must finish using DB before Close; durable stores use other owners.
type TemporaryDB struct {
	DB        *gorm.DB
	Directory string
	owner     *TemporaryDirectory
}

func OpenTemporaryDB(work, prefix string) (_ *TemporaryDB, rerr error) {
	// Retain ownership during construction so a failed open cannot leave a directory.
	owner, err := NewTemporaryDirectory(work, prefix)
	if err != nil {
		return nil, fmt.Errorf("create temporary database directory failed, %w", err)
	}
	stage := &TemporaryDB{Directory: owner.Directory, owner: owner}
	defer func() {
		if rerr != nil {
			rerr = errors.Join(rerr, stage.Close())
		}
	}()
	stage.DB, err = OpenSQLite(filepath.Join(owner.Directory, "state.sqlite"))
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
	if s.owner != nil {
		result = errors.Join(result, s.owner.Close())
		s.Directory = s.owner.Directory
	}
	return result
}
