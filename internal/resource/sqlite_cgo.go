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

func sqliteConnectionOptions() map[string][]string {
	return map[string][]string{"_busy_timeout": {"5000"}, "_synchronous": {"FULL"}}
}
