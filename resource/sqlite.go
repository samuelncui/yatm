//go:build (darwin && amd64) || (darwin && arm64) || (freebsd && amd64) || (linux && arm) || (linux && arm64) || (linux && 386) || (linux && amd64) || (linux && s390x) || (windows && amd64)

package resource

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	return sqlite.Open(dsn)
}
