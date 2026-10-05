//go:build !cgo

package resource

import (
	"strings"

	"github.com/sirupsen/logrus"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"
)

func openSQLite(dsn string) gorm.Dialector {
	logrus.Debug("use pure go sqlite driver")
	// Match the CGO driver's numeric-offset encoding. time.Time.String includes
	// timezone names such as +0730, which the default decoder cannot round-trip.
	base, query, _ := strings.Cut(dsn, "?")
	// modernc defaults to immediate busy errors. Match CGO's five-second wait for
	// ordinary Job/temporary handles and every replacement connection as well.
	return sqlite.New(sqlite.Config{DriverName: "sqlite", DSN: base + "?_time_format=sqlite&_pragma=busy_timeout(5000)&" + query})
}

func sqliteConnectionOptions() map[string][]string {
	return map[string][]string{"_pragma": {"busy_timeout(5000)", "synchronous(FULL)"}}
}
