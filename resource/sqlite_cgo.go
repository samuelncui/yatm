//go:build cgo

package resource

import (
	"github.com/sirupsen/logrus"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	logrus.Debug("use cgo sqlite driver")
	return sqlite.Open(dsn)
}
