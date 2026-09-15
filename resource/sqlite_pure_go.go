//go:build !cgo

package resource

import (
	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	logrus.Debug("use pure go sqlite driver")
	return sqlite.Open(dsn)
}
