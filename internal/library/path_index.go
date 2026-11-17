package library

import (
	"fmt"

	"gorm.io/gorm"
)

// pathIndex makes a full relative or root path unique within its scope column. A MySQL index key
// holds at most 3072 bytes, less than a 4096-byte utf8mb4 path, so MySQL enforces uniqueness on the
// path's SHA-256 and adds a non-unique prefix index for ordering, paging and equality lookups.
// SQLite keeps one unique index over the complete path.
type pathIndex struct {
	table  string
	name   string
	scope  string
	path   string
	prefix int
}

var (
	fileLocationPathIndex = pathIndex{table: "file_locations", name: "idx_file_locations_path",
		scope: "location_id", path: "path", prefix: 750}
	positionPathIndex = pathIndex{table: "positions", name: "idx_positions_media_path",
		scope: "media_id", path: "path", prefix: 750}
	locationRootIndex = pathIndex{table: "locations", name: "idx_locations_root",
		scope: "executor_id", path: "root_path", prefix: 600}
)

// createPathIndexes runs after AutoMigrate. An existing index of the same name is kept, so
// existing Catalogs are never rebuilt.
func createPathIndexes(db *gorm.DB) error {
	for _, index := range []pathIndex{fileLocationPathIndex, positionPathIndex, locationRootIndex} {
		if err := index.create(db, index.table); err != nil {
			return err
		}
	}
	return nil
}

// CreatePositionPathIndex adds the Position path index to a table with the Position layout, such as
// the offline migration's staging table.
func CreatePositionPathIndex(db *gorm.DB, table string) error {
	return positionPathIndex.create(db, table)
}

func (i pathIndex) create(db *gorm.DB, table string) error {
	statements := map[string]string{
		i.name: fmt.Sprintf("CREATE UNIQUE INDEX `%s` ON `%s`(`%s`,`%s`)", i.name, table, i.scope, i.path),
	}
	if db.Dialector.Name() == "mysql" {
		statements = map[string]string{
			i.name: fmt.Sprintf("CREATE UNIQUE INDEX `%s` ON `%s`(`%s`,(SHA2(`%s`,256)))",
				i.name, table, i.scope, i.path),
			i.name + "_prefix": fmt.Sprintf("CREATE INDEX `%s_prefix` ON `%s`(`%s`,`%s`(%d))",
				i.name, table, i.scope, i.path, i.prefix),
		}
	}
	for name, statement := range statements {
		if db.Migrator().HasIndex(table, name) {
			continue
		}
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create index %s failed, %w", name, err)
		}
	}
	return nil
}
